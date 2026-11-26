package tui

// The Following tab's timeline list: its viewport, clipping cache and rows.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
	"github.com/mdfranz/bskyban/internal/termtext"
)

// ensureVisible scrolls the timeline list so the selected item is fully on screen.
func (m model) ensureVisible() model {
	if len(m.items) == 0 {
		return m
	}
	m.cur = min(max(m.cur, 0), len(m.items)-1)
	m.listTop = max(min(m.listTop, m.cur), 0)
	// Walk up from the selection to the highest item that still fits with it on
	// screen; that only renders one screenful, however far the selection jumped.
	w, room, h := m.listWidth(), m.avail(), 0
	low := m.cur
	for i := m.cur; i >= m.listTop; i-- {
		if h += len(m.itemLines(i, w)); h > room {
			break
		}
		low = i
	}
	m.listTop = low
	return m
}

// previewLines is how many lines of each post the timeline list shows.
const previewLines = 3

// quoteLines is how many lines of a quoted post the list shows.
const quoteLines = 2

// clip hard-wraps text to w columns, keeping at most n lines (the last ends in "…"
// when something was cut). Links are not needed in the list, so OSC 8 is removed.
func clip(text string, w, n int) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(termtext.StripLinks(strings.Join(termtext.Wrap(text, w), "\n")), "\n")
	if len(lines) > n {
		lines = lines[:n]
		lines[n-1] = termtext.TruncateWidth(lines[n-1]+" …", w)
	}
	return lines
}

type (
	clipKey struct {
		key string // FeedItem.Key()
		w   int
	}
	clipped struct{ text, quote []string }
)

// bodyLines is the item's clipped text and quoted post at width bw. Wrapping is
// most of the cost of a frame and a post's text never changes once fetched, so
// each item is wrapped once per width (the cache is cleared when the width changes).
func (m model) bodyLines(it bsky.FeedItem, bw int) (text, quote []string) {
	k := clipKey{it.Key(), bw}
	if c, ok := m.clips[k]; ok {
		return c.text, c.quote
	}
	c := clipped{clip(it.Post.Preview(), bw, previewLines), clip(termtext.OneLine(it.Post.Quote), bw-2, quoteLines)}
	if m.clips != nil && it.URI != "" { // without a URI, items cannot be told apart
		m.clips[k] = c
	}
	return c.text, c.quote
}

// inThread reports whether item i answers the post right above it, so the list
// indents it beneath that post. Replies to anything else (even in the same thread)
// stay at the normal indent; nothing moves.
func (m model) inThread(i int) bool {
	if i <= 0 || i >= len(m.items) {
		return false
	}
	it := m.items[i]
	return it.Post.Reply && it.By == "" && it.Parent.URI != "" && it.Parent.URI == m.items[i-1].URI
}

// ageLink is the row's age, padded to 5 columns, as a clickable link to the post
// on bsky.app (in terminals that support OSC 8 hyperlinks).
func ageLink(it bsky.FeedItem, age string) string {
	pad := strings.Repeat(" ", max(5-lipgloss.Width(age), 0))
	if it.URI == "" {
		return age + pad
	}
	return termtext.OSC8(it.PostURL(), age) + pad
}

func (m model) listWidth() int {
	if m.width <= 0 {
		return 110
	}
	return max(m.width-2, 40)
}

