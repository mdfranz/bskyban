package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mdfranz/bskyban/internal/bsky"
)

func actions(t *testing.T, a *app) []string {
	t.Helper()
	evs, err := ReadAudit(a.audit.path)
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

func TestAuditFileIsAppendOnlyAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "audit.jsonl")
	l := NewAudit(path)
	for _, a := range []string{"block", "keep"} {
		if err := l.Log(AuditEvent{Action: a, Source: "cli", OK: true}); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
	}
	// A fresh logger on the same file appends rather than truncating.
	if err := NewAudit(path).Log(AuditEvent{Action: "unblock", OK: true}); err != nil {
		t.Fatal(err)
	}
	evs, err := ReadAudit(path)
	if err != nil || len(evs) != 3 || evs[0].Action != "block" || evs[2].Action != "unblock" {
		t.Fatalf("events = %+v err=%v", evs, err)
	}
	if evs[0].TS == "" {
		t.Error("timestamp not set")
	}
	var nilLog *Audit
	if err := nilLog.Log(AuditEvent{}); err != nil {
		t.Errorf("nil audit should discard, got %v", err)
	}
}

func TestStateDirUsesXDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	if p, _ := defaultStatePath(); p != "/tmp/xdg-state/bskyban/state.json" {
		t.Errorf("state = %s", p)
	}
	if p, _ := defaultAuditPath(); p != "/tmp/xdg-state/bskyban/audit.jsonl" {
		t.Errorf("audit = %s", p)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/someone")
	if p, _ := defaultAuditPath(); p != "/home/someone/.local/state/bskyban/audit.jsonl" {
		t.Errorf("fallback = %s", p)
	}
	if strings.Contains(os.Getenv("HOME"), ".config") {
		t.Error("nothing should live under .config")
	}
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
	evs, _ := ReadAudit(m.a.audit.path)
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
	evs, _ := ReadAudit(m.a.audit.path)
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
	if evs, _ := ReadAudit(m2.a.audit.path); len(evs) != 0 {
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
	evs, _ := ReadAudit(m.a.audit.path)
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
	evs, _ := ReadAudit(a.audit.path)
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
	l := NewAudit(path)
	old := time.Now().Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	_ = l.Log(AuditEvent{TS: old, Action: "block", Handle: "old.bsky.social", Source: "cli", OK: true})
	_ = l.Log(AuditEvent{Action: "block", Handle: "spam.bsky.social", Source: "tui:reposts", Via: "alice.bsky.social", OK: true})
	_ = l.Log(AuditEvent{Action: "keep", Handle: "nice.bsky.social", Source: "cli", OK: true})
	_ = l.Log(AuditEvent{Action: "block", Handle: "fail.bsky.social", Source: "cli", Error: "boom"})

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

func TestSnapshotKeepsRealZeroCounts(t *testing.T) {
	s := snapshotOf(bsky.Profile{PostsCount: 0, FollowersCount: 9, FollowsCount: 664}, true)
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"posts":0`) || !strings.Contains(string(b), `"following":664`) {
		t.Errorf("snapshot json = %s", b)
	}
}
