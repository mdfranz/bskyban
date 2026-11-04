package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/mdfranz/bskyban/internal/bsky"
)

func entry(author, authorHandle, by, following, blocking string) string {
	v := `"viewer":{}`
	if following != "" {
		v = `"viewer":{"following":"at://x/follow/1"}`
	}
	if blocking != "" {
		v = `"viewer":{"blocking":"at://x/block/1"}`
	}
	return fmt.Sprintf(`{"post":{"uri":"at://%s/p/1","author":{"did":%q,"handle":%q,%s},"record":{"text":"hi from %s","createdAt":"2026-11-01T00:00:00Z"}},"reason":{"$type":"app.bsky.feed.defs#reasonRepost","by":{"did":"did:by","handle":%q}}}`,
		author, author, authorHandle, v, authorHandle, by)
}

func TestFeedCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/xrpc/app.bsky.feed.getTimeline" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"feed":[`+
			entry("did:new", "new.bsky.social", "alice.bsky.social", "", "")+","+
			entry("did:followed", "followed.bsky.social", "alice.bsky.social", "yes", "")+","+
			entry("did:blocked", "blocked.bsky.social", "alice.bsky.social", "", "yes")+","+
			entry("did:decided", "decided.bsky.social", "alice.bsky.social", "", "")+","+
			entry("did:new", "new.bsky.social", "bob.bsky.social", "", "")+
			`,{"post":{"uri":"at://o","author":{"did":"did:orig","handle":"orig"},"record":{"text":"plain"}}}]}`)
	}))
	defer srv.Close()

	st := &State{path: t.TempDir() + "/state.json", Entries: map[string]Entry{"did:decided": {Decision: decKept}}}
	a := &app{c: bsky.NewClient(srv.URL), st: st}
	a.c.DID = "did:me"

	list, rc, err := a.feedCandidates(t.Context(), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].DID != "did:new" {
		t.Fatalf("candidates = %+v, want only did:new", list)
	}
	if got := rc["did:new"].By; !slices.Equal(got, []string{"alice.bsky.social", "bob.bsky.social"}) {
		t.Errorf("By = %v", got)
	}
	if rc["did:new"].Post.Repost {
		t.Error("original post should not be tagged as a repost")
	}
}
