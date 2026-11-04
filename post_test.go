package main

import (
	"context"
	"testing"

	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
)

func TestPostURL(t *testing.T) {
	if got := postURL("at://did:plc:x/app.bsky.feed.post/3k"); got != "https://bsky.app/profile/did:plc:x/post/3k" {
		t.Errorf("got %q", got)
	}
}

func TestPostURI(t *testing.T) {
	a := &cliApp{App: &app.App{Client: &bsky.Client{DID: "did:plc:me"}}}
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
