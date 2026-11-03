package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	blockCollection  = "app.bsky.graph.block"
	followCollection = "app.bsky.graph.follow"
	postCollection   = "app.bsky.feed.post"
	likeCollection   = "app.bsky.feed.like"
)

// Profile is the subset of app.bsky.actor.defs#profileView(Detailed) we use.
type Profile struct {
	DID            string `json:"did"`
	Handle         string `json:"handle"`
	DisplayName    string `json:"displayName"`
	Description    string `json:"description"`
	Avatar         string `json:"avatar"`
	CreatedAt      string `json:"createdAt"`
	FollowersCount int    `json:"followersCount"`
	FollowsCount   int    `json:"followsCount"`
	PostsCount     int    `json:"postsCount"`
	Viewer         struct {
		Blocking  string `json:"blocking"`
		Following string `json:"following"` // set if you follow them
	} `json:"viewer"`
}

func (p *Profile) UnmarshalJSON(data []byte) error {
	type plain Profile
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = Profile(decoded)
	p.DisplayName = terminalText(p.DisplayName)
	p.Description = terminalText(p.Description)
	p.Handle = terminalText(p.Handle)
	p.DID = terminalText(p.DID)
	return nil
}

// Age returns the account age, or 0 if the creation time is unknown.
func (p Profile) Age() time.Duration {
	t, err := time.Parse(time.RFC3339, p.CreatedAt)
	if err != nil {
		return 0
	}
	return time.Since(t)
}

type Client struct {
	base   string
	http   *http.Client
	jwt    string
	DID    string
	Handle string // your handle as the server reports it
}

