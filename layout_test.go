package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/termtext"
)

func TestRepliesInTheSameThreadAreJoined(t *testing.T) {
	m, _ := followingModel(t)
	root := bsky.FeedItem{URI: "at://did:cat/app.bsky.feed.post/r", Author: bsky.Profile{DID: "did:cat", Handle: "cat.bsky.social"},
		By: "egg.bsky.social", Post: bsky.Post{Text: "what qualifies as a poem?"}}
	reply := func(uri, handle, parent string) bsky.FeedItem {
		return bsky.FeedItem{URI: uri, Author: bsky.Profile{DID: "did:" + handle, Handle: handle + ".bsky.social"},
			Root: bsky.PostRef{URI: root.URI}, Parent: bsky.PostRef{URI: parent}, Post: bsky.Post{Text: "answer from " + handle, Reply: true}}
	}
	// same thread, but answering someone who isn't shown above it (the Molly White case)
	other := bsky.FeedItem{URI: "at://did:x/app.bsky.feed.post/x", Author: bsky.Profile{DID: "did:x", Handle: "x.bsky.social"},
		Root: bsky.PostRef{URI: root.URI}, Parent: bsky.PostRef{URI: "at://someone-else"}, Post: bsky.Post{Text: "unrelated reply", Reply: true}}
	m.items = []bsky.FeedItem{root, reply("at://1", "egg", root.URI), reply("at://2", "kevin", "at://1"), other}
	m.cur = 3

	if m.inThread(0) || !m.inThread(1) || !m.inThread(2) || m.inThread(3) {
		t.Fatalf("inThread = %v %v %v %v", m.inThread(0), m.inThread(1), m.inThread(2), m.inThread(3))
	}
	plain := func(i int) []string {
		var out []string
		for _, l := range m.itemLines(i, m.listWidth()) {
			out = append(out, termtext.StripLinks(termtext.StripANSI(l)))
		}
		return out
	}
	for i := 0; i < 4; i++ {
		for _, l := range plain(i) {
			if strings.ContainsAny(l, "│┃") {
				t.Fatalf("no bars between thread posts: %q", l)
			}
		}
	}
	nameCol := func(i int, name string) int { l := plain(i)[0]; return lipgloss.Width(l[:strings.Index(l, name)]) }
	if root, rep := nameCol(0, "cat.bsky"), nameCol(1, "egg.bsky"); rep != root+4 {
		t.Errorf("a reply in the thread is indented 4 under the post: root at %d, reply at %d", root, rep)
	}
	if body := plain(1)[1]; lipgloss.Width(body[:strings.Index(body, "answer")]) != nameCol(1, "egg.bsky") {
		t.Errorf("the reply's text lines up with its own name: %q", body)
	}
	if nameCol(3, "x.bsky") != nameCol(0, "cat.bsky") {
		t.Error("a reply to a post that isn't right above it is not indented, even in the same thread")
	}
}

func TestRepostShowsWhenItWasReposted(t *testing.T) {
	m, _ := followingModel(t)
	now := time.Now().UTC()
	m.items = []bsky.FeedItem{{URI: "at://p", Author: bsky.Profile{DID: "did:cat", Handle: "cat.bsky.social"}, By: "egg.bsky.social",
		RepostedAt: now.Add(-time.Minute).Format(time.RFC3339),
		Post:       bsky.Post{Text: "poem", CreatedAt: now.Add(-8 * time.Minute).Format(time.RFC3339)}}}
	m.cur = 1 // not selected, so the plain row is checked
	h := termtext.StripLinks(termtext.StripANSI(m.itemLines(0, m.listWidth())[0]))
	if !strings.HasPrefix(strings.TrimSpace(h), "1m") || !strings.Contains(h, "↻ reposted by @egg.bsky.social · written 8m ago") {
		t.Errorf("row should be aged by the repost and say when it was posted: %q", h)
	}
}

