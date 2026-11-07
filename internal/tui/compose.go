package tui

// Writing and replying to posts, liking, and deleting your own posts.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
	"github.com/mdfranz/bskyban/internal/termtext"
	"github.com/rivo/uniseg"
)

// composeKey edits the post being written: enter publishes it, esc discards it.
func (m model) composeKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "ctrl+c" {
		if m.busy {
			m.quitting = true
			m.status = "finishing action before quitting…"
			return m, nil
		}
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}
	switch key {
	case "esc":
		m.composing, m.replyTo, m.quoting, m.err = false, nil, nil, nil
		m.input.Blur()
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" && m.quoting != nil { // sharing without a comment is a plain repost
			m.busy, m.err = true, nil
			c, ref := m.a.Client, m.quoting.Ref()
			return m, func() tea.Msg {
				uri, err := c.Repost(context.Background(), ref)
				return postedMsg{uri: uri, quote: &ref, repost: true, err: err}
			}
		}
		if text == "" {
			return m, nil
		}
		if err := bsky.ValidatePostText(text); err != nil {
			m.err = err
			return m, nil
		}
		m.busy, m.err = true, nil
		c := m.a.Client
		var reply *bsky.ReplyRef
		if m.replyTo != nil {
			reply = m.replyTo.ReplyTo()
		}
		var quote *bsky.PostRef
		if m.quoting != nil {
			ref := m.quoting.Ref()
			quote = &ref
		}
		return m, func() tea.Msg {
			uri, err := c.PostWith(context.Background(), text, bsky.PostOptions{Reply: reply, Quote: quote})
			return postedMsg{text: text, uri: uri, reply: reply, quote: quote, err: err}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	m.err = nil
	return m, cmd
}

// applyPosted logs a finished post and pulls it into the timeline.
func (m model) applyPosted(msg postedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	ev := store.AuditEvent{Action: "post", DID: m.a.Client.DID, Handle: m.a.Client.Handle, Source: "tui:following", OK: msg.err == nil, Record: msg.uri,
		Detail: termtext.TruncateWidth(termtext.OneLine(msg.text), 200)}
	switch {
	case msg.repost:
		ev.Action, ev.Detail = "repost", msg.quote.URI
	case msg.quote != nil:
		ev.Via = "quote of " + msg.quote.URI
	case msg.reply != nil:
		ev.Via = "reply to " + msg.reply.Parent.URI
	}
	if msg.err != nil {
		ev.Error = msg.err.Error()
		m.audit(ev)
		m.err = msg.err // the text stays in the box so nothing typed is lost
		m.quitting = false
		return m, nil
	}
	m.audit(ev)
	m.composing, m.replyTo, m.quoting = false, nil, nil
	m.input.Blur()
	m.input.SetValue("")
	if m.quitting && m.err == nil {
		return m, tea.Quit
	}
	m.quitting = false
	m, cmd := m.startCheck(true) // shows the new post at the top
	m.status = "posted " + bsky.PostURL(msg.uri)
	if msg.repost {
		m.status = "reposted"
	}
	return m, cmd
}

// likeKey likes the selected post, or removes your like if you already liked it.
func (m model) likeKey() (model, tea.Cmd) {
	it, ok := m.selected()
	if !ok || it.URI == "" {
		return m, nil
	}
	m.busy, m.err, m.status = true, nil, ""
	c := m.a.Client
	return m, func() tea.Msg {
		ctx := context.Background()
		if it.Like != "" {
			return likedMsg{item: it, unlik: true, err: c.Unlike(ctx, it.Like)}
		}
		uri, err := c.Like(ctx, bsky.PostRef{URI: it.URI, CID: it.CID})
		return likedMsg{item: it, like: uri, err: err}
	}
}

// applyLiked logs a like or unlike and marks the post (every copy of it) in the lists.
func (m model) applyLiked(msg likedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	it := msg.item
	ev := store.AuditEvent{Action: "like", DID: m.a.Client.DID, Handle: m.a.Client.Handle, Source: "tui:following", OK: msg.err == nil,
		Record: msg.like, Detail: it.URI, Via: "@" + it.Author.Handle}
	if msg.unlik {
		ev.Action, ev.Record = "unlike", it.Like
	}
	if msg.err != nil {
		ev.Error = msg.err.Error()
		m.audit(ev)
		m.err = msg.err
		m.quitting = false
		return m, nil
	}
	m.audit(ev)
	// your like adds to (or comes off) the count shown, until the next fetch
	setLike := func(x *bsky.FeedItem) {
		switch {
		case x.Like == "" && msg.like != "":
			x.Stats.Likes++
		case x.Like != "" && msg.like == "" && x.Stats.Likes > 0:
			x.Stats.Likes--
		}
		x.Like = msg.like
	}
	for did, rc := range m.ctxs {
		if rc.Item.URI == it.URI {
			setLike(&rc.Item)
			m.ctxs[did] = rc
		}
	}
	for _, list := range [][]bsky.FeedItem{m.items, m.newPosts} {
		for i := range list {
			if list[i].URI == it.URI {
				setLike(&list[i])
			}
		}
	}
	m.status = "liked"
	if msg.unlik {
		m.status = "removed your like"
	}
	if m.quitting {
		return m, tea.Quit
	}
	return m, nil
}

