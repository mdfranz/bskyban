package main

import (
	"context"
	"testing"
)

func TestLinkFacets(t *testing.T) {
	text := "héllo https://example.com/a, and more"
	f := linkFacets(text)
	if len(f) != 1 {
		t.Fatalf("got %d facets", len(f))
	}
	idx := f[0]["index"].(map[string]int)
	if got := text[idx["byteStart"]:idx["byteEnd"]]; got != "https://example.com/a" {
		t.Errorf("link = %q", got)
	}
	if len(linkFacets("no links")) != 0 {
		t.Error("unexpected facet")
	}
}

func TestPostURL(t *testing.T) {
	if got := postURL("at://did:plc:x/app.bsky.feed.post/3k"); got != "https://bsky.app/profile/did:plc:x/post/3k" {
		t.Errorf("got %q", got)
	}
}

func TestPostURI(t *testing.T) {
	a := &app{c: &Client{DID: "did:plc:me"}}
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
