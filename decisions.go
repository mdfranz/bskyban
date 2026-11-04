package main

import (
	"context"

	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
)

// isRemote reports whether a decision leaves a record on your account (so that
// changing it later means deleting that record).
func isRemote(decision string) bool { return decision == store.Blocked || decision == store.Followed }

// createFor performs the remote side of a block or follow decision.
func createFor(ctx context.Context, c *bsky.Client, decision, did string) (string, error) {
	if decision == store.Followed {
		return c.Follow(ctx, did)
	}
	return c.Block(ctx, did)
}

// deleteFor undoes the remote side of a block or follow decision.
func deleteFor(ctx context.Context, c *bsky.Client, decision, uri string) error {
	if decision == store.Followed {
		return c.Unfollow(ctx, uri)
	}
	return c.Unblock(ctx, uri)
}

// viewerURI is the record behind a decision, as reported by the server for p.
func viewerURI(p *bsky.Profile, decision string) string {
	if decision == store.Followed {
		return p.Viewer.Following
	}
	return p.Viewer.Blocking
}
