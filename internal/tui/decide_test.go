package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
)

func TestOverrideBlockedLookupAndUnblock(t *testing.T) {
	t.Run("failed lookup preserves state and audits failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"UpstreamFailure","message":"boom"}`, http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		m, _ := candidates(t)
		m.a.Client = bsky.NewClient(srv.URL)
		m.a.Client.DID = "did:me"
		if err := m.a.State.Set("did:a", "a.bsky.social", store.Blocked); err != nil {
			t.Fatal(err)
		}

		m = press(m, "k")
		if m.err == nil {
			t.Fatal("expected lookup error")
		}
		if got := m.a.State.Entries["did:a"].Decision; got != store.Blocked {
			t.Fatalf("decision = %q, want %q", got, store.Blocked)
		}
		if m.idx != 0 {
			t.Fatalf("idx = %d, want 0", m.idx)
		}
		evs, err := store.ReadAudit(m.a.Audit.Path())
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 || evs[0].OK || evs[0].Error == "" {
			t.Fatalf("events = %+v, want 1 failed audit event", evs)
		}
	})

	t.Run("failed delete preserves state and audits failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/xrpc/app.bsky.actor.getProfile":
				fmt.Fprint(w, `{"did":"did:a","handle":"a.bsky.social","viewer":{"blocking":"at://did:me/app.bsky.graph.block/rk0"}}`)
			case "/xrpc/com.atproto.repo.deleteRecord":
				http.Error(w, `{"error":"DeleteFailed","message":"cannot delete"}`, http.StatusInternalServerError)
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)

		m, _ := candidates(t)
		m.a.Client = bsky.NewClient(srv.URL)
		m.a.Client.DID = "did:me"
		if err := m.a.State.Set("did:a", "a.bsky.social", store.Blocked); err != nil {
			t.Fatal(err)
		}

		m = press(m, "s")
		if m.err == nil {
			t.Fatal("expected delete error")
		}
		if got := m.a.State.Entries["did:a"].Decision; got != store.Blocked {
			t.Fatalf("decision = %q, want %q", got, store.Blocked)
		}
		if m.idx != 0 {
			t.Fatalf("idx = %d, want 0", m.idx)
		}
		evs, err := store.ReadAudit(m.a.Audit.Path())
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 || evs[0].OK || evs[0].Error == "" {
			t.Fatalf("events = %+v, want 1 failed audit event", evs)
		}
	})

	t.Run("authoritative lookup with no existing block records override", func(t *testing.T) {
		m, f := candidates(t)
		m.list[0] = bsky.Profile{DID: "newbie", Handle: "newbie"}
		if err := m.a.State.Set("newbie", "newbie", store.Blocked); err != nil {
			t.Fatal(err)
		}

		m = press(m, "k")
		if m.err != nil {
			t.Fatalf("unexpected err: %v", m.err)
		}
		if f.Joined() != "" {
			t.Fatalf("calls = %q, want none", f.Joined())
		}
		if got := m.a.State.Entries["newbie"].Decision; got != store.Kept {
			t.Fatalf("decision = %q, want %q", got, store.Kept)
		}
		if m.idx != 1 {
			t.Fatalf("idx = %d, want 1", m.idx)
		}
	})

	t.Run("successful lookup unblocks and records override", func(t *testing.T) {
		m, f := candidates(t)
		m.list[0] = bsky.Profile{DID: "x", Handle: "x"}
		if err := m.a.State.Set("x", "x", store.Blocked); err != nil {
			t.Fatal(err)
		}

		m = press(m, "k")
		if m.err != nil {
			t.Fatalf("unexpected err: %v", m.err)
		}
		if f.Joined() != "unblock" {
			t.Fatalf("calls = %q, want unblock", f.Joined())
		}
		if got := m.a.State.Entries["x"].Decision; got != store.Kept {
			t.Fatalf("decision = %q, want %q", got, store.Kept)
		}
		if m.idx != 1 {
			t.Fatalf("idx = %d, want 1", m.idx)
		}
	})
}
