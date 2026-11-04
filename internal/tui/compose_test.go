package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/termtext"
	"github.com/mdfranz/bskyban/internal/testpds"
)

// step sends msg and, if it started an async action, feeds that action's result back.
func step(m model, msg tea.Msg) model {
	next, cmd := m.Update(msg)
	m = next.(model)
	if cmd != nil {
		switch r := cmd().(type) {
		case postedMsg, deletedMsg, likedMsg:
			next, _ = m.Update(r)
			m = next.(model)
		}
	}
	return m
}

func typeText(m model, s string) model {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}) // ignore the blink command
		m = next.(model)
	}
	return m
}

func feedWithTimeline(t *testing.T) (model, *testpds.PDS) {
	m, f := followingModel(t)
	m.timeline = func(context.Context, string) ([]bsky.FeedItem, string, error) { return nil, "", nil }
	return m, f
}

func TestComposeAndPost(t *testing.T) {
	m, f := feedWithTimeline(t)
	m = openCompose(m)
	if !m.composing {
		t.Fatal("n should open the compose box")
	}
	m = typeText(m, "q hello")
	if got := m.input.Value(); got != "q hello" {
		t.Fatalf("typing q must not quit; input = %q", got)
	}
	if v := m.View(); !strings.Contains(v, "New post") || !strings.Contains(v, "7/300") {
		t.Errorf("compose view:\n%s", v)
	}
	m = step(m, key("enter"))
	if m.composing || m.err != nil {
		t.Fatalf("composing=%v err=%v", m.composing, m.err)
	}
	if f.Joined() != "post" {
		t.Errorf("calls = %q", f.Joined())
	}
	if !strings.Contains(m.status, "posted") {
		t.Errorf("status = %q", m.status)
	}
}

func TestComposeEscDiscardsAndLongPostIsRefused(t *testing.T) {
	m, f := feedWithTimeline(t)
	m = openCompose(m)
	m = typeText(m, "draft")
	next, _ := m.Update(key("esc"))
	m = next.(model)
	if m.composing || f.Joined() != "" {
		t.Fatalf("esc should discard without posting (composing=%v calls=%q)", m.composing, f.Joined())
	}
	m = openCompose(m)
	m.input.SetValue(strings.Repeat("x", bsky.MaxPostGraphemes+1))
	m = step(m, key("enter"))
	if !m.composing || m.err == nil || f.Joined() != "" {
		t.Errorf("over-long post should stay open with an error (composing=%v err=%v calls=%q)", m.composing, m.err, f.Joined())
	}
}

func TestDeleteOwnPostNeedsConfirmation(t *testing.T) {
	m, f := feedWithTimeline(t)
	own := bsky.FeedItem{Author: bsky.Profile{DID: "did:me", Handle: "me.bsky.social"}, URI: "at://did:me/app.bsky.feed.post/3k",
		Post: bsky.Post{Text: "mine", CreatedAt: "2026-11-01T07:00:00Z"}}
	m.items = append([]bsky.FeedItem{own}, m.items...)
	m = step(m, key("d"))
	if f.Joined() != "" || len(m.items) != 3 {
		t.Fatalf("first d must only ask (calls=%q items=%d)", f.Joined(), len(m.items))
	}
	m = step(m, key("d"))
	if f.Joined() != "delete-post" || len(m.items) != 2 {
		t.Errorf("calls=%q items=%d", f.Joined(), len(m.items))
	}
	if len(f.DeletedKeys()) != 1 || f.DeletedKeys()[0] != "3k" {
		t.Errorf("deleted = %v", f.DeletedKeys())
	}
	m = step(m, key("d")) // now on someone else's post
	m = step(m, key("d"))
	if f.Joined() != "delete-post" || len(m.items) != 2 {
		t.Errorf("someone else's post must not be deleted (calls=%q)", f.Joined())
	}
}

// openCompose presses n without running the cursor-blink command it returns.
func openCompose(m model) model {
	next, _ := m.Update(key("n"))
	return next.(model)
}

