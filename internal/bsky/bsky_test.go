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

func TestLinkCardLabel(t *testing.T) {
	uri := "https://www.theatlantic.com/politics/archive/2026/11/x/1"
	c := LinkCard{URI: uri, Title: "The Limits of Trump's Trickery"}
	// the URL is always shown in full (terminals detect plain URLs), never twice
	for text, want := range map[string]string{
		"read this":                             "↗ The Limits of Trump's Trickery " + uri,
		"see " + uri:                            "↗ The Limits of Trump's Trickery",
		"the limits of trump's trickery":        "↗ " + uri,
		"the limits of trump's trickery " + uri: "",
	} {
		if got := c.Label(text); got != want {
			t.Errorf("Label(%q) = %q, want %q", text, got, want)
		}
	}
	if got := (LinkCard{URI: "https://bit.ly/3V270SA"}).Label(""); got != "↗ https://bit.ly/3V270SA" {
		t.Errorf("untitled card = %q", got)
	}
}

func TestPreviewKeepsURLsWhole(t *testing.T) {
	long := "https://www.washingtonpost.com/technology/2026/11/03/google-deepmind-staff-protest-lifetime-ban/"
	p := Post{Text: "A big night " + long, Link: &LinkCard{URI: "https://bit.ly/3V270SA", Title: "The Limits of Trump's Midterm Trickery"}}
	if got := p.Preview(); got != "A big night "+long+" ↗ The Limits of Trump's Midterm Trickery https://bit.ly/3V270SA" {
		t.Errorf("preview should keep URLs whole: %q", got)
	}
}

func TestPostURL(t *testing.T) {
	if got := PostURL("at://did:plc:x/app.bsky.feed.post/3k"); got != "https://bsky.app/profile/did:plc:x/post/3k" {
		t.Errorf("got %q", got)
	}
}
