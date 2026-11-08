package tui

// Background timeline checks, the pending-posts queue, the --fancy highlight
// animation, and loading more of the timeline.

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mdfranz/bskyban/internal/bsky"
)

// nextTick schedules the next background check for new posts (nil if disabled).
func (m model) nextTick() tea.Cmd {
	if m.refresh <= 0 {
		return nil
	}
	return tea.Tick(m.refresh, func(time.Time) tea.Msg { return refreshTickMsg{} })
}

// checkPages bounds how far back one check looks for the overlap with what is shown.
const checkPages = 4

// startCheck fetches the newest timeline pages and finds the posts you do not have yet.
// It only reads; nothing is inserted until the result arrives (and, for the
// background check, until you press R).
func (m model) startCheck(manual bool) (model, tea.Cmd) {
	if m.timeline == nil {
		return m, nil
	}
	if m.followLoading {
		if manual {
			m.status = "still loading the timeline"
		}
		return m, nil
	}
	if m.checking {
		if manual { // a check is already running: show its result as soon as it lands
			m.applyWhenDone = true
			m.status = "checking for new posts…"
		}
		return m, nil
	}
	known := make(map[string]bool, len(m.items)+len(m.newPosts))
	for _, it := range m.items {
		known[it.Key()] = true
	}
	for _, it := range m.newPosts {
		known[it.Key()] = true
	}
	m.checking = true
	if manual {
		m.status = "checking for new posts…"
	}
	tl := m.timeline
	return m, func() tea.Msg {
		ctx := context.Background()
		var fresh []bsky.FeedItem
		got := map[string]bool{}
		cursor := ""
		for page := 0; page < checkPages; page++ {
			items, next, err := tl(ctx, cursor)
			if err != nil {
				return newItemsMsg{err: err, manual: manual}
			}
			overlap := false
			for _, it := range items {
				switch k := it.Key(); {
				case known[k]:
					overlap = true
				case !got[k]:
					got[k] = true
					fresh = append(fresh, it)
				}
			}
			if overlap || next == "" {
				return newItemsMsg{items: fresh, manual: manual}
			}
			cursor = next
		}
		return newItemsMsg{items: fresh, gap: true, manual: manual}
	}
}

// readingTop reports whether you are on the first post of the Following list, with
// nothing open or being written, so new posts can be added without moving anything you read.
func (m model) readingTop() bool {
	return m.active() == tabFollowing && m.cur == 0 && m.listTop == 0 && !m.inPost && !m.composing && !m.busy
}

// freshFor is how long a newly inserted post stays highlighted in fancy mode.
const freshFor = 2 * time.Second

// startAnim schedules the next highlight redraw while anything is still fading.
func (m model) startAnim() (model, tea.Cmd) {
	if len(m.fresh) == 0 || m.animating {
		return m, nil
	}
	m.animating = true
	return m, tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return animTickMsg{} })
}

// freshStyle is the highlight for a post inserted at t, or false once it has faded.
func (m model) freshStyle(key string) (lipgloss.Style, bool) {
	t, ok := m.fresh[key]
	if !ok {
		return lipgloss.Style{}, false
	}
	age := m.now().Sub(t)
	if age >= freshFor {
		return lipgloss.Style{}, false
	}
	i := min(int(max(age, 0)*time.Duration(len(theme.Fresh))/freshFor), len(theme.Fresh)-1)
	return lipgloss.NewStyle().Bold(true).Foreground(theme.Fresh[i]), true
}

// applyPending inserts the held-back posts at the top of the list. The post you
// are on stays selected, unless you are at the very top of the list, where the
// newest post becomes the selection.
func (m model) applyPending() model {
	n := len(m.newPosts)
	if n == 0 {
		m.status = "no new posts"
		return m
	}
	atTop := m.cur == 0 && m.listTop == 0 && !m.inPost
	m.items = append(append([]bsky.FeedItem{}, m.newPosts...), m.items...)
	if m.fancy {
		t := m.now()
		for _, it := range m.newPosts {
			m.fresh[it.Key()] = t
		}
	}
	if !atTop {
		m.cur += n
		m.listTop += n
	}
	m.status = fmt.Sprintf("%d new posts", n)
	if n == 1 {
		m.status = "1 new post"
	}
	if m.newGap {
		m.status += " (more arrived than could be fetched; the newest are shown)"
	}
	m.newPosts, m.newGap = nil, false
	return m.ensureVisible()
}

// mergeCandidates adds authors of newly reposted posts to the Reposts queue
// (at the end, so the account you are reviewing does not move).
func (m model) mergeCandidates(items []bsky.FeedItem) model {
	if m.loading || m.ctxs == nil {
		return m
	}
	m.list = m.a.MergeRepostCandidates(m.list, m.ctxs, items, m.backlog)
	return m
}

func loadTimeline(tl timelineFunc, cursor string) tea.Cmd {
	return func() tea.Msg {
		items, next, err := tl(context.Background(), cursor)
		return followingMsg{items, next, err}
	}
}

// maybeLoadMore fetches the next timeline page when the cursor nears the end.
func (m model) maybeLoadMore() (model, tea.Cmd) {
	if m.timeline == nil || m.tlCursor == "" || m.moreBusy || m.followLoading || len(m.items)-m.cur > 8 {
		return m, nil
	}
	m.moreBusy = true
	return m, loadTimeline(m.timeline, m.tlCursor)
}
