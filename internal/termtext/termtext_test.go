package termtext

import (
	"strings"
	"testing"
	"time"
)

func TestSanitize(t *testing.T) {
	cases := []struct{ input, want string }{
		{"before\x1b]52;c;YWJj\x07after", "beforeafter"},
		{"before\x1b]52;c;YWJj\x1b\\after", "beforeafter"},
		{"before\x1b[2Jafter", "beforeafter"},
		{"before\x1b[31mred\x1b[0mafter", "beforeredafter"},
		{"line one\nline two\t✓", "line one\nline two\t✓"},
		{"a\x00\x07\x08\r\u009bb", "ab"},
	}
	for _, tc := range cases {
		if got := Sanitize(tc.input); got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	payload := "hello\x1b]52;c;YWJj\x07 https://example.com"
	wrapped := strings.Join(Wrap(payload, 80), "\n")
	if strings.Contains(wrapped, "52;") || !strings.Contains(wrapped, OSC8("https://example.com", "https://example.com")) {
		t.Fatalf("unsafe content survived or app hyperlink was lost: %q", wrapped)
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
	lines := Wrap("read "+long+" now", 30)
	if len(lines) < 3 {
		t.Fatalf("expected the URL to wrap over several lines, got %q", lines)
	}
	var plain strings.Builder
	for _, l := range lines {
		if w := VisibleWidth(l); w > 30 {
			t.Errorf("line %q is %d wide (>30)", l, w)
		}
		if strings.Contains(l, "http") || strings.Contains(l, "will-die") {
			if !strings.Contains(l, "\x1b]8;;"+long+"\x1b\\") {
				t.Errorf("fragment not linked to full URL: %q", l)
			}
		}
		plain.WriteString(StripLinks(l))
	}
	if !strings.Contains(plain.String(), long) {
		t.Errorf("URL was altered or truncated: %q", plain.String())
	}
}

func TestMentionLinksToProfile(t *testing.T) {
	lines := Wrap("thanks @piper.bsky.social: great", 60)
	want := OSC8("https://bsky.app/profile/piper.bsky.social", "@piper.bsky.social")
	if len(lines) != 1 || !strings.Contains(lines[0], want) {
		t.Errorf("mention not linked to the profile: %q", lines)
	}
	if got := StripLinks(lines[0]); got != "thanks @piper.bsky.social: great" {
		t.Errorf("visible text changed: %q", got)
	}
}

func TestFindURLsTrimsPunctuation(t *testing.T) {
	got := FindURLs("see https://a.com/x. and (https://b.org/y), ok")
	if len(got) != 2 || got[0] != "https://a.com/x" || got[1] != "https://b.org/y" {
		t.Errorf("got %q", got)
	}
}

func TestMarkersAreNotStrandedAtLineEnd(t *testing.T) {
	for _, text := range []string{"aaaa bbbb ▣×3 county map", "aaaa bbbb ▶ video clip"} {
		for _, l := range Wrap(text, 10) {
			if strings.HasSuffix(l, "▣×3") || strings.HasSuffix(l, "▶") {
				t.Errorf("marker stranded at a line end: %q", Wrap(text, 10))
			}
		}
	}
}

func TestOneLine(t *testing.T) {
	if got := OneLine("  a\n\tb   c \r\n"); got != "a b c" {
		t.Errorf("OneLine = %q", got)
	}
}

func TestHumanAgeISO(t *testing.T) {
	if got := HumanAgeISO("not a time"); got != "?" {
		t.Errorf("bad timestamp = %q, want ?", got)
	}
	if got := HumanAgeISO(time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)); got != "3h" {
		t.Errorf("3 hours ago = %q", got)
	}
}
