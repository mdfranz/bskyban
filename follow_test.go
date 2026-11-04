package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mdfranz/bskyban/internal/store"
)

func TestFollowKeyFollowsAndUndoUnfollows(t *testing.T) {
	m, f := candidates(t)
	m = press(m, "f")
	if f.joined() != "follow" {
		t.Fatalf("calls = %s, want follow", f.joined())
	}
	if got := m.a.st.Entries["did:a"].Decision; got != store.Followed {
		t.Errorf("decision = %q, want followed", got)
	}
	if m.idx != 1 {
		t.Errorf("should advance after following, idx=%d", m.idx)
	}
	if rec := m.history[0].record; !strings.Contains(rec, "app.bsky.graph.follow") {
		t.Errorf("history record = %q, want a follow record", rec)
	}
	m = press(m, "left")
	if v := m.View(); !strings.Contains(v, "● followed") {
		t.Error("revisited account should show the followed badge")
	}
	m = press(m, "u")
	if f.joined() != "follow,unfollow" {
		t.Errorf("calls = %s, want follow,unfollow", f.joined())
	}
	if _, ok := m.a.st.Entries["did:a"]; ok || m.idx != 0 {
		t.Errorf("undo should clear the decision and step back: entries=%v idx=%d", m.a.st.Entries, m.idx)
	}
}

func TestOverridingAFollowUnfollowsFirst(t *testing.T) {
	m, f := candidates(t)
	m = press(m, "f")
	m = press(m, "left")
	m = press(m, "k") // keep means "no relationship": the follow is removed
	if f.joined() != "follow,unfollow" || m.a.st.Entries["did:a"].Decision != store.Kept {
		t.Fatalf("calls=%s decision=%q", f.joined(), m.a.st.Entries["did:a"].Decision)
	}
	m = press(m, "u") // undoing the keep restores the follow
	if f.joined() != "follow,unfollow,follow" || m.a.st.Entries["did:a"].Decision != store.Followed {
		t.Errorf("after undo: calls=%s decision=%q", f.joined(), m.a.st.Entries["did:a"].Decision)
	}
	m = press(m, "u") // and undoing the original follow removes the *new* record
	if f.joined() != "follow,unfollow,follow,unfollow" {
		t.Errorf("calls = %s", f.joined())
	}
	if _, ok := m.a.st.Entries["did:a"]; ok {
		t.Error("everything undone: no decision should remain")
	}
	evs, _ := store.ReadAudit(m.a.audit.Path())
	var got []string
	for _, e := range evs {
		got = append(got, e.Action)
	}
	if want := "follow,unfollow,keep,undo,undo"; strings.Join(got, ",") != want {
		t.Errorf("audit actions = %v, want %s", got, want)
	}
}

func TestFollowingABlockedAccountLiftsTheBlock(t *testing.T) {
	m, f := candidates(t)
	m = press(m, "b")
	m = press(m, "left")
	m = press(m, "f")
	if f.joined() != "block,unblock,follow" || m.a.st.Entries["did:a"].Decision != store.Followed {
		t.Fatalf("calls=%s decision=%q", f.joined(), m.a.st.Entries["did:a"].Decision)
	}
	m = press(m, "u") // back to blocked
	if f.joined() != "block,unblock,follow,unfollow,block" || m.a.st.Entries["did:a"].Decision != store.Blocked {
		t.Errorf("after undo: calls=%s decision=%q", f.joined(), m.a.st.Entries["did:a"].Decision)
	}
}

func TestBlockingAFollowedAccountUnfollowsIt(t *testing.T) {
	m, f := candidates(t)
	m = press(m, "f")
	m = press(m, "left")
	m = press(m, "b")
	if f.joined() != "follow,unfollow,block" || m.a.st.Entries["did:a"].Decision != store.Blocked {
		t.Errorf("calls=%s decision=%q", f.joined(), m.a.st.Entries["did:a"].Decision)
	}
}

func TestFollowIsNoOpWhenAlreadyFollowing(t *testing.T) {
	m, f := candidates(t)
	m.list[0].Viewer.Following = "at://did:me/app.bsky.graph.follow/old"
	m = press(m, "f")
	if f.joined() != "" || !strings.Contains(m.status, "already follow") {
		t.Errorf("calls=%s status=%q", f.joined(), m.status)
	}
	if len(m.a.st.Entries) != 0 || m.idx != 0 {
		t.Error("a no-op must not record or advance")
	}
}

func TestFollowFromTimeline(t *testing.T) {
	m, f := followingModel(t)
	m = press(m, "f") // friend: already followed
	if f.joined() != "" {
		t.Fatalf("calls = %s", f.joined())
	}
	m = press(m, "down")
	m = press(m, "f") // rando: a stranger who reached you through a repost
	if f.joined() != "follow" || m.a.st.Entries["did:rando"].Decision != store.Followed {
		t.Errorf("calls=%s entries=%v", f.joined(), m.a.st.Entries)
	}
	if m.cur != 1 {
		t.Errorf("timeline cursor should stay put, cur=%d", m.cur)
	}
	evs, _ := store.ReadAudit(m.a.audit.Path())
	if len(evs) != 1 || evs[0].Action != "follow" || evs[0].Source != "tui:following" || evs[0].Via != "friend.bsky.social" {
		t.Errorf("audit = %+v", evs)
	}
	if v := m.View(); !strings.Contains(v, "followed") || !strings.Contains(v, "f follow") {
		t.Errorf("list should tag the followed author and hint the key:\n%s", v)
	}
}

func TestCLIFollowAndUnfollow(t *testing.T) {
	a, f := newTestApp(t)
	var out bytes.Buffer
	a.stdout = &out

	if err := a.act(t.Context(), "follow", "newbie", true); err != nil {
		t.Fatal(err)
	}
	if f.joined() != "" || len(a.st.Entries) != 0 || !strings.Contains(out.String(), "[dry-run] would follow") {
		t.Fatalf("dry run changed something: calls=%s entries=%v out=%q", f.joined(), a.st.Entries, out.String())
	}
	if err := a.act(t.Context(), "follow", "newbie", false); err != nil {
		t.Fatal(err)
	}
	if f.joined() != "follow" || a.st.Entries["did:newbie"].Decision != store.Followed {
		t.Errorf("calls=%s entries=%v", f.joined(), a.st.Entries)
	}
	if err := a.act(t.Context(), "follow", "friend", false); err != nil { // already following
		t.Fatal(err)
	}
	if f.joined() != "follow" {
		t.Errorf("following someone you already follow must not call the API: %s", f.joined())
	}
	if err := a.act(t.Context(), "unfollow", "friend", false); err != nil {
		t.Fatal(err)
	}
	if f.joined() != "follow,unfollow" {
		t.Errorf("calls = %s", f.joined())
	}
	evs, _ := store.ReadAudit(a.audit.Path())
	var got []string
	for _, e := range evs {
		got = append(got, e.Action)
	}
	if strings.Join(got, ",") != "follow,follow,follow,unfollow" {
		t.Errorf("audit actions = %v", got)
	}
	if !evs[0].DryRun || evs[1].DryRun || evs[2].Detail != "already following" || evs[3].Record == "" {
		t.Errorf("audit details = %+v", evs)
	}
}
