package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/termtext"
)

// pickLink is one link the picker (o) offers for the selected post.
type pickLink struct {
	label string // what the picker shows
	url   string
}

// mentionRe finds @handle mentions in a post's text.
var mentionRe = regexp.MustCompile(`(?i)(?:^|[^\w@])@([a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)+)`)

// postLinks lists everything in it worth opening, in a fixed order: the post on
// bsky.app, its link card, URLs in its text, the quoted post, mentioned accounts,
// then the author's profile. Duplicates are dropped.
func postLinks(it bsky.FeedItem) []pickLink {
	var out []pickLink
	seen := map[string]bool{}
	add := func(label, url string) {
		if url == "" || seen[url] {
			return
		}
		seen[url] = true
		out = append(out, pickLink{label: label, url: url})
	}
	p := it.Post
	if it.URI != "" {
		add("post on bsky.app", it.PostURL())
	}
	if c := p.Link; c != nil {
		label := c.Title
		if label == "" {
			label = c.URI
		}
		add("↗ "+label, c.URI)
	}
	for _, u := range termtext.FindURLs(p.Text) {
		add(strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://"), u)
	}
	if p.QuoteURI != "" {
		who := "quoted post"
		if h, _, ok := strings.Cut(p.Quote, ":"); ok && strings.HasPrefix(h, "@") {
			who = h + "'s quoted post"
		}
		add(who, bsky.PostURL(p.QuoteURI))
	}
	for _, mm := range mentionRe.FindAllStringSubmatch(p.Text, -1) {
		add("@"+mm[1], bsky.ProfileURL(mm[1]))
	}
	if h := it.Author.Handle; h != "" {
		add("@"+h+"'s profile", bsky.ProfileURL(h))
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
	m.picking, m.pickIndex, m.status, m.err = links, 0, "", nil
	return m, nil
}

// pickKey navigates the picker and opens the highlighted link.
func (m model) pickKey(key string) (model, tea.Cmd) {
	switch key {
	case "up", "left":
		m.pickIndex = max(m.pickIndex-1, 0)
	case "down", "right":
		m.pickIndex = min(m.pickIndex+1, len(m.picking)-1)
	case "enter", "o":
		link := m.picking[m.pickIndex]
		m.picking = nil
		if err := openURL(link.url); err != nil {
			m.err = fmt.Errorf("open link: %w", err)
		} else {
			m.status = "opened " + link.label
		}
	case "esc", "backspace":
		m.picking = nil
	}
	return m, nil
}

// pickerLines shows a scrolling window of links, with the current one highlighted.
func (m model) pickerLines() []string {
	limit := min(len(m.picking), max(1, min(5, m.height-4)))
	start := max(0, m.pickIndex-limit+1)
	lines := []string{warnStyle.Render("open:")}
	for i := start; i < start+limit; i++ {
		label := termtext.TruncateWidth(termtext.Sanitize(m.picking[i].label), max(m.width-2, 1))
		if i == m.pickIndex {
			lines = append(lines, selStyle.Render("▸ "+label))
		} else {
			lines = append(lines, "  "+label)
		}
	}
	return lines
}
