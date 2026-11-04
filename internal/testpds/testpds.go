// Package testpds is a fake Bluesky server for tests of code that talks to the API.
package testpds

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
)

// PDS is a fake Bluesky server that records the write calls made against it.
type PDS struct {
	mu      sync.Mutex
	calls   []string
	deleted []string
}

func (f *PDS) log(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }

// DeletedKeys are the record keys deleted so far, in order.
func (f *PDS) DeletedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// Joined is the write calls so far, in order, comma separated (block, unblock, follow, like, post, ...).
func (f *PDS) Joined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

// New starts a fake server and returns an App logged in to it as did:me, with
// temporary state and audit files.
func New(t *testing.T) (*app.App, *PDS) {
	t.Helper()
	f := &PDS{}
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xrpc/com.atproto.repo.createRecord":
			var body struct{ Collection string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			n++
			kind := "block"
			if strings.HasSuffix(body.Collection, ".follow") {
				kind = "follow"
			} else if strings.HasSuffix(body.Collection, ".post") {
				kind = "post"
			} else if strings.HasSuffix(body.Collection, ".like") {
				kind = "like"
			}
			f.log(kind)
			fmt.Fprintf(w, `{"uri":"at://did:me/%s/rk%d"}`, body.Collection, n)
		case "/xrpc/com.atproto.repo.deleteRecord":
			var body struct {
				Collection string `json:"collection"`
				RKey       string `json:"rkey"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.deleted = append(f.deleted, body.RKey)
			f.mu.Unlock()
			if strings.HasSuffix(body.Collection, ".follow") {
				f.log("unfollow")
			} else if strings.HasSuffix(body.Collection, ".post") {
				f.log("delete-post")
			} else if strings.HasSuffix(body.Collection, ".like") {
				f.log("unlike")
			} else {
				f.log("unblock")
			}
			fmt.Fprint(w, `{}`)
		case "/xrpc/app.bsky.actor.getProfile":
			switch r.URL.Query().Get("actor") {
			case "friend": // someone you follow
				fmt.Fprint(w, `{"did":"did:friend","handle":"friend","viewer":{"following":"at://did:me/app.bsky.graph.follow/rkf"}}`)
			case "newbie": // a stranger
				fmt.Fprint(w, `{"did":"did:newbie","handle":"newbie","viewer":{}}`)
			default: // "x": already blocked
				fmt.Fprint(w, `{"did":"x","handle":"x","viewer":{"blocking":"at://did:me/app.bsky.graph.block/rk0"}}`)
			}
		case "/xrpc/app.bsky.graph.getFollows":
			if r.URL.Query().Get("actor") != "did:friend" {
				http.Error(w, "wrong actor", http.StatusBadRequest)
			} else if r.URL.Query().Get("cursor") == "next" {
				fmt.Fprint(w, `{"follows":[{"did":"did:second","handle":"second.example","displayName":"Second"}]}`)
			} else {
				fmt.Fprint(w, `{"follows":[{"did":"did:first","handle":"first.example","displayName":"First"}],"cursor":"next"}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := bsky.NewClient(srv.URL)
	c.DID = "did:me"
	st, err := store.LoadState(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	return &app.App{Client: c, State: st, Audit: store.NewAudit(t.TempDir() + "/audit.jsonl")}, f
}

// Actions lists the actions in a's audit log in order, with a "!" after any that failed.
func Actions(t *testing.T, a *app.App) []string {
	t.Helper()
	evs, err := store.ReadAudit(a.Audit.Path())
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