func TestPostsModelIsAFeedOfYourOwnPosts(t *testing.T) {
	a, _ := testpds.New(t)
	m := newPostsModel(a, func(context.Context, string) ([]bsky.FeedItem, string, error) { return nil, "", nil })
	m.followLoading = false
	m.width, m.height = 100, 30
	m.items = []bsky.FeedItem{{Author: bsky.Profile{DID: "did:me", Handle: "me.bsky.social"}, URI: "at://did:me/app.bsky.feed.post/3k", Post: bsky.Post{Text: "mine"}}}
	v := m.View()
	for _, w := range []string{"bskyban posts", "mine", "d delete", "n new post"} {
		if !strings.Contains(v, w) {
			t.Errorf("missing %q:\n%s", w, v)
		}
	}
	if next, _ := m.Update(key("tab")); next.(model).tab != 0 {
		t.Error("a single-tab model should not switch tabs")
	}
}

func TestReplyReferencesParentAndRoot(t *testing.T) {
	root := bsky.PostRef{URI: "at://did:x/app.bsky.feed.post/root", CID: "cidroot"}
	it := bsky.FeedItem{URI: "at://did:x/app.bsky.feed.post/p", CID: "cidp", Root: root}
	if r := it.ReplyTo(); r.Parent.CID != "cidp" || r.Root != root {
		t.Errorf("reply to a reply = %+v", r)
	}
	top := bsky.FeedItem{URI: "at://did:x/app.bsky.feed.post/t", CID: "cidt"}
	if r := top.ReplyTo(); r.Root != r.Parent || r.Root.CID != "cidt" {
		t.Errorf("reply to a top-level post = %+v", r)
	}
}

func TestReplyComposeShowsTarget(t *testing.T) {
	m, f := feedWithTimeline(t)
	m.items[0].URI, m.items[0].CID = "at://did:friend/app.bsky.feed.post/1", "cid1"
	next, _ := m.Update(key("r"))
	m = next.(model)
	if !m.composing || m.replyTo == nil {
		t.Fatal("r should open a reply")
	}
	if v := m.View(); !strings.Contains(v, "Reply to @friend.bsky.social") || !strings.Contains(v, "hello world") {
		t.Errorf("view:\n%s", v)
	}
	m = typeText(m, "thanks")
	m = step(m, key("enter"))
	if m.composing || f.Joined() != "post" {
		t.Errorf("composing=%v calls=%q err=%v", m.composing, f.Joined(), m.err)
	}
}

func TestLikeTogglesAndMarksEveryCopy(t *testing.T) {
	m, f := feedWithTimeline(t)
	uri := "at://did:rando/app.bsky.feed.post/9"
	m.items[0].URI, m.items[0].CID = uri, "cid9"
	m.items[1].URI, m.items[1].CID = uri, "cid9" // the same post, reposted
	m = step(m, key("h"))
	if f.Joined() != "like" || m.items[0].Like == "" || m.items[1].Like == "" {
		t.Fatalf("calls=%q likes=%q/%q err=%v", f.Joined(), m.items[0].Like, m.items[1].Like, m.err)
	}
	if v := m.View(); !strings.Contains(v, "♥") {
		t.Errorf("liked post should show ♥:\n%s", v)
	}
	m = step(m, key("h"))
	if f.Joined() != "like,unlike" || m.items[0].Like != "" || m.items[1].Like != "" {
		t.Errorf("calls=%q likes=%q/%q", f.Joined(), m.items[0].Like, m.items[1].Like)
	}
}

func TestLikeAndReplyOnTheRepostedPostInTheRepostsTab(t *testing.T) {
	m, f := feedWithTimeline(t)
	m.tab = 1 // Reposts
	author := bsky.Profile{DID: "did:rando", Handle: "rando.bsky.social"}
	item := bsky.FeedItem{Author: author, URI: "at://did:rando/app.bsky.feed.post/9", CID: "cid9", Post: bsky.Post{Text: "buy now"}}
	m.list = []bsky.Profile{author}
	m.ctxs = map[string]app.RepostCtx{"did:rando": {Post: item.Post, Item: item, By: []string{"friend.bsky.social"}}}
	m = step(m, key("h"))
	if f.Joined() != "like" || m.ctxs["did:rando"].Item.Like == "" {
		t.Fatalf("calls=%q err=%v", f.Joined(), m.err)
	}
	if v := m.View(); !strings.Contains(v, "you liked this") {
		t.Errorf("view:\n%s", v)
	}
	next, _ := m.Update(key("r"))
	m = next.(model)
	if !m.composing || m.replyTo == nil || m.replyTo.URI != item.URI {
		t.Fatalf("r should reply to the reposted post: %+v", m.replyTo)
	}
}

