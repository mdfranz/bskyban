package bsky

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/rivo/uniseg"
)

const (
	blockCollection  = "app.bsky.graph.block"
	followCollection = "app.bsky.graph.follow"
	PostCollection   = "app.bsky.feed.post"
	likeCollection   = "app.bsky.feed.like"
	repostCollection = "app.bsky.feed.repost"
)

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

// MaxPostGraphemes is Bluesky's post length limit.
const MaxPostGraphemes = 300

// ValidatePostText returns an error when text is longer than Bluesky allows.
func ValidatePostText(text string) error {
	if n := uniseg.GraphemeClusterCount(text); n > MaxPostGraphemes {
		return fmt.Errorf("post is %d characters; the limit is %d", n, MaxPostGraphemes)
	}
	return nil
}

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
func (c *Client) Post(ctx context.Context, text string, reply *ReplyRef) (string, error) {
	return c.PostWith(ctx, text, PostOptions{Reply: reply})
}

// PostOptions are the optional parts of a post.
type PostOptions struct {
	Reply *ReplyRef // makes the post an answer in that thread
	Quote *PostRef  // embeds this post, which is how Bluesky quote-posts with a comment
}

// PostWith publishes a text post with the given options and returns its AT URI.
func (c *Client) PostWith(ctx context.Context, text string, opts PostOptions) (string, error) {
	rec := map[string]any{
		"$type":     PostCollection,
		"text":      text,
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}
	if opts.Reply != nil {
		rec["reply"] = opts.Reply
	}
	if opts.Quote != nil {
		rec["embed"] = map[string]any{"$type": "app.bsky.embed.record", "record": opts.Quote}
	}
	if f := linkFacets(text); len(f) > 0 {
		rec["facets"] = f
	}
	var out struct {
		URI string `json:"uri"`
	}
	err := c.do(ctx, http.MethodPost, "com.atproto.repo.createRecord", nil, map[string]any{
		"repo": c.DID, "collection": PostCollection, "record": rec,
	}, &out)
	return out.URI, err
}

// Repost shares a post to your followers, without a comment, and returns the AT
// URI of the repost record.
func (c *Client) Repost(ctx context.Context, post PostRef) (string, error) {
	var out struct {
		URI string `json:"uri"`
	}
	err := c.do(ctx, http.MethodPost, "com.atproto.repo.createRecord", nil, map[string]any{
		"repo":       c.DID,
		"collection": repostCollection,
		"record": map[string]any{
			"$type":     repostCollection,
			"subject":   post,
			"createdAt": time.Now().UTC().Format(time.RFC3339),
		},
	}, &out)
	return out.URI, err
}

// DeletePost deletes the post at uri from your account.
func (c *Client) DeletePost(ctx context.Context, uri string) error {
	return c.deleteRecord(ctx, PostCollection, uri)
}

// Like likes a post and returns the AT URI of the like record.
func (c *Client) Like(ctx context.Context, post PostRef) (string, error) {
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
func (c *Client) GetPost(ctx context.Context, uri string) (PostRef, string, error) {
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
		return PostRef{}, "", err
	}
	if len(out.Posts) == 0 {
		return PostRef{}, "", errors.New("post not found")
	}
	p := out.Posts[0]
	return PostRef{p.URI, p.CID}, p.Viewer.Like, nil
}

// deleteRecord removes the record at uri (at://did/collection/rkey) from your repo.
func (c *Client) deleteRecord(ctx context.Context, collection, uri string) error {
	if !strings.Contains(uri, "/") {
		return fmt.Errorf("bad record uri %q", uri)
	}
	return c.do(ctx, http.MethodPost, "com.atproto.repo.deleteRecord", nil, map[string]any{
		"repo":       c.DID,
		"collection": collection,
		"rkey":       RKey(uri),
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
