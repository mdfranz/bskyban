package bsky

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/mdfranz/bskyban/internal/termtext"
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
		Blocking   string `json:"blocking"`
		Following  string `json:"following"`  // set if you follow them
		FollowedBy string `json:"followedBy"` // set if they follow you
	} `json:"viewer"`
}

func (p *Profile) UnmarshalJSON(data []byte) error {
	type plain Profile
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = Profile(decoded)
	p.DisplayName = termtext.Sanitize(p.DisplayName)
	p.Description = termtext.Sanitize(p.Description)
	p.Handle = termtext.Sanitize(p.Handle)
	p.DID = termtext.Sanitize(p.DID)
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