func TestPostTextLinesUpWithTheAuthorName(t *testing.T) {
	m, _ := followingModel(t)
	m.items = []bsky.FeedItem{
		{Author: bsky.Profile{DID: "did:a", Handle: "a.bsky.social", DisplayName: "Alpha"}, Post: bsky.Post{Text: "body text", Quote: "@q: quoted"}},
		{Author: bsky.Profile{DID: "did:b", Handle: "b.bsky.social", DisplayName: "Beta"}, Post: bsky.Post{Text: "other"}},
	}
	for _, cur := range []int{0, 1} { // selected and plain rows
		m.cur = cur
		lines := m.itemLines(0, m.listWidth())
		var plain []string
		for _, l := range lines {
			plain = append(plain, termtext.StripANSI(l))
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

func TestPostViewShowsEngagementAndLink(t *testing.T) {
	it := bsky.FeedItem{
		URI: "at://did:friend/app.bsky.feed.post/3k", CID: "c",
		Author: bsky.Profile{DID: "did:friend", Handle: "friend.bsky.social"},
		Post:   bsky.Post{Text: "hi"},
		Stats:  bsky.PostStats{Likes: 120, Reposts: 1, Replies: 18},
	}

	m, f := feedWithTimeline(t)
	m.items = []bsky.FeedItem{it}
	m = press(m, "enter")
	v := termtext.StripLinks(termtext.StripANSI(m.View()))
	if !strings.Contains(v, "120 likes · 1 repost · 18 replies · 0 quotes · open post ↗") {
		t.Errorf("stats line missing:\n%s", v)
	}
	if !strings.Contains(m.View(), "https://bsky.app/profile/friend.bsky.social/post/3k") {
		t.Error("open post should link to the post on bsky.app")
	}

	m = step(m, key("h"))
	if f.joined() != "like" || m.items[0].Stats.Likes != 121 {
		t.Fatalf("liking should count you: calls=%q likes=%d", f.joined(), m.items[0].Stats.Likes)
	}
	if v := termtext.StripANSI(m.View()); !strings.Contains(v, "♥ 121 likes (you liked this)") {
		t.Errorf("liked stats missing:\n%s", v)
	}
	m = step(m, key("h"))
	if m.items[0].Stats.Likes != 120 {
		t.Errorf("unliking should take you off again: %d", m.items[0].Stats.Likes)
	}
}

func TestPostViewShowsLinkCardURL(t *testing.T) {
	long := "https://www.washingtonpost.com/technology/2026/11/03/google-deepmind-staff-protest-lifetime-ban/"
	p := bsky.Post{Text: "A big night " + long, Link: &bsky.LinkCard{URI: "https://bit.ly/3V270SA", Title: "The Limits of Trump's Midterm Trickery"}}
	full := strings.Join(renderPost(p, 200, true), "\n")
	if plain := termtext.StripANSI(termtext.StripLinks(full)); !strings.Contains(plain, "↗ The Limits of Trump's Midterm Trickery https://bit.ly/3V270SA") || strings.Contains(plain, "[link]") {
		t.Errorf("post view card should show its URL:\n%s", plain)
	}
}

func TestLinkArrowStaysWithItsTitle(t *testing.T) {
	lines := termtext.Wrap("aaaa bbbb ↗ Title here", 10)
	for _, l := range lines {
		if strings.HasSuffix(l, "↗") {
			t.Fatalf("arrow stranded at a line end: %q", lines)
		}
	}
	if strings.Join(lines, " ") != "aaaa bbbb ↗ Title here" {
		t.Errorf("text changed: %q", lines)
	}
}

func TestHintsArePinnedToTheBottomRow(t *testing.T) {
	m, _ := feedWithTimeline(t)
	m.height = 30
	check := func(name string, m model) {
		t.Helper()
		rows := strings.Split(m.View(), "\n")
		if len(rows) != m.height {
			t.Errorf("%s: view has %d rows, want the window's %d", name, len(rows), m.height)
		}
		last := termtext.StripANSI(rows[len(rows)-1])
		if !strings.Contains(last, "q quit") && !strings.Contains(last, "esc cancel") {
			t.Errorf("%s: last row should be the key hints, got %q", name, last)
		}
	}
	check("short list", m) // two posts: lots of empty room above the footer
	m.status = "1 new post"
	m.err = errors.New("boom")
	check("with status and error", m)
	m.status, m.err = "", nil

	m.items = nil
	for i := 0; i < 40; i++ { // more posts than fit
		m.items = append(m.items, bsky.FeedItem{URI: fmt.Sprintf("at://%d", i), Author: bsky.Profile{DID: "did:x", Handle: "x.bsky.social"},
			Post: bsky.Post{Text: strings.Repeat("long text ", 40)}})
	}
	check("full list", m)
	check("post view", press(m, "enter"))

	m.width = 50 // narrow: the hints wrap onto several rows, still at the bottom
	check("narrow", m)

	next, _ := m.Update(key("n"))
	check("compose", next.(model))
}

func TestReplyNamesWhoItAnswers(t *testing.T) {
	m, _ := followingModel(t)
	m.items = []bsky.FeedItem{
		{URI: "at://a", Author: bsky.Profile{DID: "did:gil", Handle: "gilduran.com"}, Post: bsky.Post{Text: "Thank you!", Reply: true, ParentHandle: "drjanicekirsch.bsky.social"}},
		{URI: "at://b", Author: bsky.Profile{DID: "did:x", Handle: "x.bsky.social"}, Post: bsky.Post{Text: "?", Reply: true}},
	}
	m.cur = 5
	if h := termtext.StripANSI(m.itemLines(0, m.listWidth())[0]); !strings.Contains(h, "↳ replying to @drjanicekirsch.bsky.social") {
		t.Errorf("header = %q", h)
	}
	if h := termtext.StripANSI(m.itemLines(1, m.listWidth())[0]); !strings.HasSuffix(h, " reply") {
		t.Errorf("without a known parent it still says reply: %q", h)
	}
}

func TestListAgeLinksToThePost(t *testing.T) {
	m, _ := followingModel(t)
	m.items = []bsky.FeedItem{
		{URI: "at://did:a/app.bsky.feed.post/3k", Author: bsky.Profile{DID: "did:a", Handle: "a.bsky.social"}, Post: bsky.Post{Text: "hi"}},
		{Author: bsky.Profile{DID: "did:b", Handle: "b.bsky.social"}, Post: bsky.Post{Text: "no uri"}},
	}
	link := "\x1b]8;;https://bsky.app/profile/a.bsky.social/post/3k\x1b\\"
	for _, cur := range []int{0, 1} { // as the selected row and as a plain row
		m.cur = cur
		h := m.itemLines(0, m.listWidth())[0]
		if !strings.Contains(h, link) {
			t.Errorf("cur=%d: the age should link to the post: %q", cur, h)
		}
		if w := lipgloss.Width(h); cur == 0 && w != m.listWidth() {
			t.Errorf("the link must not change the selected row's width: %d, want %d", w, m.listWidth())
		}
	}
	if h := m.itemLines(1, m.listWidth())[0]; strings.Contains(h, "\x1b]8;;") {
		t.Errorf("no link without a post URI: %q", h)
	}
}
