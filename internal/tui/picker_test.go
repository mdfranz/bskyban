package tui

import (
	"strings"
	"testing"

	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/termtext"
)

func pickerItem() bsky.FeedItem {
	return bsky.FeedItem{
		URI:    "at://did:nbc/app.bsky.feed.post/3k",
		Author: bsky.Profile{DID: "did:nbc", Handle: "nbcnews.com"},
		Post: bsky.Post{
			Text:     "BREAKING: Reynolds wins, @jennycohn.bsky.social notes. https://nbcnews.to/4zuhIAm. Also email@example.com",
			Link:     &bsky.LinkCard{URI: "https://www.nbcnews.com/politics/georgia", Title: "Georgia results"},
			Quote:    "@gilduran.com: Make tech billionaires squirm",
			QuoteURI: "at://did:gil/app.bsky.feed.post/9q",
		},
	}
}

func TestPostLinksListsEverythingOnceInOrder(t *testing.T) {
	var got []string
	for _, l := range postLinks(pickerItem()) {
		got = append(got, l.label+" = "+l.url)
	}
	want := []string{
		"post on bsky.app = https://bsky.app/profile/nbcnews.com/post/3k",
		"↗ Georgia results = https://www.nbcnews.com/politics/georgia",
		"nbcnews.to/4zuhIAm = https://nbcnews.to/4zuhIAm",
		"@gilduran.com's quoted post = https://bsky.app/profile/did:gil/post/9q",
		"@jennycohn.bsky.social = https://bsky.app/profile/jennycohn.bsky.social",
		"@nbcnews.com's profile = https://bsky.app/profile/nbcnews.com",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("links:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// a card whose URL is also in the text is listed once
	it := pickerItem()
	it.Post.Text = "see " + it.Post.Link.URI
	if n := len(postLinks(it)); n != 4 { // post, card, quoted post, profile
		t.Errorf("duplicate URL listed twice: %d links", n)
	}
}

func TestEnterOpensLinkPicker(t *testing.T) {
	m, _ := feedWithTimeline(t)
	m.items[0] = pickerItem()
	for _, inPost := range []bool{false, true} {
		m.inPost = inPost
		m = press(m, "enter")
		if len(m.picking) == 0 || m.inPost != inPost {
			t.Fatalf("enter inPost=%v: picking=%v inPost=%v", inPost, m.picking, m.inPost)
		}
		m = press(m, "esc")
	}
}

func TestPickerOpensTheChosenLink(t *testing.T) {
	opened := withBrowser(t, nil)
	m, _ := feedWithTimeline(t)
	m.items[0] = pickerItem()
	m = press(m, "enter")
	if len(m.picking) != 6 || m.pickIndex != 0 {
		t.Fatalf("picker = %d links, selection %d", len(m.picking), m.pickIndex)
	}
	v := termtext.StripANSI(m.View())
	for _, w := range []string{"open:", "▸ post on bsky.app", "↗ Georgia results", "↑ ↓ select", "enter open"} {
		if !strings.Contains(v, w) {
			t.Errorf("picker missing %q:\n%s", w, v)
		}
	}
	m = press(m, "down")
	if v = termtext.StripANSI(m.View()); !strings.Contains(v, "▸ ↗ Georgia results") {
		t.Errorf("selection did not move:\n%s", v)
	}
	m = press(m, "enter")
	if len(*opened) != 1 || (*opened)[0] != "https://www.nbcnews.com/politics/georgia" || m.picking != nil {
		t.Fatalf("enter should open the selected link: opened=%v picking=%v", *opened, m.picking != nil)
	}
	m = press(m, "o")
	m = press(m, "up")
	if m.pickIndex != 0 {
		t.Fatal("selection moved before first link")
	}
	for i := 0; i < 10; i++ {
		m = press(m, "down")
	}
	if m.pickIndex != 5 || !strings.Contains(termtext.StripANSI(m.View()), "▸ @nbcnews.com's profile") {
		t.Fatal("last link should be selectable and visible")
	}
	m = press(m, "esc")
	if m.picking != nil || len(*opened) != 1 {
		t.Fatal("escape should cancel")
	}
}