func NewClient(base string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, nsid string, query url.Values, body, out any) error {
	u := c.base + "/xrpc/" + nsid
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.jwt != "" {
		req.Header.Set("Authorization", "Bearer "+c.jwt)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error, Message string }
		_ = json.Unmarshal(data, &e)
		if e.Error != "" {
			return fmt.Errorf("%s: %s (%s)", nsid, terminalText(e.Message), terminalText(e.Error))
		}
		return fmt.Errorf("%s: HTTP %d", nsid, resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) Login(ctx context.Context, identifier, password string) error {
	var out struct {
		AccessJwt string `json:"accessJwt"`
		DID       string `json:"did"`
		Handle    string `json:"handle"`
	}
	err := c.do(ctx, http.MethodPost, "com.atproto.server.createSession", nil,
		map[string]string{"identifier": identifier, "password": password}, &out)
	if err != nil {
		return err
	}
	c.jwt, c.DID, c.Handle = out.AccessJwt, out.DID, out.Handle
	return nil
}

// GetFollowers returns followers of the logged-in account, newest first.
// limit <= 0 means all of them.
func (c *Client) GetFollowers(ctx context.Context, limit int) ([]Profile, error) {
	var all []Profile
	cursor := ""
	for {
		q := url.Values{"actor": {c.DID}, "limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var out struct {
			Followers []Profile `json:"followers"`
			Cursor    string    `json:"cursor"`
		}
		if err := c.do(ctx, http.MethodGet, "app.bsky.graph.getFollowers", q, nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Followers...)
		if out.Cursor == "" || len(out.Followers) == 0 || (limit > 0 && len(all) >= limit) {
			break
		}
		cursor = out.Cursor
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// GetProfile accepts a handle or DID.
func (c *Client) GetProfile(ctx context.Context, actor string) (*Profile, error) {
	var p Profile
	err := c.do(ctx, http.MethodGet, "app.bsky.actor.getProfile", url.Values{"actor": {actor}}, nil, &p)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProfiles fetches detailed profiles for DIDs/handles in batches of 25.
func (c *Client) GetProfiles(ctx context.Context, actors []string) ([]Profile, error) {
	var all []Profile
	for i := 0; i < len(actors); i += 25 {
		end := min(i+25, len(actors))
		var out struct {
			Profiles []Profile `json:"profiles"`
		}
		q := url.Values{"actors": actors[i:end]}
		if err := c.do(ctx, http.MethodGet, "app.bsky.actor.getProfiles", q, nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Profiles...)
	}
	return all, nil
}

// Post is a recent post (or repost) in an account's feed.
type Post struct {
	Text      string
	CreatedAt string
	Repost    bool
	Reply     bool
	Attach    []string // one-line descriptions of images, links, video, quotes

	// Only set for posts that come with hydrated context (timeline / author feeds).
	Quote        string   // "@handle: text" of a quoted post
	Images       []string // full-size image URLs
	ParentHandle string   // for replies: who is being replied to
	ParentText   string
}

type facet struct {
	Index struct {
		ByteStart int `json:"byteStart"`
		ByteEnd   int `json:"byteEnd"`
	} `json:"index"`
	Features []struct {
		Type string `json:"$type"`
		URI  string `json:"uri"`
	} `json:"features"`
}

// expandLinks replaces the shortened link text clients store in a post
// ("www.example.com/some-pa...") with the real URL from the link facet.
func expandLinks(text string, facets []facet) string {
	type rep struct {
		start, end int
		uri        string
	}
	var reps []rep
	for _, f := range facets {
		for _, ft := range f.Features {
			if strings.HasSuffix(ft.Type, "#link") && ft.URI != "" &&
				f.Index.ByteStart >= 0 && f.Index.ByteStart < f.Index.ByteEnd && f.Index.ByteEnd <= len(text) {
				reps = append(reps, rep{f.Index.ByteStart, f.Index.ByteEnd, ft.URI})
			}
		}
	}
	sort.Slice(reps, func(i, j int) bool { return reps[i].start > reps[j].start })
	for _, r := range reps { // back to front so earlier offsets stay valid
		text = text[:r.start] + r.uri + text[r.end:]
	}
	return text
}

// describeEmbed turns a raw post embed into short one-line descriptions.
func describeEmbed(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var e struct {
		Type     string                 `json:"$type"`
		Images   []struct{ Alt string } `json:"images"`
		Alt      string                 `json:"alt"`
		External struct {
			URI, Title string
		} `json:"external"`
		Record json.RawMessage `json:"record"`
		Media  json.RawMessage `json:"media"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	var out []string
	switch {
	case strings.HasPrefix(e.Type, "app.bsky.embed.images"):
		s := fmt.Sprintf("[%d image", len(e.Images))
		if len(e.Images) != 1 {
			s += "s"
		}
		s += "]"
		for _, im := range e.Images {
			if alt := strings.Join(strings.Fields(im.Alt), " "); alt != "" {
				s += " alt: " + alt
				break
			}
		}
		out = append(out, s)
	case strings.HasPrefix(e.Type, "app.bsky.embed.video"):
		s := "[video]"
		if alt := strings.Join(strings.Fields(e.Alt), " "); alt != "" {
			s += " alt: " + alt
		}
		out = append(out, s)
	case strings.HasPrefix(e.Type, "app.bsky.embed.external"):
		s := "[link]"
		if e.External.Title != "" {
			s += " " + e.External.Title
		}
		out = append(out, s+" "+e.External.URI)
	case e.Type == "app.bsky.embed.recordWithMedia":
		out = append(out, describeEmbed(e.Media)...)
		out = append(out, "[quote post]")
	case strings.HasPrefix(e.Type, "app.bsky.embed.record"):
		out = append(out, "[quote post]")
	}
	return out
}

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
		URI    string `json:"uri"`
		CID    string `json:"cid"`
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
			Facets    []facet         `json:"facets"`
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
		Type string  `json:"$type"`
		By   Profile `json:"by"`
	} `json:"reason"`
}

// viewContext pulls a quoted post and image URLs out of a hydrated embed view.
func viewContext(raw json.RawMessage) (quote string, images []string) {
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
		_, mi := viewContext(v.Media)
		images = append(images, mi...)
	}
	if len(v.Record) > 0 {
		var r struct {
			Author Profile `json:"author"`
			Value  struct {
				Text   string  `json:"text"`
				Facets []facet `json:"facets"`
			} `json:"value"`
			Record json.RawMessage `json:"record"` // recordWithMedia nests one level deeper
		}
		if json.Unmarshal(v.Record, &r) == nil {
			if r.Author.Handle == "" && len(r.Record) > 0 {
				_ = json.Unmarshal(r.Record, &r)
			}
			if r.Author.Handle != "" {
				quote = "@" + r.Author.Handle + ": " + expandLinks(r.Value.Text, r.Value.Facets)
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
		ParentHandle: e.Reply.Parent.Author.Handle,
		ParentText:   e.Reply.Parent.Record.Text,
	}
	p.Quote, p.Images = viewContext(e.Post.Embed)
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

// postRef identifies one post (strong reference: AT URI plus content hash).
type postRef struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

// replyRef is what a reply record needs to point at its place in a thread.
type replyRef struct {
	Root   postRef `json:"root"`
	Parent postRef `json:"parent"`
}

// threadRoot is the root of the thread the post belongs to, or zero if it is not a reply.
func (e feedEntry) threadRoot() postRef {
	var r replyRef
	_ = json.Unmarshal(e.Post.Record.Reply, &r)
	return r.Root
}

// item turns a feed entry into a timeline item.
func (e feedEntry) item() FeedItem {
	return FeedItem{Post: e.toPost(), Author: e.Post.Author, URI: e.Post.URI, CID: e.Post.CID, Root: e.threadRoot(), Like: e.Post.Viewer.Like}
}

// FeedItem is one timeline entry: a post, its author, and who reposted it (if anyone).
type FeedItem struct {
	Post   Post
	Author Profile
	By     string // handle of the reposter, empty for original posts
	URI    string // the post's AT URI
	CID    string
	Root   postRef // thread root when the post is a reply
	Like   string  // AT URI of your like of this post, if you liked it
}

// replyTo is the reply reference for answering this post.
func (it FeedItem) replyTo() *replyRef {
	parent := postRef{it.URI, it.CID}
	root := it.Root
	if root.URI == "" {
		root = parent
	}
	return &replyRef{Root: root, Parent: parent}
}

// key identifies a timeline entry: the same post can appear once per reposter.
func (it FeedItem) key() string { return it.URI + "|" + it.By }

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
			it.By = e.Reason.By.Handle
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
				reposts = append(reposts, Repost{Post: e.toPost(), Author: e.Post.Author, By: e.Reason.By})
			}
		}
		if next == "" || len(entries) == 0 {
			break
		}
		cursor = next
	}
	return reposts, nil
}

// createSubjectRecord creates an app.bsky.graph.<block|follow> record about did
// in your repo and returns its AT URI.
func (c *Client) createSubjectRecord(ctx context.Context, collection, did string) (string, error) {
	var out struct {
		URI string `json:"uri"`
	}
	err := c.do(ctx, http.MethodPost, "com.atproto.repo.createRecord", nil, map[string]any{
		"repo":       c.DID,
		"collection": collection,
		"record": map[string]any{
			"$type":     collection,
			"subject":   did,
			"createdAt": time.Now().UTC().Format(time.RFC3339),
		},
	}, &out)
	return out.URI, err
}

// maxPostGraphemes is Bluesky's post length limit.
const maxPostGraphemes = 300

var urlRE = regexp.MustCompile(`https?://[^\s]+`)

// linkFacets marks URLs in text as links (offsets are UTF-8 byte positions),
// since Bluesky doesn't linkify plain text on its own.
func linkFacets(text string) []map[string]any {
	var out []map[string]any
	for _, m := range urlRE.FindAllStringIndex(text, -1) {
		end := m[0] + len(strings.TrimRight(text[m[0]:m[1]], ".,;:!?)]\"'"))
		out = append(out, map[string]any{
			"index":    map[string]int{"byteStart": m[0], "byteEnd": end},
			"features": []map[string]string{{"$type": "app.bsky.richtext.facet#link", "uri": text[m[0]:end]}},
		})
	}
	return out
}

// Post publishes a text post to your account and returns its AT URI. A non-nil
// reply makes it an answer in that thread.
func (c *Client) Post(ctx context.Context, text string, reply *replyRef) (string, error) {
	rec := map[string]any{
		"$type":     postCollection,
		"text":      text,
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}
	if reply != nil {
		rec["reply"] = reply
	}
	if f := linkFacets(text); len(f) > 0 {
		rec["facets"] = f
	}
	var out struct {
		URI string `json:"uri"`
	}
	err := c.do(ctx, http.MethodPost, "com.atproto.repo.createRecord", nil, map[string]any{
		"repo": c.DID, "collection": postCollection, "record": rec,
	}, &out)
	return out.URI, err
}

// DeletePost deletes the post at uri from your account.
func (c *Client) DeletePost(ctx context.Context, uri string) error {
	return c.deleteRecord(ctx, postCollection, uri)
}

// Like likes a post and returns the AT URI of the like record.
func (c *Client) Like(ctx context.Context, post postRef) (string, error) {
	var out struct {
		URI string `json:"uri"`
	}
	err := c.do(ctx, http.MethodPost, "com.atproto.repo.createRecord", nil, map[string]any{
		"repo":       c.DID,
		"collection": likeCollection,
		"record": map[string]any{
			"$type":     likeCollection,
			"subject":   post,
			"createdAt": time.Now().UTC().Format(time.RFC3339),
		},
	}, &out)
	return out.URI, err
}

// Unlike deletes the like record identified by its AT URI.
func (c *Client) Unlike(ctx context.Context, likeURI string) error {
	return c.deleteRecord(ctx, likeCollection, likeURI)
}

// GetPost looks up one post: its reference (with CID) and your like of it, if any.
func (c *Client) GetPost(ctx context.Context, uri string) (postRef, string, error) {
	var out struct {
		Posts []struct {
			URI    string `json:"uri"`
			CID    string `json:"cid"`
			Viewer struct {
				Like string `json:"like"`
			} `json:"viewer"`
		} `json:"posts"`
	}
	if err := c.do(ctx, http.MethodGet, "app.bsky.feed.getPosts", url.Values{"uris": {uri}}, nil, &out); err != nil {
		return postRef{}, "", err
	}
	if len(out.Posts) == 0 {
		return postRef{}, "", errors.New("post not found")
	}
	p := out.Posts[0]
	return postRef{p.URI, p.CID}, p.Viewer.Like, nil
}

// deleteRecord removes the record at uri (at://did/collection/rkey) from your repo.
func (c *Client) deleteRecord(ctx context.Context, collection, uri string) error {
	i := strings.LastIndex(uri, "/")
	if i < 0 {
		return fmt.Errorf("bad record uri %q", uri)
	}
	return c.do(ctx, http.MethodPost, "com.atproto.repo.deleteRecord", nil, map[string]any{
		"repo":       c.DID,
		"collection": collection,
		"rkey":       uri[i+1:],
	}, nil)
}

// Block creates a block record and returns its AT URI.
func (c *Client) Block(ctx context.Context, did string) (string, error) {
	return c.createSubjectRecord(ctx, blockCollection, did)
}

// Unblock deletes the block record identified by its AT URI.
func (c *Client) Unblock(ctx context.Context, blockURI string) error {
	return c.deleteRecord(ctx, blockCollection, blockURI)
}

// Follow creates a follow record and returns its AT URI.
func (c *Client) Follow(ctx context.Context, did string) (string, error) {
	return c.createSubjectRecord(ctx, followCollection, did)
}

// Unfollow deletes the follow record identified by its AT URI.
func (c *Client) Unfollow(ctx context.Context, followURI string) error {
	return c.deleteRecord(ctx, followCollection, followURI)
}

// isRemote reports whether a decision leaves a record on your account (so that
// changing it later means deleting that record).
func isRemote(decision string) bool { return decision == decBlocked || decision == decFollowed }

// createFor performs the remote side of a block or follow decision.
func (c *Client) createFor(ctx context.Context, decision, did string) (string, error) {
	if decision == decFollowed {
		return c.Follow(ctx, did)
	}
	return c.Block(ctx, did)
}

// deleteFor undoes the remote side of a block or follow decision.
func (c *Client) deleteFor(ctx context.Context, decision, uri string) error {
	if decision == decFollowed {
		return c.Unfollow(ctx, uri)
	}
	return c.Unblock(ctx, uri)
}

// viewerURI is the record behind a decision, as reported by the server for p.
func viewerURI(p *Profile, decision string) string {
	if decision == decFollowed {
		return p.Viewer.Following
	}
	return p.Viewer.Blocking
}
