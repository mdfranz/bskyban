package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
)

func post(uri, handle, by string) bsky.FeedItem {
	return bsky.FeedItem{
		URI: uri, By: by,
		Author: bsky.Profile{DID: "did:" + handle, Handle: handle + ".bsky.social", DisplayName: handle},
		Post:   bsky.Post{Text: "text of " + uri, CreatedAt: "2026-11-01T06:00:00Z"},
	}
}

// script serves a fixed list of timeline pages (newest first) and counts calls.
type script struct {
	pages [][]bsky.FeedItem
	calls int
	err   error
}

func (s *script) fn(_ context.Context, cursor string) ([]bsky.FeedItem, string, error) {
	s.calls++
	if s.err != nil {
		return nil, "", s.err
	}
	i := 0
	if cursor != "" {
		fmt.Sscanf(cursor, "c%d", &i)
	}
	if i >= len(s.pages) {
		return nil, "", nil
	}
	next := ""
	if i+1 < len(s.pages) {
		next = fmt.Sprintf("c%d", i+1)
	}
	return s.pages[i], next, nil
}

func refreshModel(t *testing.T, s *script, shown ...bsky.FeedItem) model {
	a, _ := newTestApp(t)
	m := newFeedModel(a.App, nil, s.fn)
	m.loading, m.followLoading = false, false
	m.ctxs = map[string]app.RepostCtx{}
	m.refresh = 30 * time.Second
	m.width, m.height = 120, 40
	m.items = shown
	return m
}

// check runs one check to completion and feeds the result back into the model.
func check(t *testing.T, m model, manual bool) model {
	t.Helper()
	m, cmd := m.startCheck(manual)
	if cmd == nil {
		return m
	}
	next, _ := m.Update(cmd())
	return next.(model)
}

// reading is a model whose reader has a post open, so new posts are held back.
func reading(m model) model {
	m.inPost = true
	return m
}

func uris(items []bsky.FeedItem) string {
	var u []string
	for _, it := range items {
		u = append(u, it.URI)
	}
	return strings.Join(u, ",")
}

func TestBackgroundCheckHoldsNewPostsBack(t *testing.T) {
	A, B, C := post("A", "a", ""), post("B", "b", ""), post("C", "c", "")
	s := &script{pages: [][]bsky.FeedItem{{post("N2", "n2", ""), post("N1", "n1", ""), A, B, C}}}
	m := refreshModel(t, s, A, B, C)
	m.cur = 1
	m = check(t, m, false)
	if uris(m.items) != "A,B,C" || m.cur != 1 {
		t.Fatalf("a background check must not touch the list: items=%s cur=%d", uris(m.items), m.cur)
	}
	if uris(m.newPosts) != "N2,N1" {
		t.Fatalf("newPosts = %s", uris(m.newPosts))
	}
	if !strings.Contains(m.View(), "Following (+2 new)") {
		t.Errorf("header should announce the new posts:\n%s", m.View())
	}
	if !strings.Contains(m.hint(), "R refresh") {
		t.Errorf("hint should offer R: %q", m.hint())
	}
	// checking again does not count the same posts twice
	m = check(t, m, false)
	if len(m.newPosts) != 2 {
		t.Errorf("repeat check duplicated posts: %s", uris(m.newPosts))
	}
}

func TestRKeyInsertsPostsAndKeepsYourPlace(t *testing.T) {
	A, B, C := post("A", "a", ""), post("B", "b", ""), post("C", "c", "")
	s := &script{pages: [][]bsky.FeedItem{{post("N2", "n2", ""), post("N1", "n1", ""), A, B, C}}}

	m := refreshModel(t, s, A, B, C)
	m.cur = 1
	m = check(t, m, false)
	callsBefore := s.calls
	m = press(m, "R")
	if uris(m.items) != "N2,N1,A,B,C" || m.items[m.cur].URI != "B" {
		t.Fatalf("selection moved: items=%s cur=%d (%s)", uris(m.items), m.cur, m.items[m.cur].URI)
	}
	if s.calls != callsBefore {
		t.Error("R with posts already waiting should not fetch again")
	}
	if len(m.newPosts) != 0 || !strings.Contains(m.status, "2 new posts") {
		t.Errorf("newPosts=%d status=%q", len(m.newPosts), m.status)
	}
	if strings.Contains(m.View(), "new)") {
		t.Error("the (+N new) marker should clear")
	}

	// reading a post: it stays on screen
	m = refreshModel(t, s, A, B, C)
	m.cur, m.inPost = 2, true
	m = check(t, m, false)
	m = press(m, "R")
	if !m.inPost || m.items[m.cur].URI != "C" {
		t.Errorf("post view should keep showing C: inPost=%v cur=%s", m.inPost, m.items[m.cur].URI)
	}

	// at the very top of the list you land on the newest post
	m = refreshModel(t, s, A, B, C)
	m = check(t, m, false)
	m = press(m, "R")
	if m.cur != 0 || m.items[0].URI != "N2" {
		t.Errorf("at the top, selection should be the newest post: cur=%d first=%s", m.cur, m.items[0].URI)
	}
}

