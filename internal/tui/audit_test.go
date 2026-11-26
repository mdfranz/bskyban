package tui

import (
	"strings"
	"testing"

	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
	"github.com/mdfranz/bskyban/internal/testpds"
)

func TestTUIActionsAreAudited(t *testing.T) {
	m, _ := candidates(t)
	m.list[0].Description = "  hello   there  "
	m.ctxs = map[string]app.RepostCtx{"did:a": {By: []string{"alice.bsky.social", "bob.bsky.social"}}}
	m = press(m, "b") // block a
	m = press(m, "left")
	m = press(m, "k") // override: lifts the block
	m = press(m, "U") // undo: block comes back
	want := []string{"block", "unblock", "keep", "undo"}
	got := testpds.Actions(t, m.a)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	evs, _ := store.ReadAudit(m.a.Audit.Path())
	b := evs[0]
	if b.Source != "tui:review" || b.DID != "did:a" || b.Handle != "a.bsky.social" || !b.OK {
		t.Errorf("block event = %+v", b)
	}
	if b.Record == "" || !strings.Contains(b.Record, "app.bsky.graph.block") {
		t.Errorf("block record missing: %q", b.Record)
	}
	if b.Snapshot == nil || b.Snapshot.Bio != "hello there" {
		t.Errorf("snapshot = %+v", b.Snapshot)
	}
	if b.Snapshot.Posts != nil {
		t.Error("counts must be left out when only a summary profile is known")
	}
	if evs[1].Detail != "lifted by kept" || evs[2].Prev != "blocked" {
		t.Errorf("override events = %+v / %+v", evs[1], evs[2])
	}
	if evs[3].Detail != "undid kept" || evs[3].Prev != "kept" {
		t.Errorf("undo event = %+v", evs[3])
	}
}

func TestFeedActionsRecordSourceAndVia(t *testing.T) {
	m, _ := followingModel(t)
	m = press(m, "down")
	m = press(m, "b")
	evs, _ := store.ReadAudit(m.a.Audit.Path())
	if len(evs) != 1 {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Source != "tui:following" || evs[0].Via != "friend.bsky.social" || evs[0].Action != "block" {
		t.Errorf("event = %+v", evs[0])
	}
	// Cancelled confirmations and plain navigation write nothing.
	m2, _ := followingModel(t)
	m2 = press(m2, "b")
	m2 = press(m2, "down")
	if evs, _ := store.ReadAudit(m2.a.Audit.Path()); len(evs) != 0 {
		t.Errorf("navigation/confirmation was logged: %+v", evs)
	}
}

func TestFailedActionIsAuditedNotRecorded(t *testing.T) {
	m, _ := candidates(t)
	m.a.Client = bsky.NewClient("http://127.0.0.1:1") // nothing listens here
	m.a.Client.DID = "did:me"
	m = press(m, "b")
	if m.err == nil {
		t.Fatal("expected an error to be shown")
	}
	if _, ok := m.a.State.Entries["did:a"]; ok {
		t.Error("failed block must not be recorded as a decision")
	}
	evs, _ := store.ReadAudit(m.a.Audit.Path())
	if len(evs) != 1 || evs[0].OK || evs[0].Error == "" || evs[0].Action != "block" {
		t.Errorf("events = %+v", evs)
	}
}
