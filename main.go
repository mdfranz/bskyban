package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// minRefresh is the shortest allowed interval between background checks.
const minRefresh = 10 * time.Second

func main() {
	os.Exit(realMain())
}

func realMain() int {
	args := os.Args[1:]
	cmd := "review"
	if len(args) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	if cmd == "help" || (len(args) > 0 && (args[0] == "-h" || args[0] == "--help")) {
		fmt.Print(usageText)
		return 0
	}
	if cmd == "-h" || cmd == "--help" {
		fmt.Print(usageText)
		return 0
	}

	if cmd == "log" {
		if err := runLog(args, os.Stdout); err != nil {
			return fail(err)
		}
		return 0
	}

	handle, pw := os.Getenv("BSKY_HANDLE"), os.Getenv("BSKY_APP_PASSWORD")
	if handle == "" || pw == "" {
		fmt.Fprintln(os.Stderr, "BSKY_HANDLE and BSKY_APP_PASSWORD must be set")
		return 1
	}
	pds := os.Getenv("BSKY_PDS")
	if pds == "" {
		pds = "https://bsky.social"
	}
	statePath := os.Getenv("BSKYBAN_STATE")
	if statePath == "" {
		var err error
		if statePath, err = defaultStatePath(); err != nil {
			return fail(err)
		}
	}
	auditPath := os.Getenv("BSKYBAN_AUDIT")
	if auditPath == "" {
		var err error
		if auditPath, err = defaultAuditPath(); err != nil {
			return fail(err)
		}
	}
	st, err := LoadState(statePath)
	if err != nil {
		return fail(err)
	}
	ctx := context.Background()
	c := NewClient(pds)
	if err := c.Login(ctx, handle, pw); err != nil {
		return fail(fmt.Errorf("login: %w", err))
	}
	a := &app{c: c, st: st, audit: NewAudit(auditPath), stdin: os.Stdin, stdout: os.Stdout}

	if cmd == "feed" {
		fs := newFlagSet("feed")
		pages := fs.Int("pages", 3, "")
		backlog := fs.Bool("backlog", false, "")
		list := fs.Bool("list", false, "")
		asJSON := fs.Bool("json", false, "")
		refresh := fs.Duration("refresh", 30*time.Second, "")
		fancy := fs.Bool("fancy", false, "")
		if err := fs.Parse(args); err != nil {
			return fail(fmt.Errorf("%w: %v", errUsage, err))
		}
		if *refresh != 0 && *refresh < minRefresh {
			return fail(fmt.Errorf("%w: --refresh must be 0 (off) or at least %s", errUsage, minRefresh))
		}
		if *list {
			err = a.cmdFeedList(ctx, *pages, *backlog, *asJSON)
		} else {
			fm := newFeedModel(a,
				func(ctx context.Context) ([]Profile, map[string]repostCtx, error) {
					return a.feedCandidates(ctx, *pages, *backlog)
				},
				func(ctx context.Context, cursor string) ([]FeedItem, string, error) {
					return a.c.GetTimeline(ctx, cursor, 50)
				})
			fm.refresh, fm.backlog = *refresh, *backlog
			fm.fancy = *fancy || envOn("BSKYBAN_FANCY")
			err = runTUI(fm)
		}
	} else if cmd == "posts" {
		fs := newFlagSet("posts")
		list := fs.Bool("list", false, "")
		asJSON := fs.Bool("json", false, "")
		n := fs.Int("n", 20, "")
		refresh := fs.Duration("refresh", 30*time.Second, "")
		fancy := fs.Bool("fancy", false, "")
		if err := fs.Parse(args); err != nil {
			return fail(fmt.Errorf("%w: %v", errUsage, err))
		}
		if *refresh != 0 && *refresh < minRefresh {
			return fail(fmt.Errorf("%w: --refresh must be 0 (off) or at least %s", errUsage, minRefresh))
		}
		if *list || *asJSON {
			err = a.cmdPostsList(ctx, *n, *asJSON)
		} else {
			pm := newPostsModel(a, func(ctx context.Context, cursor string) ([]FeedItem, string, error) {
				return a.c.GetOwnPosts(ctx, cursor, 50)
			})
			pm.refresh = *refresh
			pm.fancy = *fancy || envOn("BSKYBAN_FANCY")
			err = runTUI(pm)
		}
	} else if cmd == "review" {
		fs := newFlagSet("review")
		since := fs.Int("since", 0, "")
		backlog := fs.Bool("backlog", false, "")
		if err := fs.Parse(args); err != nil {
			return fail(fmt.Errorf("%w: %v", errUsage, err))
		}
		err = runTUI(newModel(a, "review", func(ctx context.Context) ([]Profile, map[string]repostCtx, error) {
			l, err := a.followers(ctx, *since, false, *backlog)
			return l, nil, err
		}))
	} else {
		err = a.run(ctx, cmd, args)
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

// envOn reports whether the environment variable is set to 1, true or yes.
func envOn(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", terminalText(err.Error()))
	if errors.Is(err, errUsage) {
		fmt.Fprint(os.Stderr, "\n", usageText)
		return 2
	}
	return 1
}
