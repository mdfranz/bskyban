package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mdfranz/bskyban/internal/bsky"
)

// longTimeline is followingModel with n multi-line posts, each with a quote.
func longTimeline(t *testing.T, n int) model {
	m, _ := followingModel(t)
	text := strings.Repeat("words about things https://example.com/a/path and @someone.bsky.social ", 4)
	m.items = nil
	for i := range n {
		m.items = append(m.items, bsky.FeedItem{
			URI:    fmt.Sprintf("at://did:a%d/app.bsky.feed.post/rk%d", i, i),
			Author: bsky.Profile{DID: fmt.Sprintf("did:a%d", i%20), Handle: fmt.Sprintf("user%d.bsky.social", i%20)},
			Post:   bsky.Post{Text: text, CreatedAt: "2026-11-01T06:00:00Z", Quote: "@q.bsky.social: " + text},
		})
	}
	return m
}

// slowListTop is the scroll position the original one-step-at-a-time loop chose.
func slowListTop(m model) int {
	top := min(m.listTop, m.cur)
	for top < m.cur {
		h := 0
		for i := top; i <= m.cur; i++ {
			h += len(m.itemLines(i, m.listWidth()))
		}
		if h <= m.avail() {
			break
		}
		top++
	}
	return top
}

func TestEndJumpIsFastAndShowsTheLastPost(t *testing.T) {
	m := longTimeline(t, 500)
	start := time.Now()
	m = press(m, "end")
	if d := time.Since(start); d > 2*time.Second { // was ~37s: every post re-rendered once per scroll step
		t.Fatalf("end took %v", d)
	}
	if m.cur != 499 {
		t.Fatalf("cur = %d", m.cur)
	}
	if !strings.Contains(m.View(), "user19.bsky.social") { // the last post's author
		t.Errorf("last post not on screen:\n%s", m.View())
	}
}

func TestEnsureVisibleScrollsOnlyAsFarAsNeeded(t *testing.T) {
	m := longTimeline(t, 60)
	for _, tc := range []struct{ top, cur int }{{0, 0}, {0, 3}, {0, 40}, {10, 12}, {5, 59}, {30, 30}} {
		m.listTop, m.cur = tc.top, tc.cur
		want := slowListTop(m)
		if got := m.ensureVisible().listTop; got != want {
			t.Errorf("listTop %d, cur %d: got top %d, want %d", tc.top, tc.cur, got, want)
		}
	}
}

func TestWrappedTextIsRewrappedAfterAWidthChange(t *testing.T) {
	m := longTimeline(t, 3)
	wide := m.itemLines(0, m.listWidth())
	next, _ := m.Update(tea.WindowSizeMsg{Width: 70, Height: 40})
	m = next.(model)
	narrow := m.itemLines(0, m.listWidth())
	if len(narrow) <= len(wide) && strings.Join(narrow, "") == strings.Join(wide, "") {
		t.Errorf("same lines at width 70 as at 120:\n%s", strings.Join(narrow, "\n"))
	}
}

func TestSpinnerStopsWhenNothingIsLoading(t *testing.T) {
	m, _ := followingModel(t) // timeline loaded, nothing open
	next, cmd := m.Update(spinner.TickMsg{})
	m = next.(model)
	if cmd != nil || m.spinOn {
		t.Fatalf("spinner kept ticking with nothing loading")
	}
	// Opening a post whose author is not fetched yet starts it again.
	next, cmd = m.Update(key("right"))
	m = next.(model)
	if !m.spinOn || cmd == nil {
		t.Fatalf("spinner did not restart for a loading profile")
	}
}