// itemLines renders timeline item i: a header line, up to previewLines lines of
// text (ending in … if there is more), and a blank separator line.
func (m model) itemLines(i, w int) []string {
	it := m.items[i]
	age := termtext.HumanAgeISO(it.Post.CreatedAt)
	if it.By != "" && it.RepostedAt != "" {
		age = termtext.HumanAgeISO(it.RepostedAt) // when it reached your timeline; the post's own age is a tag
	}
	threaded := m.inThread(i)
	gutter, gw := "", 0 // a reply right under its thread is indented beneath it
	if threaded {
		gutter, gw = "    ", 4
	}
	name := it.Author.DisplayName
	if name == "" {
		name = it.Author.Handle
	}
	who := fmt.Sprintf("%s (@%s)", name, it.Author.Handle)
	var tagsPlain, tags []string
	add := func(plain string, st lipgloss.Style) {
		tagsPlain = append(tagsPlain, plain)
		tags = append(tags, st.Render(plain))
	}
	if it.By != "" {
		add("↻ reposted by @"+it.By, accentStyle)
		if posted := termtext.HumanAgeISO(it.Post.CreatedAt); it.RepostedAt != "" && posted != age {
			add("· written "+posted+" ago", headingStyle.Bold(false).Italic(true))
		}
	}
	if it.Post.Reply {
		if h := it.Post.ParentHandle; h != "" {
			add("↳ replying to @"+h, dimStyle)
		} else {
			add("reply", dimStyle)
		}
	}
	if it.Like != "" {
		add("♥", errStyle)
	}
	fresh, isFresh := m.freshStyle(it.Key())
	if isFresh {
		add("new", fresh)
	}
	if d := m.a.State.Entries[it.Author.DID].Decision; d != store.Skipped { // skipped stays off the list
		if st, ok := decisionStyle(d); ok {
			add(d, st)
		}
	}
	tailPlain, tail := strings.Join(tagsPlain, " "), strings.Join(tags, " ")
	who = termtext.TruncateWidth(who, max(w-(2+gw+5+1+1+lipgloss.Width(tailPlain)), 10))
	namePart, handlePart := who, ""
	if strings.HasPrefix(who, name+" (@") {
		namePart, handlePart = who[:len(name)], who[len(name):]
	}

	// the post's own text, then the quoted post on lines of its own, both lined up
	// under the author's name: lead (2) + age (5) + space = column 8
	const indent = "      " // after the 2-column lead
	bw := w - 2 - len(indent) - gw
	text, quote := m.bodyLines(it, bw)
	if len(text) == 0 && len(quote) == 0 {
		text = []string{"(no text)"}
	}

	var lines []string
	if i == m.cur {
		styledWho := selStyle.Bold(true).Render(namePart) + selStyle.Foreground(theme.Heading).Render(handlePart)
		selectedTags := append([]string(nil), tagsPlain...)
		for j, tag := range selectedTags {
			if strings.HasPrefix(tag, "· written ") {
				selectedTags[j] = selStyle.Foreground(theme.Heading).Italic(true).Render(tag)
			}
		}
		lines = append(lines, selStyle.Width(w).Render(strings.TrimRight(fmt.Sprintf("▸ %s%s %s %s", gutter, ageLink(it, age), styledWho, strings.Join(selectedTags, " ")), " ")))
		for _, l := range text {
			lines = append(lines, selStyle.Width(w).Render("  "+gutter+indent+l))
		}
		for _, l := range quote {
			lines = append(lines, selStyle.Width(w).Render("  "+gutter+indent+"▎ "+l))
		}
	} else {
		lead := "  "
		if isFresh {
			lead = fresh.Render("┃") + " "
		}
		lead += gutter
		styledWho := titleStyle.Render(namePart) + headingStyle.Bold(false).Render(handlePart)
		header := fmt.Sprintf("%s%s %s", lead, dimStyle.Render(ageLink(it, age)), styledWho)
		if tail != "" {
			header += " " + tail
		}
		lines = append(lines, header)
		for _, l := range text {
			lines = append(lines, lead+indent+dimStyle.Render(l))
		}
		for _, l := range quote {
			lines = append(lines, lead+indent+accentStyle.Render("▎ ")+dimStyle.Render(l))
		}
	}
	return append(lines, "")
}

func (m model) listView() string {
	w, used := m.listWidth(), 0
	var b strings.Builder
	for i := m.listTop; i < len(m.items); i++ {
		lines := m.itemLines(i, w)
		if used > 0 && used+len(lines) > m.avail() {
			break
		}
		b.WriteString(strings.Join(lines, "\n") + "\n")
		used += len(lines)
	}
	return b.String()
}
