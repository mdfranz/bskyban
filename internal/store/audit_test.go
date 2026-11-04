package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdfranz/bskyban/internal/bsky"
)

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
	if p, _ := DefaultStatePath(); p != "/tmp/xdg-state/bskyban/state.json" {
		t.Errorf("state = %s", p)
	}
	if p, _ := DefaultAuditPath(); p != "/tmp/xdg-state/bskyban/audit.jsonl" {
		t.Errorf("audit = %s", p)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/someone")
	if p, _ := DefaultAuditPath(); p != "/home/someone/.local/state/bskyban/audit.jsonl" {
		t.Errorf("fallback = %s", p)
	}
	if strings.Contains(os.Getenv("HOME"), ".config") {
		t.Error("nothing should live under .config")
	}
}

func TestSnapshotKeepsRealZeroCounts(t *testing.T) {
	s := SnapshotOf(bsky.Profile{PostsCount: 0, FollowersCount: 9, FollowsCount: 664}, true)
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"posts":0`) || !strings.Contains(string(b), `"following":664`) {
		t.Errorf("snapshot json = %s", b)
	}
}
