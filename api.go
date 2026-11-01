package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const blockCollection = "app.bsky.graph.block"

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

// Age returns the account age, or 0 if the creation time is unknown.
func (p Profile) Age() time.Duration {
	t, err := time.Parse(time.RFC3339, p.CreatedAt)
	if err != nil {
		return 0
	}
	return time.Since(t)
}

type Client struct {
	base string
	http *http.Client
	jwt  string
	DID  string
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
			return fmt.Errorf("%s: %s (%s)", nsid, e.Message, e.Error)
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
	}
	err := c.do(ctx, http.MethodPost, "com.atproto.server.createSession", nil,
		map[string]string{"identifier": identifier, "password": password}, &out)
	if err != nil {
		return err
	}
	c.jwt, c.DID = out.AccessJwt, out.DID
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
	var out struct {
		Feed []struct {
			Post struct {
				Record struct {
					Text      string          `json:"text"`
					CreatedAt string          `json:"createdAt"`
					Reply     json.RawMessage `json:"reply"`
					Embed     json.RawMessage `json:"embed"`
					Facets    []facet         `json:"facets"`
				} `json:"record"`
			} `json:"post"`
			Reason struct {
				Type string `json:"$type"`
			} `json:"reason"`
		} `json:"feed"`
	}
	q := url.Values{"actor": {actor}, "limit": {fmt.Sprint(limit)}, "filter": {"posts_with_replies"}}
	if err := c.do(ctx, http.MethodGet, "app.bsky.feed.getAuthorFeed", q, nil, &out); err != nil {
		return nil, err
	}
	posts := make([]Post, 0, len(out.Feed))
	for _, f := range out.Feed {
		posts = append(posts, Post{
			Text:      expandLinks(f.Post.Record.Text, f.Post.Record.Facets),
			CreatedAt: f.Post.Record.CreatedAt,
			Repost:    strings.HasSuffix(f.Reason.Type, "reasonRepost"),
			Reply:     len(f.Post.Record.Reply) > 0 && string(f.Post.Record.Reply) != "null",
			Attach:    describeEmbed(f.Post.Record.Embed),
		})
	}
	return posts, nil
}

// Block creates a block record and returns its AT URI.
func (c *Client) Block(ctx context.Context, did string) (string, error) {
	var out struct {
		URI string `json:"uri"`
	}
	err := c.do(ctx, http.MethodPost, "com.atproto.repo.createRecord", nil, map[string]any{
		"repo":       c.DID,
		"collection": blockCollection,
		"record": map[string]any{
			"$type":     blockCollection,
			"subject":   did,
			"createdAt": time.Now().UTC().Format(time.RFC3339),
		},
	}, &out)
	return out.URI, err
}

// Unblock deletes the block record identified by its AT URI
// (at://did/app.bsky.graph.block/rkey).
func (c *Client) Unblock(ctx context.Context, blockURI string) error {
	i := strings.LastIndex(blockURI, "/")
	if i < 0 {
		return fmt.Errorf("bad block uri %q", blockURI)
	}
	return c.do(ctx, http.MethodPost, "com.atproto.repo.deleteRecord", nil, map[string]any{
		"repo":       c.DID,
		"collection": blockCollection,
		"rkey":       blockURI[i+1:],
	}, nil)
}
