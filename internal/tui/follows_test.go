package tui

import (
	"strings"
	"testing"

	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/termtext"
)

func TestFollowsBrowserUsesProfileAndPostsPanels(t *testing.T) {
	m, _ := followingModel(t)
	m.follows = &followsBrowser{actor: bsky.Profile{Handle: "friend"}, accounts: []bsky.Profile{
		{DID: "did:first", Handle: "first.example", DisplayName: "First"},
		{DID: "did:second", Handle: "second.example", DisplayName: "Second"},
	}, loaded: true}
	posts := []bsky.Post{{Text: "newest from first", CreatedAt: "2026-11-01T06:00:00Z"}}
	m.posts["did:first"] = &posts
	view := termtext.StripLinks(termtext.StripANSI(m.View()))
	for _, want := range []string{"Accounts followed by @friend", "First", "@first.example", "1 of 2", "Recent posts", "newest from first"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestFollowsBrowserKeys(t *testing.T) {
	m, _ := followingModel(t)
	accounts := make([]bsky.Profile, 3)
	for i := range accounts {
		accounts[i] = bsky.Profile{DID: "did:" + string(rune('a'+i)), Handle: string(rune('a'+i)) + ".example"}
	}
	m.follows = &followsBrowser{actor: bsky.Profile{Handle: "friend"}, accounts: accounts, loaded: true}
	var long []bsky.Post
	for range 30 {
		long = append(long, bsky.Post{Text: "post", CreatedAt: "2026-11-01T06:00:00Z"})
	}
	m.posts["did:a"] = &long

	m = press(m, "down")
	if m.follows.selected != 0 || m.scroll == 0 {
		t.Errorf("down should scroll the posts, not move: selected=%d scroll=%d", m.follows.selected, m.scroll)
	}
	m = press(m, "right")
	if m.follows.selected != 1 || m.scroll != 0 {
		t.Errorf("right should move to the next account and reset the scroll: selected=%d scroll=%d", m.follows.selected, m.scroll)
	}
	m = press(m, "left")
	m = press(m, "left")
	if m.follows.selected != 0 {
		t.Errorf("left stops at the first account: %d", m.follows.selected)
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
	if !strings.Contains(m.View(), "@first.example") {
		t.Fatalf("first page missing: %s", m.View())
	}
	next, cmd = m.Update(key("right"))
	m = next.(model)
	if cmd == nil {
		t.Fatal("moving to the end should load the next page")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	next, _ = m.Update(key("right"))
	m = next.(model)
	if !strings.Contains(m.View(), "@second.example") {
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
