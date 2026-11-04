// Package tui is the interactive terminal UI: the timeline and repost review, your
// own posts, and the follower review.
package tui

import (
	"context"
	"time"

	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
)

// Options configure a terminal UI.
type Options struct {
	Theme   Theme
	Refresh time.Duration // how often to check for new posts; 0 turns checking off
	Fancy   bool          // highlight newly inserted posts
	Backlog bool          // also offer accounts you skipped before
}

// RunFeed shows your timeline and the authors of reposts you have not handled.
// pages is how many timeline pages (100 items each) to scan for reposts.
func RunFeed(a *app.App, pages int, o Options) error {
	applyTheme(o.Theme)
	fm := newFeedModel(a,
		func(ctx context.Context) ([]bsky.Profile, map[string]app.RepostCtx, error) {
			return a.FeedCandidates(ctx, pages, o.Backlog)
		},
		func(ctx context.Context, cursor string) ([]bsky.FeedItem, string, error) {
			return a.Client.GetTimeline(ctx, cursor, 50)
		})
	fm.refresh, fm.backlog, fm.fancy = o.Refresh, o.Backlog, o.Fancy
	return runTUI(fm)
}

// RunPosts shows your own posts.
func RunPosts(a *app.App, o Options) error {
	applyTheme(o.Theme)
	pm := newPostsModel(a, func(ctx context.Context, cursor string) ([]bsky.FeedItem, string, error) {
		return a.Client.GetOwnPosts(ctx, cursor, 50)
	})
	pm.refresh, pm.fancy = o.Refresh, o.Fancy
	return runTUI(pm)
}

// RunReview walks through new followers one at a time. since limits it to the
// newest N followers (0 for all).
func RunReview(a *app.App, since int, o Options) error {
	applyTheme(o.Theme)
	return runTUI(newModel(a, "review", func(ctx context.Context) ([]bsky.Profile, map[string]app.RepostCtx, error) {
		l, err := a.Followers(ctx, since, false, o.Backlog)
		return l, nil, err
	}))
}
