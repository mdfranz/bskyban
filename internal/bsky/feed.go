package bsky

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// GetRecentPosts returns up to limit of the account's latest posts, replies and reposts included.
func (c *Client) GetRecentPosts(ctx context.Context, actor string, limit int) ([]Post, error) {
	q := url.Values{"actor": {actor}, "limit": {fmt.Sprint(limit)}, "filter": {"posts_with_replies"}}
	entries, _, err := c.feedPage(ctx, "app.bsky.feed.getAuthorFeed", q)
	if err != nil {
		return nil, err
	}
	posts := make([]Post, 0, len(entries))
	for _, e := range entries {
		posts = append(posts, e.toPost())
	}
	return posts, nil
}

// feedEntry is one app.bsky.feed.defs#feedViewPost.
type feedEntry struct {
	Post struct {
		URI string `json:"uri"`
		CID string `json:"cid"`
		PostStats
		Viewer struct {
			Like string `json:"like"`
		} `json:"viewer"`
		Author Profile         `json:"author"`
		Embed  json.RawMessage `json:"embed"` // hydrated view
		Record struct {
			Text      string          `json:"text"`
			CreatedAt string          `json:"createdAt"`
			Reply     json.RawMessage `json:"reply"`
			Embed     json.RawMessage `json:"embed"`
			Facets    []Facet         `json:"facets"`
		} `json:"record"`
	} `json:"post"`
	Reply struct {
		Parent struct {
			Author Profile `json:"author"`
			Record struct {
				Text string `json:"text"`
			} `json:"record"`
		} `json:"parent"`
	} `json:"reply"`
	Reason struct {
		Type      string  `json:"$type"`
		By        Profile `json:"by"`
		IndexedAt string  `json:"indexedAt"` // when it was reposted
	} `json:"reason"`
}