func TestManualRefreshFetchesWhenNothingIsWaiting(t *testing.T) {
	A := post("A", "a", "")
	s := &script{pages: [][]bsky.FeedItem{{A}}}
	m := refreshModel(t, s, A)
	m, cmd := m.startCheck(true)
	next, _ := m.Update(cmd())
	m = next.(model)
	if s.calls != 1 || m.status != "no new posts" {
		t.Errorf("calls=%d status=%q", s.calls, m.status)
	}
	s.pages = [][]bsky.FeedItem{{post("N", "n", ""), A}}
	m = press(m, "R") // R in the UI: no pending posts, so it checks and then applies
	if cmd := m.checking; !cmd {
		t.Fatal("R should start a check")
	}
}

func TestCheckWalksBackUntilItOverlaps(t *testing.T) {
	A := post("A", "a", "")
	s := &script{pages: [][]bsky.FeedItem{
		{post("N4", "n4", ""), post("N3", "n3", "")},
		{post("N2", "n2", ""), post("N1", "n1", "")},
		{A},
	}}
	m := check(t, reading(refreshModel(t, s, A)), false)
	if uris(m.newPosts) != "N4,N3,N2,N1" || m.newGap || s.calls != 3 {
		t.Errorf("new=%s gap=%v calls=%d", uris(m.newPosts), m.newGap, s.calls)
	}

	// more new posts than a check will look at: it stops and says so
	var pages [][]bsky.FeedItem
	for p := 0; p < 10; p++ {
		pages = append(pages, []bsky.FeedItem{post(fmt.Sprintf("P%da", p), "x", ""), post(fmt.Sprintf("P%db", p), "y", "")})
	}
	s = &script{pages: pages}
	m = check(t, reading(refreshModel(t, s, A)), false)
	if !m.newGap || s.calls != checkPages || len(m.newPosts) != 2*checkPages {
		t.Errorf("gap=%v calls=%d new=%d", m.newGap, s.calls, len(m.newPosts))
	}
	m = press(m, "R")
	if !strings.Contains(m.status, "more arrived than could be fetched") {
		t.Errorf("status = %q", m.status)
	}
}

func TestSamePostRepostedByTwoPeopleIsTwoEntries(t *testing.T) {
	A := post("A", "a", "")
	s := &script{pages: [][]bsky.FeedItem{{post("X", "x", "alice.bsky.social"), post("X", "x", "bob.bsky.social"), A}}}
	m := check(t, reading(refreshModel(t, s, A)), false)
	if len(m.newPosts) != 2 {
		t.Fatalf("newPosts = %s", uris(m.newPosts))
	}
	m = check(t, m, false)
	if len(m.newPosts) != 2 {
		t.Errorf("second check added duplicates: %d", len(m.newPosts))
	}
}

func TestFailedChecks(t *testing.T) {
	A := post("A", "a", "")
	s := &script{pages: [][]bsky.FeedItem{{A}}, err: errors.New("network down")}
	m := check(t, refreshModel(t, s, A), false)
	if m.refreshErr == nil || m.err != nil {
		t.Fatalf("a background failure should only mark the header: refreshErr=%v err=%v", m.refreshErr, m.err)
	}
	if !strings.Contains(m.View(), "refresh failed") {
		t.Error("header should show that refreshing failed")
	}
	m = check(t, m, false) // same failure again on the next tick
	logged := func() []store.AuditEvent {
		evs, err := store.ReadAudit(m.a.Audit.Path())
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}
	if evs := logged(); len(evs) != 1 || evs[0].Action != "refresh" || evs[0].OK || !strings.Contains(evs[0].Error, "network down") {
		t.Fatalf("a persisting background failure should be logged once: %+v", evs)
	}
	m = check(t, m, true)
	if m.err == nil || !strings.Contains(m.err.Error(), "network down") {
		t.Errorf("a manual refresh should report the error: %v", m.err)
	}
	if len(logged()) != 2 {
		t.Errorf("a manual refresh failure is always logged: %d entries", len(logged()))
	}
	s.err = nil
	m.err = nil
	m = check(t, m, false)
	if m.refreshErr != nil || strings.Contains(m.View(), "refresh failed") {
		t.Error("a good check should clear the warning")
	}
}

