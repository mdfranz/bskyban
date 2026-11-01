package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUntrustedTerminalControlsAreRemoved(t *testing.T) {
	cases := []struct{ input, want string }{
		{"before\x1b]52;c;YWJj\x07after", "beforeafter"},
		{"before\x1b]52;c;YWJj\x1b\\after", "beforeafter"},
		{"before\x1b[2Jafter", "beforeafter"},
		{"before\x1b[31mred\x1b[0mafter", "beforeredafter"},
		{"line one\nline two\t✓", "line one\nline two\t✓"},
		{"a\x00\x07\x08\r\u009bb", "ab"},
	}
	for _, tc := range cases {
		if got := terminalText(tc.input); got != tc.want {
			t.Errorf("terminalText(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	payload := "hello\x1b]52;c;YWJj\x07 https://example.com"
	wrapped := strings.Join(wrapText(payload, 80), "\n")
	if strings.Contains(wrapped, "52;") || !strings.Contains(wrapped, osc8("https://example.com", "https://example.com")) {
		t.Fatalf("unsafe content survived or app hyperlink was lost: %q", wrapped)
	}
	var p Profile
	data, _ := json.Marshal(map[string]string{"displayName": payload, "description": payload})
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(p.DisplayName+p.Description, "\x1b\x07") {
		t.Fatalf("decoded profile retained controls: %+v", p)
	}
	if got := formatPost(Post{Text: payload, Attach: []string{payload}}, 0); strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("CLI post retained controls: %q", got)
	}
}

func TestLinkify(t *testing.T) {
	cases := map[string]string{
		"see https://a.com/x.":   "see \x1b]8;;https://a.com/x\x1b\\https://a.com/x\x1b]8;;\x1b\\.",
		"Free: junoview.com #Py": "Free: \x1b]8;;https://junoview.com\x1b\\junoview.com\x1b]8;;\x1b\\ #Py",
		"no links here. Python.": "no links here. Python.",
	}
	for in, want := range cases {
		if got := linkify(in); got != want {
			t.Errorf("linkify(%q) = %q, want %q", in, got, want)
		}
	}
	// styling escapes must survive untouched around a bare domain
	in := "\x1b[1mFacesFeed.com\x1b[0m"
	want := "\x1b[1m\x1b]8;;https://FacesFeed.com\x1b\\FacesFeed.com\x1b]8;;\x1b\\\x1b[0m"
	if got := linkify(in); got != want {
		t.Errorf("styled linkify = %q, want %q", got, want)
	}
}

func TestWrapKeepsFullURL(t *testing.T) {
	long := "https://www.newsweek.com/ted-cruz-says-americans-will-die-if-abdul-el-sayed-joins-senate-intel-committee"
	lines := wrapText("read "+long+" now", 30)
	if len(lines) < 3 {
		t.Fatalf("expected the URL to wrap over several lines, got %q", lines)
	}
	var plain strings.Builder
	for _, l := range lines {
		if w := visibleWidth(l); w > 30 {
			t.Errorf("line %q is %d wide (>30)", l, w)
		}
		if strings.Contains(l, "http") || strings.Contains(l, "will-die") {
			if !strings.Contains(l, "\x1b]8;;"+long+"\x1b\\") {
				t.Errorf("fragment not linked to full URL: %q", l)
			}
		}
		plain.WriteString(oscRe.ReplaceAllString(l, ""))
	}
	if !strings.Contains(plain.String(), long) {
		t.Errorf("URL was altered or truncated: %q", plain.String())
	}
}

func TestExpandLinks(t *testing.T) {
	text := "See www.news.com/ted-cruz-say... now"
	f := facet{}
	f.Index.ByteStart, f.Index.ByteEnd = 4, 32
	f.Features = []struct {
		Type string `json:"$type"`
		URI  string `json:"uri"`
	}{{Type: "app.bsky.richtext.facet#link", URI: "https://www.news.com/ted-cruz-says-all"}}
	got := expandLinks(text, []facet{f})
	if got != "See https://www.news.com/ted-cruz-says-all now" {
		t.Errorf("got %q", got)
	}
}

func TestMentionLinksToProfile(t *testing.T) {
	lines := wrapText("thanks @piper.bsky.social: great", 60)
	want := osc8("https://bsky.app/profile/piper.bsky.social", "@piper.bsky.social")
	if len(lines) != 1 || !strings.Contains(lines[0], want) {
		t.Errorf("mention not linked to the profile: %q", lines)
	}
	if got := oscRe.ReplaceAllString(lines[0], ""); got != "thanks @piper.bsky.social: great" {
		t.Errorf("visible text changed: %q", got)
	}
}
