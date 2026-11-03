package main

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// step sends msg and, if it started an async action, feeds that action's result back.
func step(m model, msg tea.Msg) model {
	next, cmd := m.Update(msg)
	m = next.(model)
	if cmd != nil {
		switch r := cmd().(type) {
		case postedMsg, deletedMsg:
			next, _ = m.Update(r)
			m = next.(model)
		}
	}
	return m
}

func typeText(m model, s string) model {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}) // ignore the blink command
		m = next.(model)
	}
	return m
}

func feedWithTimeline(t *testing.T) (model, *fakePDS) {
	m, f := followingModel(t)
	m.timeline = func(context.Context, string) ([]FeedItem, string, error) { return nil, "", nil }
	return m, f
}

func TestComposeAndPost(t *testing.T) {
	m, f := feedWithTimeline(t)
	m = openCompose(m)
	if !m.composing {
		t.Fatal("n should open the compose box")
	}
	m = typeText(m, "q hello")
	if got := m.input.Value(); got != "q hello" {
		t.Fatalf("typing q must not quit; input = %q", got)
	}
	if v := m.View(); !strings.Contains(v, "New post") || !strings.Contains(v, "7/300") {
		t.Errorf("compose view:\n%s", v)
	}
	m = step(m, key("enter"))
	if m.composing || m.err != nil {
		t.Fatalf("composing=%v err=%v", m.composing, m.err)
	}
	if f.joined() != "post" {
		t.Errorf("calls = %q", f.joined())
	}
	if !strings.Contains(m.status, "posted") {
		t.Errorf("status = %q", m.status)
	}
}

func TestComposeEscDiscardsAndLongPostIsRefused(t *testing.T) {
	m, f := feedWithTimeline(t)
	m = openCompose(m)
	m = typeText(m, "draft")
	next, _ := m.Update(key("esc"))
	m = next.(model)
	if m.composing || f.joined() != "" {
		t.Fatalf("esc should discard without posting (composing=%v calls=%q)", m.composing, f.joined())
	}
	m = openCompose(m)
	m.input.SetValue(strings.Repeat("x", maxPostGraphemes+1))
	m = step(m, key("enter"))
	if !m.composing || m.err == nil || f.joined() != "" {
		t.Errorf("over-long post should stay open with an error (composing=%v err=%v calls=%q)", m.composing, m.err, f.joined())
	}
}

func TestDeleteOwnPostNeedsConfirmation(t *testing.T) {
	m, f := feedWithTimeline(t)
	own := FeedItem{Author: Profile{DID: "did:me", Handle: "me.bsky.social"}, URI: "at://did:me/app.bsky.feed.post/3k",
		Post: Post{Text: "mine", CreatedAt: "2026-11-01T07:00:00Z"}}
	m.items = append([]FeedItem{own}, m.items...)
	m = step(m, key("d"))
	if f.joined() != "" || len(m.items) != 3 {
		t.Fatalf("first d must only ask (calls=%q items=%d)", f.joined(), len(m.items))
	}
	m = step(m, key("d"))
	if f.joined() != "delete-post" || len(m.items) != 2 {
		t.Errorf("calls=%q items=%d", f.joined(), len(m.items))
	}
	if len(f.deleted) != 1 || f.deleted[0] != "3k" {
		t.Errorf("deleted = %v", f.deleted)
	}
	m = step(m, key("d")) // now on someone else's post
	m = step(m, key("d"))
	if f.joined() != "delete-post" || len(m.items) != 2 {
		t.Errorf("someone else's post must not be deleted (calls=%q)", f.joined())
	}
}

// openCompose presses n without running the cursor-blink command it returns.
func openCompose(m model) model {
	next, _ := m.Update(key("n"))
	return next.(model)
}

func TestPostsModelIsAFeedOfYourOwnPosts(t *testing.T) {
	a, _ := newTestApp(t)
	m := newPostsModel(a, func(context.Context, string) ([]FeedItem, string, error) { return nil, "", nil })
	m.followLoading = false
	m.width, m.height = 100, 30
	m.items = []FeedItem{{Author: Profile{DID: "did:me", Handle: "me.bsky.social"}, URI: "at://did:me/app.bsky.feed.post/3k", Post: Post{Text: "mine"}}}
	v := m.View()
	for _, w := range []string{"bskyban posts", "mine", "d delete", "n new post"} {
		if !strings.Contains(v, w) {
			t.Errorf("missing %q:\n%s", w, v)
		}
	}
	if next, _ := m.Update(key("tab")); next.(model).tab != 0 {
		t.Error("a single-tab model should not switch tabs")
	}
}