func TestNewRepostAuthorsJoinTheQueue(t *testing.T) {
	A := post("A", "a", "")
	existing := bsky.Profile{DID: "did:old", Handle: "old.bsky.social"}
	followed := post("F", "followed", "alice.bsky.social")
	followed.Author.Viewer.Following = "at://follow"
	decided := post("D", "decided", "alice.bsky.social")
	s := &script{pages: [][]bsky.FeedItem{{
		post("R1", "newguy", "alice.bsky.social"),
		followed,
		decided,
		post("R2", "old", "bob.bsky.social"), // already in the queue, reposted by someone else too
		post("O", "plain", ""),               // not a repost
		A,
	}}}
	m := refreshModel(t, s, A)
	m.list = []bsky.Profile{existing}
	m.ctxs["did:old"] = app.RepostCtx{By: []string{"alice.bsky.social"}}
	m.a.State.Entries["did:decided"] = store.Entry{Decision: store.Kept}
	m = check(t, m, false)

	var got []string
	for _, p := range m.list {
		got = append(got, p.Handle)
	}
	if strings.Join(got, ",") != "old.bsky.social,newguy.bsky.social" {
		t.Errorf("queue = %v, want the new author appended after the existing one", got)
	}
	if by := m.ctxs["did:old"].By; strings.Join(by, ",") != "alice.bsky.social,bob.bsky.social" {
		t.Errorf("existing candidate's reposters = %v", by)
	}
	if m.ctxs["did:newguy"].Post.Text == "" {
		t.Error("new candidate should carry the reposted post")
	}
	if !strings.Contains(m.tabBar(), "Reposts (2)") {
		t.Errorf("tab count should grow: %q", m.tabBar())
	}
	if m.idx != 0 {
		t.Error("the account under review must not move")
	}
}

func TestTickSchedulesTheNextCheck(t *testing.T) {
	A := post("A", "a", "")
	s := &script{pages: [][]bsky.FeedItem{{A}}}
	m := refreshModel(t, s, A)
	if m.nextTick() == nil {
		t.Fatal("30s refresh should schedule ticks")
	}
	next, cmd := m.Update(refreshTickMsg{})
	if cmd == nil || !next.(model).checking {
		t.Error("a tick should start a check and schedule the next tick")
	}
	m.refresh = 0
	if m.nextTick() != nil {
		t.Error("--refresh 0 must switch polling off")
	}
	m.followLoading = true
	if _, cmd := m.startCheck(false); cmd != nil {
		t.Error("no check while the first page is still loading")
	}
}

func TestReviewModeHasNoRefresh(t *testing.T) {
	m, _ := candidates(t)
	if m.nextTick() != nil || strings.Contains(m.hint(), "refresh") {
		t.Error("follower review has no timeline to refresh")
	}
	before := m
	m = press(m, "R")
	if m.status != before.status || m.checking {
		t.Error("R should do nothing in review mode")
	}
}

func TestRWhileACheckIsRunning(t *testing.T) {
	A := post("A", "a", "")
	s := &script{pages: [][]bsky.FeedItem{{post("N1", "n1", ""), A}}}

	// posts are already waiting and another check is in flight: insert them now
	m := check(t, reading(refreshModel(t, s, A)), false)
	m.checking = true
	m = press(m, "R")
	if uris(m.items) != "N1,A" || m.status != "1 new post" {
		t.Errorf("R should insert the waiting post at once: items=%s status=%q", uris(m.items), m.status)
	}

	// nothing waiting yet: R waits for the running check and then applies it
	m = reading(refreshModel(t, s, A))
	m, cmd := m.startCheck(false)
	m = press(m, "R")
	if !m.applyWhenDone || uris(m.items) != "A" {
		t.Fatalf("R during a check should wait: applyWhenDone=%v items=%s", m.applyWhenDone, uris(m.items))
	}
	next, _ := m.Update(cmd()) // the in-flight check lands
	m = next.(model)
	if uris(m.items) != "N1,A" || m.applyWhenDone || m.status != "1 new post" {
		t.Errorf("result should be applied when it lands: items=%s wait=%v status=%q", uris(m.items), m.applyWhenDone, m.status)
	}
}

func TestNewPostsInsertThemselvesOnlyAtTheTop(t *testing.T) {
	A := post("A", "a", "")
	s := &script{pages: [][]bsky.FeedItem{{post("N1", "n1", ""), A}}}

	m := check(t, refreshModel(t, s, A), false) // on the first post of the list
	if uris(m.items) != "N1,A" || len(m.newPosts) != 0 || m.cur != 0 {
		t.Errorf("at the top the new post should be added with the newest selected: items=%s held=%s cur=%d", uris(m.items), uris(m.newPosts), m.cur)
	}

	m = check(t, reading(refreshModel(t, s, A)), false) // reading a post
	if uris(m.items) != "A" || uris(m.newPosts) != "N1" {
		t.Errorf("while reading, it must be held back: items=%s held=%s", uris(m.items), uris(m.newPosts))
	}

	m = refreshModel(t, s, A)
	m.composing = true
	m = check(t, m, false)
	if uris(m.items) != "A" {
		t.Errorf("not while writing a post: items=%s", uris(m.items))
	}
}
