package tui

import (
	"strings"
	"testing"

	"github.com/mdfranz/bskyban/internal/store"
)

func TestUnfollowRequiresConfirmationAndTargetsSelectedAuthor(t *testing.T) {
	m, f := followingModel(t)
	m = press(m, "u")
	if f.Joined() != "" || !strings.Contains(m.status, "Press u again") {
		t.Fatalf("first press should only ask: calls=%s status=%q", f.Joined(), m.status)
	}
	m = press(m, "down")
	m = press(m, "u")
	if f.Joined() != "" {
		t.Fatal("changing selection must cancel confirmation")
	}
	m = press(m, "up")
	m = press(press(m, "u"), "u")
	if f.Joined() != "unfollow" || m.items[0].Author.Viewer.Following != "" {
		t.Fatalf("unfollow failed: calls=%s following=%q", f.Joined(), m.items[0].Author.Viewer.Following)
	}
	events, _ := store.ReadAudit(m.a.Audit.Path())
	if len(events) != 1 || events[0].Action != "unfollow" || events[0].DID != "did:friend" {
		t.Errorf("audit = %+v", events)
	}
}

func TestFollowKeyFollowsAndUndoUnfollows(t *testing.T) {
	m, f := candidates(t)
	m = press(m, "f")
	if f.Joined() != "follow" {
		t.Fatalf("calls = %s, want follow", f.Joined())
	}
	if got := m.a.State.Entries["did:a"].Decision; got != store.Followed {
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
	m = press(m, "U")
	if f.Joined() != "follow,unfollow" {
		t.Errorf("calls = %s, want follow,unfollow", f.Joined())
	}
	if _, ok := m.a.State.Entries["did:a"]; ok || m.idx != 0 {
		t.Errorf("undo should clear the decision and step back: entries=%v idx=%d", m.a.State.Entries, m.idx)
	}
}

func TestOverridingAFollowUnfollowsFirst(t *testing.T) {
	m, f := candidates(t)
	m = press(m, "f")
	m = press(m, "left")
	m = press(m, "k") // keep means "no relationship": the follow is removed
	if f.Joined() != "follow,unfollow" || m.a.State.Entries["did:a"].Decision != store.Kept {
		t.Fatalf("calls=%s decision=%q", f.Joined(), m.a.State.Entries["did:a"].Decision)
	}
	m = press(m, "U") // undoing the keep restores the follow
	if f.Joined() != "follow,unfollow,follow" || m.a.State.Entries["did:a"].Decision != store.Followed {
		t.Errorf("after undo: calls=%s decision=%q", f.Joined(), m.a.State.Entries["did:a"].Decision)
	}
	m = press(m, "U") // and undoing the original follow removes the *new* record
	if f.Joined() != "follow,unfollow,follow,unfollow" {
		t.Errorf("calls = %s", f.Joined())
	}
	if _, ok := m.a.State.Entries["did:a"]; ok {
		t.Error("everything undone: no decision should remain")
	}
	evs, _ := store.ReadAudit(m.a.Audit.Path())
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
	if f.Joined() != "block,unblock,follow" || m.a.State.Entries["did:a"].Decision != store.Followed {
		t.Fatalf("calls=%s decision=%q", f.Joined(), m.a.State.Entries["did:a"].Decision)
	}
	m = press(m, "U") // back to blocked
	if f.Joined() != "block,unblock,follow,unfollow,block" || m.a.State.Entries["did:a"].Decision != store.Blocked {
		t.Errorf("after undo: calls=%s decision=%q", f.Joined(), m.a.State.Entries["did:a"].Decision)
	}
}

func TestBlockingAFollowedAccountUnfollowsIt(t *testing.T) {
	m, f := candidates(t)
	m = press(m, "f")
	m = press(m, "left")
	m = press(m, "b")
	if f.Joined() != "follow,unfollow,block" || m.a.State.Entries["did:a"].Decision != store.Blocked {
		t.Errorf("calls=%s decision=%q", f.Joined(), m.a.State.Entries["did:a"].Decision)
	}
}

func TestFollowIsNoOpWhenAlreadyFollowing(t *testing.T) {
	m, f := candidates(t)
	m.list[0].Viewer.Following = "at://did:me/app.bsky.graph.follow/old"
	m = press(m, "f")
	if f.Joined() != "" || !strings.Contains(m.status, "already follow") {
		t.Errorf("calls=%s status=%q", f.Joined(), m.status)
	}
	if len(m.a.State.Entries) != 0 || m.idx != 0 {
		t.Error("a no-op must not record or advance")
	}
}

func TestFollowFromTimeline(t *testing.T) {
	m, f := followingModel(t)
	m = press(m, "f") // friend: already followed
	if f.Joined() != "" {
		t.Fatalf("calls = %s", f.Joined())
	}
	m = press(m, "down")
	m = press(m, "f") // rando: a stranger who reached you through a repost
	if f.Joined() != "follow" || m.a.State.Entries["did:rando"].Decision != store.Followed {
		t.Errorf("calls=%s entries=%v", f.Joined(), m.a.State.Entries)
	}
	if m.cur != 1 {
		t.Errorf("timeline cursor should stay put, cur=%d", m.cur)
	}
	evs, _ := store.ReadAudit(m.a.Audit.Path())
	if len(evs) != 1 || evs[0].Action != "follow" || evs[0].Source != "tui:following" || evs[0].Via != "friend.bsky.social" {
		t.Errorf("audit = %+v", evs)
	}
	if v := m.View(); !strings.Contains(v, "followed") || !strings.Contains(v, "f follow") {
		t.Errorf("list should tag the followed author and hint the key:\n%s", v)
	}
}
