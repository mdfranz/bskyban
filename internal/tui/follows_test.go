package tui

import (
	"strings"
	"testing"

	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/termtext"
)

func TestFollowingDescriptionsFitOneLine(t *testing.T) {
	m, _ := followingModel(t)
	m.width, m.height = 40, 12
	m.follows = &followsBrowser{actor: bsky.Profile{Handle: "friend"}, accounts: []bsky.Profile{
		{Handle: "first.example", DisplayName: "First", Description: "First line\nsecond line " + strings.Repeat("more ", 30)},
		{Handle: "second.example", DisplayName: "Second", Description: "Another bio"},
	}, selected: 1, loaded: true}
	view := termtext.StripANSI(m.followsView())
	lines := strings.Split(strings.TrimSpace(view), "\n")
	if len(lines) != 6 || !strings.Contains(view, "First line second line") || !strings.Contains(view, "Another bio") {
		t.Fatalf("expected one bio line per account: %q", view)
	}
	for _, line := range lines {
		if strings.Contains(line, "more more more more more more more more") {
			t.Fatalf("bio was not truncated: %q", line)
		}
	}
}

func TestBrowseFollowingPages(t *testing.T) {
	m, _ := followingModel(t)
	opened := withBrowser(t, nil)
	next, cmd := m.Update(key("F"))
	m = next.(model)
	if cmd == nil || m.follows == nil {
		t.Fatal("F should load the selected author's follows")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if !strings.Contains(m.View(), "First (@first.example)") {
		t.Fatalf("first page missing: %s", m.View())
	}
	next, cmd = m.Update(key("down"))
	m = next.(model)
	if cmd == nil {
		t.Fatal("down at end should load the next page")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	next, _ = m.Update(key("down"))
	m = next.(model)
	if !strings.Contains(m.View(), "▸ Second (@second.example)") {
		t.Fatalf("second page missing: %s", m.View())
	}
	next, _ = m.Update(key("enter"))
	m = next.(model)
	if len(*opened) != 1 || (*opened)[0] != "https://bsky.app/profile/did:second" {
		t.Fatalf("opened = %v", *opened)
	}
	next, _ = m.Update(key("esc"))
	m = next.(model)
	if m.follows != nil || m.cur != 0 {
		t.Fatal("escape should return to the original post")
	}
}

func TestClosedFollowingBrowserIgnoresLateResults(t *testing.T) {
	m, _ := followingModel(t)
	next, cmd := m.Update(key("F"))
	m = next.(model)
	next, _ = m.Update(key("esc"))
	m = next.(model)
	msg := cmd()
	next, _ = m.Update(msg)
	if next.(model).follows != nil {
		t.Fatal("late response reopened browser")
	}
}
