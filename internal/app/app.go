// Package app holds the logic the terminal UI and the subcommands share: which
// followers and repost authors still need a decision, and how a decision maps to
// a record on your account.
package app

import (
	"context"
	"slices"

	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
)

// App is a logged-in client with the local decisions and audit log it works against.
type App struct {
	Client *bsky.Client
	State  *store.State
	Audit  *store.Audit
}

// Followers returns followers (newest first) you don't follow back and have no
// recorded decision for (new ones),
// plus skipped ones when backlog is true, or every follower when all is true. since > 0 limits to the newest N followers.
func (a *App) Followers(ctx context.Context, since int, all, backlog bool) ([]bsky.Profile, error) {
	fs, err := a.Client.GetFollowers(ctx, since)
	if err != nil {
		return nil, err
	}
	if all {
		return fs, nil
	}
	var out []bsky.Profile
	for _, f := range fs {
		if a.IsNew(f, backlog) {
			out = append(out, f)
		}
	}
	return out, nil
}

// IsNew reports whether p is someone you don't follow, haven't blocked, and have
// no recorded decision for (skipped ones count when backlog is true).
func (a *App) IsNew(p bsky.Profile, backlog bool) bool {
	if p.Viewer.Following != "" || p.Viewer.Blocking != "" || p.DID == a.Client.DID {
		return false
	}
	e, done := a.State.Entries[p.DID]
	return !done || (backlog && e.Decision == store.Skipped)
}

// RepostCtx records how a candidate reached your timeline.
type RepostCtx struct {
	Post bsky.Post
	Item bsky.FeedItem // the reposted post, for liking or replying to it
	By   []string      // handles of people you follow who reposted them
}

// FeedCandidates returns the original authors of reposts in your timeline that
// are new to you, in order of first appearance (newest first), each once.
func (a *App) FeedCandidates(ctx context.Context, pages int, backlog bool) ([]bsky.Profile, map[string]RepostCtx, error) {
	reposts, err := a.Client.GetTimelineReposts(ctx, pages)
	if err != nil {
		return nil, nil, err
	}
	var out []bsky.Profile
	rc := map[string]RepostCtx{}
	for _, r := range reposts {
		if !a.IsNew(r.Author, backlog) {
			continue
		}
		cur, seen := rc[r.Author.DID]
		if !seen {
			out = append(out, r.Author)
			cur.Post, cur.Item = r.Post, r.Item
			cur.Post.Repost = false // shown as the original post, under "Reposted by"
		}
		if !slices.Contains(cur.By, r.By.Handle) {
			cur.By = append(cur.By, r.By.Handle)
		}
		rc[r.Author.DID] = cur
	}
	return out, rc, nil
}

// IsRemote reports whether a decision leaves a record on your account (so that
// changing it later means deleting that record).
func IsRemote(decision string) bool { return decision == store.Blocked || decision == store.Followed }

// CreateFor performs the remote side of a block or follow decision.
func CreateFor(ctx context.Context, c *bsky.Client, decision, did string) (string, error) {
	if decision == store.Followed {
		return c.Follow(ctx, did)
	}
	return c.Block(ctx, did)
}

// DeleteFor undoes the remote side of a block or follow decision.
func DeleteFor(ctx context.Context, c *bsky.Client, decision, uri string) error {
	if decision == store.Followed {
		return c.Unfollow(ctx, uri)
	}
	return c.Unblock(ctx, uri)
}

// ViewerURI is the record behind a decision, as reported by the server for p.
func ViewerURI(p *bsky.Profile, decision string) string {
	if decision == store.Followed {
		return p.Viewer.Following
	}
	return p.Viewer.Blocking
}
