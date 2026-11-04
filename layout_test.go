package main

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestRepliesInTheSameThreadAreJoined(t *testing.T) {
	m, _ := followingModel(t)
	root := FeedItem{URI: "at://did:cat/app.bsky.feed.post/r", Author: Profile{DID: "did:cat", Handle: "cat.bsky.social"},
		By: "egg.bsky.social", Post: Post{Text: "what qualifies as a poem?"}}
	reply := func(uri, handle string) FeedItem {
		return FeedItem{URI: uri, Author: Profile{DID: "did:" + handle, Handle: handle + ".bsky.social"},
			Root: postRef{URI: root.URI}, Post: Post{Text: "answer from " + handle, Reply: true}}
	}
	other := FeedItem{URI: "at://did:x/app.bsky.feed.post/x", Author: Profile{DID: "did:x", Handle: "x.bsky.social"},
		Root: postRef{URI: "at://elsewhere"}, Post: Post{Text: "unrelated reply", Reply: true}}
	m.items = []FeedItem{root, reply("at://1", "egg"), reply("at://2", "kevin"), other}
	m.cur = 3

	if m.inThread(0) || !m.inThread(1) || !m.inThread(2) || m.inThread(3) {
		t.Fatalf("inThread = %v %v %v %v", m.inThread(0), m.inThread(1), m.inThread(2), m.inThread(3))
	}
	plain := func(i int) []string {
		var out []string
		for _, l := range m.itemLines(i, m.listWidth()) {
			out = append(out, ansiRe.ReplaceAllString(l, ""))
		}
		return out
	}
	if last := plain(0); strings.TrimSpace(last[len(last)-1]) != "│" {
		t.Errorf("the root should lead into its replies with │, got %q", last[len(last)-1])
	}
	if r := plain(1); !strings.HasPrefix(r[0], "  │ ") || !strings.HasPrefix(r[1], "  │ ") {
		t.Errorf("a joined reply is drawn in the thread gutter:\n%s", strings.Join(r, "\n"))
	}
	if last := plain(2); strings.TrimSpace(last[len(last)-1]) != "" {
		t.Errorf("the thread ends before an unrelated post, got %q", last[len(last)-1])
	}
	if r := plain(3); strings.Contains(r[0], "│") {
		t.Errorf("an unrelated reply is not joined: %q", r[0])
	}
}

func TestRepostShowsWhenItWasReposted(t *testing.T) {
	m, _ := followingModel(t)
	now := time.Now().UTC()
	m.items = []FeedItem{{URI: "at://p", Author: Profile{DID: "did:cat", Handle: "cat.bsky.social"}, By: "egg.bsky.social",
		RepostedAt: now.Add(-time.Minute).Format(time.RFC3339),
		Post:       Post{Text: "poem", CreatedAt: now.Add(-8 * time.Minute).Format(time.RFC3339)}}}
	m.cur = 1 // not selected, so the plain row is checked
	h := ansiRe.ReplaceAllString(m.itemLines(0, m.listWidth())[0], "")
	if !strings.HasPrefix(strings.TrimSpace(h), "1m") || !strings.Contains(h, "↻ @egg.bsky.social · posted 8m") {
		t.Errorf("row should be aged by the repost and say when it was posted: %q", h)
	}
}

func TestPostTextLinesUpWithTheAuthorName(t *testing.T) {
	m, _ := followingModel(t)
	m.items = []FeedItem{
		{Author: Profile{DID: "did:a", Handle: "a.bsky.social", DisplayName: "Alpha"}, Post: Post{Text: "body text", Quote: "@q: quoted"}},
		{Author: Profile{DID: "did:b", Handle: "b.bsky.social", DisplayName: "Beta"}, Post: Post{Text: "other"}},
	}
	for _, cur := range []int{0, 1} { // selected and plain rows
		m.cur = cur
		lines := m.itemLines(0, m.listWidth())
		var plain []string
		for _, l := range lines {
			plain = append(plain, ansiRe.ReplaceAllString(l, ""))
		}
		col := func(s, sub string) int { return lipgloss.Width(s[:strings.Index(s, sub)]) } // display column
		name := col(plain[0], "Alpha")
		if got := col(plain[1], "body text"); got != name {
			t.Errorf("cur=%d: text starts at %d, name at %d:\n%s", cur, got, name, strings.Join(plain, "\n"))
		}
		if got := col(plain[2], "▎"); got != name {
			t.Errorf("cur=%d: quote starts at %d, name at %d", cur, got, name)
		}
		if cur == 1 && strings.HasSuffix(plain[0], " ") {
			t.Errorf("no trailing space after a header without tags: %q", plain[0])
		}
	}
}