func TestUpDownStepThroughPostsInPostView(t *testing.T) {
	m, _ := followingModel(t)
	m = press(m, "enter")
	m = press(m, "down")
	if m.cur != 1 || !m.inPost {
		t.Fatalf("down should open the next post: cur=%d inPost=%v", m.cur, m.inPost)
	}
	m = press(m, "up")
	if m.cur != 0 {
		t.Errorf("up should go back: cur=%d", m.cur)
	}
	m = press(m, "pgdown")
	if m.cur != 0 || m.scroll < 0 {
		t.Errorf("pgdown scrolls the panel, not the posts: cur=%d", m.cur)
	}
}

func TestCurrentPostIsMarkedAndNotRepeatedInRecentPosts(t *testing.T) {
	m, _ := followingModel(t)
	m = press(m, "enter")
	older := bsky.Post{Text: "older one", CreatedAt: "2026-11-01T01:00:00Z"}
	pp := []bsky.Post{m.items[0].Post, older}
	m.posts["did:friend"] = &pp
	v := termtext.StripLinks(m.View())
	if !strings.Contains(v, "▸ This post · 1 of 2") {
		t.Errorf("current post not marked:\n%s", v)
	}
	if n := strings.Count(v, "hello world"); n != 1 {
		t.Errorf("current post shown %d times:\n%s", n, v)
	}
	if !strings.Contains(v, "older one") {
		t.Errorf("other recent posts missing:\n%s", v)
	}
}

func TestHIsHeartInTheFeedButStillMovesInReview(t *testing.T) {
	m, f := feedWithTimeline(t)
	m.items[0].URI, m.items[0].CID = "at://did:friend/app.bsky.feed.post/1", "c1"
	m = press(m, "right") // open the post; h used to mean "previous" here
	m = step(m, key("h"))
	if f.Joined() != "like" || m.cur != 0 {
		t.Errorf("h should like and stay on the post: calls=%q cur=%d", f.Joined(), m.cur)
	}
	if strings.Contains(m.hint(), "L like") || !strings.Contains(m.hint(), "h like") {
		t.Errorf("hint should say h like: %s", m.hint())
	}
}

func TestLNoLongerMovesRightInTheFeedButDoesInReview(t *testing.T) {
	m, _ := feedWithTimeline(t)
	m = press(m, "right")
	m = press(m, "l")
	if m.cur != 0 {
		t.Errorf("l should do nothing in the Following post view: cur=%d", m.cur)
	}
	m = press(m, "right")
	if m.cur != 1 {
		t.Errorf("→ still moves to the next post: cur=%d", m.cur)
	}
	m.inPost, m.cur = false, 0
	m = press(m, "l")
	if m.inPost || m.cur != 0 {
		t.Errorf("l should not open a post from the list: inPost=%v cur=%d", m.inPost, m.cur)
	}

	m.tab = 1 // Reposts
	m.list = []bsky.Profile{{DID: "did:a", Handle: "a.bsky.social"}, {DID: "did:b", Handle: "b.bsky.social"}}
	m.ctxs = map[string]app.RepostCtx{}
	m = press(m, "l")
	if m.idx != 0 {
		t.Errorf("l should not move to the next account in the feed: idx=%d", m.idx)
	}
	m = press(m, "right")
	if m.idx != 1 {
		t.Errorf("→ moves to the next account: idx=%d", m.idx)
	}

	r, _ := candidates(t) // follower review keeps its vim keys
	r = press(r, "l")
	if r.idx != 1 {
		t.Errorf("l moves right in follower review: idx=%d", r.idx)
	}
	r = press(r, "h")
	if r.idx != 0 {
		t.Errorf("h moves left in follower review: idx=%d", r.idx)
	}
}