// viewContext pulls a quoted post and image URLs out of a hydrated embed view.
func viewContext(raw json.RawMessage) (quote, quoteURI string, images []string) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var v struct {
		Type   string `json:"$type"`
		Images []struct {
			Fullsize string `json:"fullsize"`
		} `json:"images"`
		Record json.RawMessage `json:"record"`
		Media  json.RawMessage `json:"media"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	for _, im := range v.Images {
		if im.Fullsize != "" {
			images = append(images, im.Fullsize)
		}
	}
	if len(v.Media) > 0 { // recordWithMedia
		_, _, mi := viewContext(v.Media)
		images = append(images, mi...)
	}
	if len(v.Record) > 0 {
		var r struct {
			URI    string  `json:"uri"`
			Author Profile `json:"author"`
			Value  struct {
				Text   string  `json:"text"`
				Facets []Facet `json:"facets"`
			} `json:"value"`
			Record json.RawMessage `json:"record"` // recordWithMedia nests one level deeper
		}
		if json.Unmarshal(v.Record, &r) == nil {
			if r.Author.Handle == "" && len(r.Record) > 0 {
				_ = json.Unmarshal(r.Record, &r)
			}
			if r.Author.Handle != "" {
				quote = "@" + r.Author.Handle + ": " + expandLinks(r.Value.Text, r.Value.Facets)
				quoteURI = r.URI
			}
		}
	}
	return
}

func (e feedEntry) isRepost() bool { return strings.HasSuffix(e.Reason.Type, "reasonRepost") }

func (e feedEntry) toPost() Post {
	r := e.Post.Record
	p := Post{
		Text:         expandLinks(r.Text, r.Facets),
		CreatedAt:    r.CreatedAt,
		Repost:       e.isRepost(),
		Reply:        len(r.Reply) > 0 && string(r.Reply) != "null",
		Attach:       describeEmbed(r.Embed),
		Link:         embedLink(r.Embed),
		ParentHandle: e.Reply.Parent.Author.Handle,
		ParentText:   e.Reply.Parent.Record.Text,
	}
	p.Quote, p.QuoteURI, p.Images = viewContext(e.Post.Embed)
	if p.Quote != "" { // the quote text replaces the bare "[quote post]" marker
		p.Attach = slices.DeleteFunc(p.Attach, func(s string) bool { return s == "[quote post]" })
	}
	return p
}

func (c *Client) feedPage(ctx context.Context, nsid string, q url.Values) ([]feedEntry, string, error) {
	var out struct {
		Feed   []feedEntry `json:"feed"`
		Cursor string      `json:"cursor"`
	}
	if err := c.do(ctx, http.MethodGet, nsid, q, nil, &out); err != nil {
		return nil, "", err
	}
	return out.Feed, out.Cursor, nil
}

// PostRef identifies one post (strong reference: AT URI plus content hash).
type PostRef struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

// ReplyRef is what a reply record needs to point at its place in a thread.
type ReplyRef struct {
	Root   PostRef `json:"root"`
	Parent PostRef `json:"parent"`
}

// replyRefs is where the post sits in its thread (zero if it is not a reply).
func (e feedEntry) replyRefs() ReplyRef {
	var r ReplyRef
	_ = json.Unmarshal(e.Post.Record.Reply, &r)
	return r
}

// item turns a feed entry into a timeline item.
func (e feedEntry) item() FeedItem {
	r := e.replyRefs()
	return FeedItem{Post: e.toPost(), Author: e.Post.Author, URI: e.Post.URI, CID: e.Post.CID, Root: r.Root, Parent: r.Parent,
		Like: e.Post.Viewer.Like, Stats: e.Post.PostStats}
}

// FeedItem is one timeline entry: a post, its author, and who reposted it (if anyone).
type FeedItem struct {
	Post   Post
	Author Profile
	By     string // handle of the reposter, empty for original posts
	URI    string // the post's AT URI
	CID    string
	Root   PostRef // thread root when the post is a reply
	Parent PostRef // the post it answers, when it is a reply
	Like   string  // AT URI of your like of this post, if you liked it

	RepostedAt string // when By reposted it (RFC 3339), for reposts
	Stats      PostStats
}

// PostStats are a post's engagement counts as the server reported them.
type PostStats struct {
	Likes   int `json:"likeCount"`
	Reposts int `json:"repostCount"`
	Replies int `json:"replyCount"`
	Quotes  int `json:"quoteCount"`
}

// ReplyTo is the reply reference for answering this post.
func (it FeedItem) ReplyTo() *ReplyRef {
	parent := PostRef{it.URI, it.CID}
	root := it.Root
	if root.URI == "" {
		root = parent
	}
	return &ReplyRef{Root: root, Parent: parent}
}

// Key identifies a timeline entry: the same post can appear once per reposter.
func (it FeedItem) Key() string { return it.URI + "|" + it.By }

// GetTimeline returns one page of your home timeline (accounts you follow),
// newest first, plus the cursor for the next page ("" when there is none).
func (c *Client) GetTimeline(ctx context.Context, cursor string, limit int) ([]FeedItem, string, error) {
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	entries, next, err := c.feedPage(ctx, "app.bsky.feed.getTimeline", q)
	if err != nil {
		return nil, "", err
	}
	items := make([]FeedItem, 0, len(entries))
	for _, e := range entries {
		it := e.item()
		if e.isRepost() {
			it.By, it.RepostedAt = e.Reason.By.Handle, e.Reason.IndexedAt
			it.Post.Repost = false // this is the original post; By says who reposted it
		}
		items = append(items, it)
	}
	return items, next, nil
}

// GetOwnPosts returns one page of your own posts and replies (not your reposts
// of other people's posts), newest first, plus the cursor for the next page.
func (c *Client) GetOwnPosts(ctx context.Context, cursor string, limit int) ([]FeedItem, string, error) {
	q := url.Values{"actor": {c.DID}, "limit": {fmt.Sprint(limit)}, "filter": {"posts_with_replies"}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	entries, next, err := c.feedPage(ctx, "app.bsky.feed.getAuthorFeed", q)
	if err != nil {
		return nil, "", err
	}
	items := make([]FeedItem, 0, len(entries))
	for _, e := range entries {
		if e.isRepost() {
			continue
		}
		items = append(items, e.item())
	}
	return items, next, nil
}

// Repost is a post someone you follow reposted, with its original author.
type Repost struct {
	Item   FeedItem // the reposted post itself, for liking or replying to it
	Post   Post
	Author Profile // original author
	By     Profile // who reposted it
}

// GetTimelineReposts scans up to pages pages (100 items each) of your home
// timeline, newest first, and returns the reposts in it.
func (c *Client) GetTimelineReposts(ctx context.Context, pages int) ([]Repost, error) {
	var reposts []Repost
	cursor := ""
	for i := 0; i < max(pages, 1); i++ {
		q := url.Values{"limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		entries, next, err := c.feedPage(ctx, "app.bsky.feed.getTimeline", q)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.isRepost() {
				reposts = append(reposts, Repost{Item: e.item(), Post: e.toPost(), Author: e.Post.Author, By: e.Reason.By})
			}
		}
		if next == "" || len(entries) == 0 {
			break
		}
		cursor = next
	}
	return reposts, nil
}

// RKey is the record key at the end of an at:// URI.
func RKey(uri string) string { return uri[strings.LastIndex(uri, "/")+1:] }

// ProfileURL is an account's bsky.app page; actor is a handle or a DID.
func ProfileURL(actor string) string { return "https://bsky.app/profile/" + actor }

// PostURL turns at://did/app.bsky.feed.post/rkey into its bsky.app link.
func PostURL(uri string) string {
	if strings.HasPrefix(uri, "at://") {
		if parts := strings.Split(strings.TrimPrefix(uri, "at://"), "/"); len(parts) == 3 {
			return ProfileURL(parts[0]) + "/post/" + parts[2]
		}
	}
	return uri
}

// PostURL is the post's bsky.app address, using the author's handle when known.
func (it FeedItem) PostURL() string {
	if it.Author.Handle == "" {
		return PostURL(it.URI)
	}
	return ProfileURL(it.Author.Handle) + "/post/" + RKey(it.URI)
}
