package main

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

func TestPickerOpensTheChosenLink(t *testing.T) {
	opened := withBrowser(t, nil)
	m, _ := feedWithTimeline(t)
	m.items[0] = pickerItem()
	if !strings.Contains(m.hint(), "o open link") {
		t.Errorf("hint should offer o: %s", m.hint())
	}

	m = press(m, "o")
	if len(m.picking) != 6 {
		t.Fatalf("picking = %d links", len(m.picking))
	}
	v := termtext.StripANSI(m.View())
	for _, w := range []string{"open:", "1 post on bsky.app", "2 ↗ Georgia results", "6 @nbcnews.com's profile", "1-6 open"} {
		if !strings.Contains(v, w) {
			t.Errorf("picker footer missing %q:\n%s", w, v)
		}
	}
	m = press(m, "2")
	if len(*opened) != 1 || (*opened)[0] != "https://www.nbcnews.com/politics/georgia" || m.picking != nil {
		t.Fatalf("2 should open the card and close the picker: opened=%v picking=%v", *opened, m.picking != nil)
	}
	if !strings.Contains(m.status, "opened ↗ Georgia results") {
		t.Errorf("status = %q", m.status)
	}

	// any other key closes it without opening, and is not acted on
	m = press(m, "o")
	m = press(m, "b")
	if m.picking != nil || len(*opened) != 1 || m.confirm != "" || m.busy {
		t.Errorf("b should only close the picker: picking=%v opened=%v busy=%v", m.picking != nil, *opened, m.busy)
	}
	m = press(m, "o")
	m = press(m, "9") // past the end
	if len(*opened) != 1 {
		t.Errorf("a number past the list opens nothing: %v", *opened)
	}
}

func TestOOpensTheOnlyOutsideLink(t *testing.T) {
	opened := withBrowser(t, nil)
	m, _ := feedWithTimeline(t)
	it := pickerItem()
	it.Post.Text = "no urls here"
	m.items[0] = it
	m = press(press(m, "o"), "o")
	if strings.Join(*opened, ",") != it.Post.Link.URI {
		t.Errorf("o o should open the card when it is the only outside link: %v", *opened)
	}

	it.Post.Link = nil // no outside links: o o opens the post itself
	m.items[0] = it
	m = press(press(m, "o"), "o")
	if (*opened)[1] != "https://bsky.app/profile/nbcnews.com/post/3k" {
		t.Errorf("o o without outside links should open the post: %v", *opened)
	}

	m.items[0] = pickerItem() // two outside links: o o picks the post, not a guess
	m = press(press(m, "o"), "o")
	if (*opened)[2] != "https://bsky.app/profile/nbcnews.com/post/3k" {
		t.Errorf("o o with several outside links should open the post: %v", *opened)
	}
}
