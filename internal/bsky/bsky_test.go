package bsky

import (
	"encoding/json"
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

func TestExpandLinks(t *testing.T) {
	text := "See www.news.com/ted-cruz-say... now"
	f := Facet{}
	f.Index.ByteStart, f.Index.ByteEnd = 4, 32
	f.Features = []struct {
		Type string `json:"$type"`
		URI  string `json:"uri"`
	}{{Type: "app.bsky.richtext.facet#link", URI: "https://www.news.com/ted-cruz-says-all"}}
	got := expandLinks(text, []Facet{f})
	if got != "See https://www.news.com/ted-cruz-says-all now" {
		t.Errorf("got %q", got)
	}
}

func TestFeedEntryStats(t *testing.T) {
	var e feedEntry
	raw := `{"post":{"uri":"at://did:friend/app.bsky.feed.post/3k","cid":"c","likeCount":120,"repostCount":1,"replyCount":18,"quoteCount":0,
		"author":{"did":"did:friend","handle":"friend.bsky.social"},"record":{"text":"hi"}}}`
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatal(err)
	}
	it := e.item()
	if it.Stats != (PostStats{Likes: 120, Reposts: 1, Replies: 18}) {
		t.Fatalf("stats = %+v", it.Stats)
	}
}

func TestLinkCardEmbed(t *testing.T) {
	uri := "https://www.theatlantic.com/politics/archive/2026/11/x/1"
	raw := json.RawMessage(`{"$type":"app.bsky.embed.external#view","external":{"uri":"` + uri + `","title":"The Limits of  Trump's Trickery"}}`)
	c := embedLink(raw)
	if c == nil || c.Title != "The Limits of Trump's Trickery" {
		t.Fatalf("card = %+v", c)
	}
	if got := describeEmbed(raw); len(got) != 0 {
		t.Errorf("a link card is no longer an attachment string: %v", got)
	}
}

func TestMediaMarkers(t *testing.T) {
	for raw, want := range map[string]string{
		`{"$type":"app.bsky.embed.images","images":[{"alt":"a  cat"}]}`:            "▣ a cat",
		`{"$type":"app.bsky.embed.images","images":[{"alt":""},{"alt":"map"},{}]}`: "▣×3 map",
		`{"$type":"app.bsky.embed.images","images":[{}]}`:                          "▣",
		`{"$type":"app.bsky.embed.video","alt":"clip"}`:                            "▶ video clip",
	} {
		if got := describeEmbed(json.RawMessage(raw)); len(got) != 1 || got[0] != want {
			t.Errorf("%s: got %q, want %q", raw, got, want)
		}
	}
}
