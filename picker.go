package main

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mdfranz/bskyban/internal/termtext"
)

// pickLink is one link the picker (o) offers for the selected post.
type pickLink struct {
	label string // what the picker shows
	url   string
	ext   bool // leads off Bluesky (a link card or a URL in the text)
}

// maxPicks is how many links the picker numbers (keys 1-9).
const maxPicks = 9

// mentionRe finds @handle mentions in a post's text.
var mentionRe = regexp.MustCompile(`(?i)(?:^|[^\w@])@([a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)+)`)

// postLinks lists everything in it worth opening, in a fixed order: the post on
// bsky.app, its link card, URLs in its text, the quoted post, mentioned accounts,
// then the author's profile. Duplicates are dropped.
func postLinks(it FeedItem) []pickLink {
	var out []pickLink
	seen := map[string]bool{}
	add := func(label, url string, ext bool) {
		if url == "" || seen[url] || len(out) >= maxPicks {
			return
		}
		seen[url] = true
		out = append(out, pickLink{label: label, url: url, ext: ext})
	}
	p := it.Post
	if it.URI != "" {
		add("post on bsky.app", postLink(it), false)
	}
	if c := p.Link; c != nil {
		label := c.Title
		if label == "" {
			label = c.URI
		}
		add("↗ "+label, c.URI, true)
	}
	for _, u := range termtext.FindURLs(p.Text) {
		add(strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://"), u, true)
	}
	if p.QuoteURI != "" {
		who := "quoted post"
		if h, _, ok := strings.Cut(p.Quote, ":"); ok && strings.HasPrefix(h, "@") {
			who = h + "'s quoted post"
		}
		add(who, postURL(p.QuoteURI), false)
	}
	for _, mm := range mentionRe.FindAllStringSubmatch(p.Text, -1) {
		add("@"+mm[1], "https://bsky.app/profile/"+mm[1], false)
	}
	if h := it.Author.Handle; h != "" {
		add("@"+h+"'s profile", "https://bsky.app/profile/"+h, false)
	}
	return out
}

// openKey (o) shows the picker for the selected post.
func (m model) openKey() (model, tea.Cmd) {
	it, ok := m.selected()
	if !ok {
		return m, nil
	}
	links := postLinks(it)
	if len(links) == 0 {
		m.status = "nothing to open"
		return m, nil
	}
	m.picking, m.status, m.err = links, "", nil
	return m, nil
}

// pickKey handles a key while the picker is open: 1-9 opens that link, o opens
// the post's one outside link (or the post itself), anything else closes it.
func (m model) pickKey(key string) (model, tea.Cmd) {
	links := m.picking
	m.picking = nil
	n := -1
	switch {
	case len(key) == 1 && key[0] >= '1' && key[0] <= '9':
		n = int(key[0] - '1')
	case key == "o":
		n = defaultPick(links)
	}
	if n < 0 || n >= len(links) {
		return m, nil // closed without opening anything
	}
	if err := openURL(links[n].url); err != nil {
		m.err = fmt.Errorf("open link: %w", err)
		return m, nil
	}
	m.status = "opened " + links[n].label
	return m, nil
}

// defaultPick is what o o opens: the only link leading off Bluesky, if there is
// exactly one, otherwise the first entry (the post on bsky.app).
func defaultPick(links []pickLink) int {
	ext := -1
	for i, l := range links {
		if l.ext {
			if ext >= 0 {
				return 0
			}
			ext = i
		}
	}
	if ext >= 0 {
		return ext
	}
	return 0
}

// pickerLines is the numbered list the footer shows while picking, wrapped to w.
func (m model) pickerLines() []string {
	items := make([]string, len(m.picking))
	for i, l := range m.picking {
		items[i] = fmt.Sprintf("%d %s", i+1, truncateWidth(termtext.Sanitize(l.label), 40))
	}
	return wrapItems(append([]string{"open:"}, items...), m.width)
}
