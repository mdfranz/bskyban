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
	go func() {
		ctx := m.readContext()
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
