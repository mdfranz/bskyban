package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
	"github.com/mdfranz/bskyban/internal/testpds"
)

func TestCLIDryRunIsAuditedAndChangesNothing(t *testing.T) {
	base, f := testpds.New(t)
	var out bytes.Buffer
	a := &CLI{App: base, stdout: &out}
	// the fake server reports the profile as blocked
	if err := a.act(t.Context(), "unblock", "x", true); err != nil {
		t.Fatal(err)
	}
	if f.Joined() != "" || len(a.State.Entries) != 0 {
		t.Errorf("dry run changed something: %s %v", f.Joined(), a.State.Entries)
	}
	if err := a.act(t.Context(), "unblock", "x", false); err != nil {
		t.Fatal(err)
	}
	evs, _ := store.ReadAudit(a.Audit.Path())
	if len(evs) != 2 || !evs[0].DryRun || evs[1].DryRun || evs[1].Source != "cli" || evs[1].Action != "unblock" {
		t.Fatalf("events = %+v", evs)
	}
	if evs[1].Record == "" || f.Joined() != "unblock" {
		t.Errorf("record=%q calls=%s", evs[1].Record, f.Joined())
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
		if err := RunLog(args, &b); err != nil {
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

func TestCLIFollowAndUnfollow(t *testing.T) {
	base, f := testpds.New(t)
	var out bytes.Buffer
	a := &CLI{App: base, stdout: &out}

	if err := a.act(t.Context(), "follow", "newbie", true); err != nil {
		t.Fatal(err)
	}
	if f.Joined() != "" || len(a.State.Entries) != 0 || !strings.Contains(out.String(), "[dry-run] would follow") {
		t.Fatalf("dry run changed something: calls=%s entries=%v out=%q", f.Joined(), a.State.Entries, out.String())
	}
	if err := a.act(t.Context(), "follow", "newbie", false); err != nil {
		t.Fatal(err)
	}
	if f.Joined() != "follow" || a.State.Entries["did:newbie"].Decision != store.Followed {
		t.Errorf("calls=%s entries=%v", f.Joined(), a.State.Entries)
	}
	if err := a.act(t.Context(), "follow", "friend", false); err != nil { // already following
		t.Fatal(err)
	}
	if f.Joined() != "follow" {
		t.Errorf("following someone you already follow must not call the API: %s", f.Joined())
	}
	if err := a.act(t.Context(), "unfollow", "friend", false); err != nil {
		t.Fatal(err)
	}
	if f.Joined() != "follow,unfollow" {
		t.Errorf("calls = %s", f.Joined())
	}
	evs, _ := store.ReadAudit(a.Audit.Path())
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

func TestUntrustedTerminalControlsAreRemoved(t *testing.T) {
	payload := "hello\x1b]52;c;YWJj\x07 https://example.com"
	var p bsky.Profile
	data, _ := json.Marshal(map[string]string{"displayName": payload, "description": payload})
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(p.DisplayName+p.Description, "\x1b\x07") {
		t.Fatalf("decoded profile retained controls: %+v", p)
	}
	if got := formatPost(bsky.Post{Text: payload, Attach: []string{payload}}, 0); strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("CLI post retained controls: %q", got)
	}
}

func TestPostURI(t *testing.T) {
	a := &CLI{App: &app.App{Client: &bsky.Client{DID: "did:plc:me"}}}
	for ref, want := range map[string]string{
		"3k":                                    "at://did:plc:me/app.bsky.feed.post/3k",
		"at://did:plc:me/app.bsky.feed.post/3k": "at://did:plc:me/app.bsky.feed.post/3k",
		"https://bsky.app/profile/did:plc:me/post/3k": "at://did:plc:me/app.bsky.feed.post/3k",
	} {
		got, err := a.postURI(context.Background(), ref)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v", ref, got, err)
		}
	}
	for _, ref := range []string{"at://did:plc:other/app.bsky.feed.post/3k", "at://did:plc:me/app.bsky.graph.block/3k", "https://example.com/x", "a/b"} {
		if _, err := a.postURI(context.Background(), ref); err == nil {
			t.Errorf("%s: expected error", ref)
		}
	}
}
