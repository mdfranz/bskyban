package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/testpds"
)

func TestStaleReadMessagesIgnored(t *testing.T) {
	a, _ := testpds.New(t)
	tl := func(context.Context, string) ([]bsky.FeedItem, string, error) {
		return nil, "", nil
	}
	m := newFeedModel(a, func(context.Context) ([]bsky.Profile, map[string]app.RepostCtx, error) { return nil, nil, nil }, tl)
	m.followLoading = false
	m.tlCursor = "c1"
	m.items = []bsky.FeedItem{{URI: "at://1", Author: bsky.Profile{DID: "did:1", Handle: "one"}}}

	// Stale timeline page is ignored.
	next, _ := m.Update(followingMsg{reqID: 9999, items: []bsky.FeedItem{{URI: "at://stale"}}, err: errors.New("stale")})
	m = next.(model)
	if len(m.items) != 1 || m.err != nil {
		t.Fatalf("stale followingMsg applied: items=%d err=%v", len(m.items), m.err)
	}

	// Stale refresh check is ignored.
	m, _ = m.startCheck(false)
	next, _ = m.Update(newItemsMsg{reqID: m.checkID + 10, items: []bsky.FeedItem{{URI: "at://stale"}}, err: errors.New("stale")})
	m = next.(model)
	if !m.checking || m.refreshErr != nil || len(m.newPosts) != 0 {
		t.Fatalf("stale newItemsMsg applied: checking=%v err=%v new=%d", m.checking, m.refreshErr, len(m.newPosts))
	}

	// Stale profile and posts responses are ignored.
	_ = m.fetchProfile("did:1")
	next, _ = m.Update(profileMsg{did: "did:1", reqID: m.profileReqIDs["did:1"] + 10, err: errors.New("stale")})
	m = next.(model)
	if _, ok := m.profiles["did:1"]; !ok || m.err != nil {
		t.Fatalf("stale profileMsg applied: ok=%v err=%v", ok, m.err)
	}
	next, _ = m.Update(postsMsg{did: "did:1", reqID: m.postsReqIDs["did:1"] + 10, err: errors.New("stale")})
	m = next.(model)
	if _, ok := m.posts["did:1"]; !ok || m.err != nil {
		t.Fatalf("stale postsMsg applied: ok=%v err=%v", ok, m.err)
	}

	// Stale follows page response is ignored.
	m.follows = &followsBrowser{actor: bsky.Profile{DID: "did:1"}, loading: true, reqID: 42}
	next, _ = m.Update(followsPageMsg{browser: m.follows, reqID: 41, err: errors.New("stale")})
	m = next.(model)
	if !m.follows.loading || m.err != nil {
		t.Fatalf("stale followsPageMsg applied: loading=%v err=%v", m.follows.loading, m.err)
	}
}

func TestQuitCancelsInFlightReads(t *testing.T) {
	m, _ := candidates(t)
	done := make(chan error, 1)
	ctx := m.readContext() // taken before m is reassigned below
	go func() {
		<-ctx.Done()
		done <- ctx.Err()
	}()

	if m.readCtx.Err() != nil {
		t.Fatalf("readCtx canceled before quit: %v", m.readCtx.Err())
	}
	next, cmd := m.Update(key("q"))
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd")
	}
	if err := <-done; err != context.Canceled {
		t.Fatalf("readCtx.Err() = %v, want context.Canceled", err)
	}
}

func TestAbortedQuitKeepsReadsWorking(t *testing.T) {
	m, _ := candidates(t)
	m.busy = true
	next, _ := m.Update(key("q"))
	m = next.(model)
	if !m.quitting {
		t.Fatal("expected quit to wait for the busy action")
	}
	if err := m.readContext().Err(); err != nil {
		t.Fatalf("reads cancelled while quit is only deferred: %v", err)
	}
	// The action fails, so the quit is aborted and the TUI keeps running.
	next, cmd := m.Update(actMsg{undo: undoEntry{profile: m.list[0], decision: "blocked"}, err: errors.New("boom")})
	m = next.(model)
	if m.quitting || cmd != nil {
		t.Fatalf("expected quit aborted: quitting=%v cmd=%v", m.quitting, cmd != nil)
	}
	if err := m.readContext().Err(); err != nil {
		t.Fatalf("TUI still running but reads are cancelled: %v", err)
	}
}

func TestDeferredQuitCancelsReads(t *testing.T) {
	m, _ := candidates(t)
	ctx := m.readContext()
	m.busy = true
	next, _ := m.Update(key("q"))
	m = next.(model)
	// The action succeeds, so the deferred quit goes ahead.
	next, cmd := m.Update(actMsg{undo: undoEntry{profile: m.list[0], decision: "kept"}})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected tea.Quit after the action finished")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("reads not cancelled on deferred quit: %v", ctx.Err())
	}
}
