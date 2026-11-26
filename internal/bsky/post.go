package bsky

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mdfranz/bskyban/internal/termtext"
)

// Post is a recent post (or repost) in an account's feed.
type Post struct {
	Text      string
	CreatedAt string
	Repost    bool
	Reply     bool
	Attach    []string  // one-line descriptions of images, video, quotes
	Link      *LinkCard // link preview card, if the post has one

	// Only set for posts that come with hydrated context (timeline / author feeds).
	Quote        string   // "@handle: text" of a quoted post
	QuoteURI     string   // AT URI of the quoted post
	Images       []string // full-size image URLs
	ParentHandle string   // for replies: who is being replied to
	ParentText   string
}

type Facet struct {
	Index struct {
		ByteStart int `json:"byteStart"`
		ByteEnd   int `json:"byteEnd"`
	} `json:"index"`
	Features []struct {
		Type string `json:"$type"`
		URI  string `json:"uri"`
	} `json:"features"`
}

// expandLinks replaces the shortened link text clients store in a post
// ("www.example.com/some-pa...") with the real URL from the link Facet.
func expandLinks(text string, facets []Facet) string {
	type rep struct {
		start, end int
		uri        string
	}
	var reps []rep
	for _, f := range facets {
		for _, ft := range f.Features {
			if strings.HasSuffix(ft.Type, "#link") && ft.URI != "" &&
				f.Index.ByteStart >= 0 && f.Index.ByteStart < f.Index.ByteEnd && f.Index.ByteEnd <= len(text) {
				reps = append(reps, rep{f.Index.ByteStart, f.Index.ByteEnd, ft.URI})
			}
		}
	}
	sort.Slice(reps, func(i, j int) bool { return reps[i].start > reps[j].start })
	for _, r := range reps { // back to front so earlier offsets stay valid
		text = text[:r.start] + r.uri + text[r.end:]
	}
	return text
}

// LinkCard is a post's link preview: where it goes and the page title.
type LinkCard struct{ URI, Title string }

// embedLink returns the post's link card, if its embed has one.
func embedLink(raw json.RawMessage) *LinkCard {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var e struct {
		Type     string `json:"$type"`
		External struct {
			URI, Title string
		} `json:"external"`
		Media json.RawMessage `json:"media"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	switch {
	case strings.HasPrefix(e.Type, "app.bsky.embed.external") && e.External.URI != "":
		return &LinkCard{URI: e.External.URI, Title: termtext.OneLine(e.External.Title)}
	case e.Type == "app.bsky.embed.recordWithMedia":
		return embedLink(e.Media)
	}
	return nil
}

// describeEmbed turns a raw post embed into short one-line descriptions.
func describeEmbed(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var e struct {
		Type     string                 `json:"$type"`
		Images   []struct{ Alt string } `json:"images"`
		Alt      string                 `json:"alt"`
		External struct {
			URI, Title string
		} `json:"external"`
		Record json.RawMessage `json:"record"`
		Media  json.RawMessage `json:"media"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	var out []string
	switch {
	case strings.HasPrefix(e.Type, "app.bsky.embed.images"):
		s := termtext.ImageMark // "▣", or "▣×3" for several, then the first alt text
		if len(e.Images) != 1 {
			s += fmt.Sprintf("×%d", len(e.Images))
		}
		for _, im := range e.Images {
			if alt := termtext.OneLine(im.Alt); alt != "" {
				s += " " + alt
				break
			}
		}
		out = append(out, s)
	case strings.HasPrefix(e.Type, "app.bsky.embed.video"):
		s := termtext.VideoMark + " video"
		if alt := termtext.OneLine(e.Alt); alt != "" {
			s += " " + alt
		}
		out = append(out, s)
	case e.Type == "app.bsky.embed.recordWithMedia":
		out = append(out, describeEmbed(e.Media)...)
		out = append(out, "[quote post]")
	case strings.HasPrefix(e.Type, "app.bsky.embed.record"):
		out = append(out, "[quote post]")
	}
	return out
}

// Preview is the text shown for a post in the list: its text, then any
// attachments and quoted post, so image-only posts still say something.
// Preview is the post's own text, attachments and link card as one paragraph (no
// stray line breaks). URLs stay whole so the terminal can detect and open them.
// A quoted post is left out; itemLines shows it on lines of its own.
func (p Post) Preview() string {
	parts := append([]string{p.Text}, p.Attach...)
	if p.Link != nil {
		parts = append(parts, p.Link.Label(p.Text))
	}
	return termtext.OneLine(strings.Join(parts, " "))
}

// title is the page title to show, unless it is missing or the post's text
// already says it.
func (c LinkCard) title(text string) string {
	if c.Title == "" || strings.Contains(strings.ToLower(text), strings.ToLower(c.Title)) {
		return ""
	}
	return c.Title
}

// Label is the card in one line: "↗ Title https://…". The URL is left out when the
// post's text already has it, and the title when the text already says it.
func (c LinkCard) Label(text string) string {
	parts := []string{"↗"}
	if t := c.title(text); t != "" {
		parts = append(parts, t)
	}
	if !strings.Contains(text, c.URI) {
		parts = append(parts, c.URI)
	}
	if len(parts) == 1 { // the text has both: nothing left to add
		return ""
	}
	return strings.Join(parts, " ")
}
