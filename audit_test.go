package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
)

func actions(t *testing.T, a *app) []string {
	t.Helper()
	evs, err := store.ReadAudit(a.audit.Path())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		s := e.Action
		if !e.OK {
			s += "!"
		}
		out = append(out, s)
	}
	return out
}

func TestTUIActionsAreAudited(t *testing.T) {
	m, _ := candidates(t)
	m.list[0].Description = "  hello   there  "
	m.ctxs = map[string]repostCtx{"did:a": {By: []string{"alice.bsky.social", "bob.bsky.social"}}}
	m = press(m, "b") // block a
	m = press(m, "left")
	m = press(m, "k") // override: lifts the block
	m = press(m, "u") // undo: block comes back
	want := []string{"block", "unblock", "keep", "undo"}
	got := actions(t, m.a)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	evs, _ := store.ReadAudit(m.a.audit.Path())
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
	evs, _ := store.ReadAudit(m.a.audit.Path())
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
	if evs, _ := store.ReadAudit(m2.a.audit.Path()); len(evs) != 0 {
		t.Errorf("navigation/confirmation was logged: %+v", evs)
	}
}

func TestFailedActionIsAuditedNotRecorded(t *testing.T) {
	m, _ := candidates(t)
	m.a.c = bsky.NewClient("http://127.0.0.1:1") // nothing listens here
	m.a.c.DID = "did:me"
	m = press(m, "b")
	if m.err == nil {
		t.Fatal("expected an error to be shown")
	}
	if _, ok := m.a.st.Entries["did:a"]; ok {
		t.Error("failed block must not be recorded as a decision")
	}
	evs, _ := store.ReadAudit(m.a.audit.Path())
	if len(evs) != 1 || evs[0].OK || evs[0].Error == "" || evs[0].Action != "block" {
		t.Errorf("events = %+v", evs)
	}
}

func TestCLIDryRunIsAuditedAndChangesNothing(t *testing.T) {
	a, f := newTestApp(t)
	var out bytes.Buffer
	a.stdout = &out
	// the fake server reports the profile as blocked
	if err := a.act(t.Context(), "unblock", "x", true); err != nil {
		t.Fatal(err)
	}
	if f.joined() != "" || len(a.st.Entries) != 0 {
		t.Errorf("dry run changed something: %s %v", f.joined(), a.st.Entries)
	}
	if err := a.act(t.Context(), "unblock", "x", false); err != nil {
		t.Fatal(err)
	}
	evs, _ := store.ReadAudit(a.audit.Path())
	if len(evs) != 2 || !evs[0].DryRun || evs[1].DryRun || evs[1].Source != "cli" || evs[1].Action != "unblock" {
		t.Fatalf("events = %+v", evs)
	}
	if evs[1].Record == "" || f.joined() != "unblock" {
		t.Errorf("record=%q calls=%s", evs[1].Record, f.joined())
	}
}

func TestLogCommandFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("BSKYBAN_AUDIT", path)
	l := store.NewAudit(path)
	old := time.Now().Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	_ = l.Log(store.AuditEvent{TS: old, Action: "block", Handle: "old.bsky.social", Source: "cli", OK: true})
	_ = l.Log(store.AuditEvent{Action: "block", Handle: "spam.bsky.social", Source: "tui:reposts", Via: "alice.bsky.social", OK: true})
	_ = l.Log(store.AuditEvent{Action: "keep", Handle: "nice.bsky.social", Source: "cli", OK: true})
	_ = l.Log(store.AuditEvent{Action: "block", Handle: "fail.bsky.social", Source: "cli", Error: "boom"})

	run := func(args ...string) string {
		var b bytes.Buffer
		if err := runLog(args, &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if got := run("--since", "7d"); strings.Contains(got, "old.bsky.social") || !strings.Contains(got, "spam.bsky.social") {
		t.Errorf("--since 7d:\n%s", got)
	}
	if got := run("--action", "keep"); !strings.Contains(got, "nice.bsky.social") || strings.Contains(got, "spam.bsky.social") {
		t.Errorf("--action keep:\n%s", got)
	}
	if got := run("--who", "@spam"); strings.Count(got, "\n") != 1 || !strings.Contains(got, "via @alice.bsky.social") {
		t.Errorf("--who:\n%s", got)
	}
	if got := run("-n", "1"); !strings.Contains(got, "FAILED: boom") || strings.Count(got, "\n") != 1 {
		t.Errorf("-n 1 should show only the latest:\n%s", got)
	}
	if got := run("--json", "--action", "keep"); !strings.HasPrefix(got, `{"ts":`) {
		t.Errorf("--json:\n%s", got)
	}
	if d, err := parseSince("36h"); err != nil || d != 36*time.Hour {
		t.Errorf("36h = %v %v", d, err)
	}
	if _, err := parseSince("soon"); err == nil {
		t.Error("bad --since accepted")
	}
}