// selected is the timeline post under the cursor.
func (m model) selected() (bsky.FeedItem, bool) {
	if m.active() == tabCandidates { // the reposted post of the account on screen
		if m.loading || m.idx >= len(m.list) {
			return bsky.FeedItem{}, false
		}
		it := m.ctxs[m.list[m.idx].DID].Item
		return it, it.URI != ""
	}
	if m.cur >= len(m.items) {
		return bsky.FeedItem{}, false
	}
	return m.items[m.cur], true
}

// ownItem is the selected timeline post when it is yours.
func (m model) ownItem() (bsky.FeedItem, bool) {
	if m.active() != tabFollowing || m.cur >= len(m.items) {
		return bsky.FeedItem{}, false
	}
	it := m.items[m.cur]
	return it, it.Author.DID == m.a.Client.DID && it.URI != ""
}

// deleteKey deletes your own selected post, after a second press of d.
func (m model) deleteKey(confirm string) (model, tea.Cmd) {
	it, ok := m.ownItem()
	if !ok {
		if m.active() == tabFollowing && m.timeline != nil {
			m.status = "you can only delete your own posts"
		}
		return m, nil
	}
	if confirm != "del:"+it.URI {
		m.confirm = "del:" + it.URI
		m.status = "Press d again to delete this post."
		return m, nil
	}
	m.busy, m.err = true, nil
	c := m.a.Client
	return m, func() tea.Msg {
		return deletedMsg{item: it, err: c.DeletePost(context.Background(), it.URI)}
	}
}

// applyDeleted logs a deletion and drops the post from the list.
func (m model) applyDeleted(msg deletedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	it := msg.item
	ev := store.AuditEvent{Action: "delete-post", DID: m.a.Client.DID, Handle: m.a.Client.Handle, Source: "tui:following", OK: msg.err == nil, Record: it.URI,
		Detail: termtext.TruncateWidth(termtext.OneLine(it.Post.Text), 200)}
	if msg.err != nil {
		ev.Error = msg.err.Error()
		m.audit(ev)
		m.err = msg.err
		m.quitting = false
		return m, nil
	}
	m.audit(ev)
	keep := func(items []bsky.FeedItem) []bsky.FeedItem {
		return slices.DeleteFunc(slices.Clone(items), func(x bsky.FeedItem) bool { return x.URI == it.URI })
	}
	removedBefore := 0
	for i, x := range m.items {
		if x.URI == it.URI && i < m.cur {
			removedBefore++
		}
	}
	m.items, m.newPosts = keep(m.items), keep(m.newPosts)
	for k := range m.fresh {
		if strings.HasPrefix(k, it.URI+"|") {
			delete(m.fresh, k)
		}
	}
	m.cur = min(max(m.cur-removedBefore, 0), max(len(m.items)-1, 0))
	if len(m.items) == 0 {
		m.inPost = false
	}
	m.listTop, m.scroll = min(m.listTop, m.cur), 0
	m = m.ensureVisible()
	m.status = "deleted post"
	if m.quitting {
		return m, tea.Quit
	}
	return m, m.prefetch()
}

// composeView is the new-post screen: the text box and a length counter (errors
// and hints are in the footer View pins to the bottom).
func (m model) composeView() string {
	var b strings.Builder
	if r := m.replyTo; r != nil {
		b.WriteString(headingStyle.Render("Reply to @"+termtext.Sanitize(r.Author.Handle)) + "\n")
		quote := termtext.OneLine(termtext.Sanitize(r.Post.Text))
		if w := m.width - 4; w > 10 {
			quote = termtext.TruncateWidth(quote, w)
		}
		b.WriteString(dimStyle.Render("▎ "+quote) + "\n\n")
	} else if q := m.quoting; q != nil {
		b.WriteString(headingStyle.Render("Share @"+termtext.Sanitize(q.Author.Handle)+"'s post") + "\n")
		quote := termtext.OneLine(termtext.Sanitize(q.Post.Text))
		if w := m.width - 4; w > 10 {
			quote = termtext.TruncateWidth(quote, w)
		}
		b.WriteString(dimStyle.Render("▎ "+quote) + "\n")
		b.WriteString(dimStyle.Render("Add a comment to quote it, or leave it empty to repost as is.") + "\n\n")
	} else {
		b.WriteString(headingStyle.Render("New post") + "\n\n")
	}
	in := m.input
	if m.width > 6 {
		in.Width = m.width - 4
	}
	b.WriteString(in.View() + "\n\n")
	n := uniseg.GraphemeClusterCount(in.Value())
	count := fmt.Sprintf("%d/%d", n, bsky.MaxPostGraphemes)
	if n > bsky.MaxPostGraphemes {
		count = errStyle.Render(count)
	} else {
		count = dimStyle.Render(count)
	}
	b.WriteString(count + "\n")
	return b.String()
}
