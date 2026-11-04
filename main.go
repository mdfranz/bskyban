package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
	"github.com/mdfranz/bskyban/internal/termtext"
)

// minRefresh is the shortest allowed interval between background checks.
const minRefresh = 10 * time.Second

func main() {
	os.Exit(realMain())
}

func realMain() int {
	args := os.Args[1:]
	cmd := "feed" // what plain `bskyban` runs
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
	if cmd == "themes" {
		if len(args) != 0 {
			return fail(fmt.Errorf("%w: themes takes no arguments", errUsage))
		}
		if err := cmdThemes(os.Stdout); err != nil {
			return fail(err)
		}
		return 0
	}

	var opts tuiOptions
	if cmd == "review" || cmd == "feed" || cmd == "posts" {
		var err error
		opts, err = parseTUIOptions(cmd, args)
		if err != nil {
			return fail(err)
		}
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
		if statePath, err = store.DefaultStatePath(); err != nil {
			return fail(err)
		}
	}
	auditPath := os.Getenv("BSKYBAN_AUDIT")
	if auditPath == "" {
		var err error
		if auditPath, err = store.DefaultAuditPath(); err != nil {
			return fail(err)
		}
	}
	st, err := store.LoadState(statePath)
	if err != nil {
		return fail(err)
	}
	ctx := context.Background()
	c := bsky.NewClient(pds)
	if err := c.Login(ctx, handle, pw); err != nil {
		return fail(fmt.Errorf("login: %w", err))
	}
	a := &cliApp{App: &app.App{Client: c, State: st, Audit: store.NewAudit(auditPath)}, stdin: os.Stdin, stdout: os.Stdout}

	switch cmd {
	case "feed":
		if opts.list {
			err = a.cmdFeedList(ctx, opts.pages, opts.backlog, opts.asJSON)
		} else {
			applyTheme(opts.theme)
			fm := newFeedModel(a.App,
				func(ctx context.Context) ([]bsky.Profile, map[string]app.RepostCtx, error) {
					return a.FeedCandidates(ctx, opts.pages, opts.backlog)
				},
				func(ctx context.Context, cursor string) ([]bsky.FeedItem, string, error) {
					return a.Client.GetTimeline(ctx, cursor, 50)
				})
			fm.refresh, fm.backlog = opts.refresh, opts.backlog
			fm.fancy = opts.fancy || envOn("BSKYBAN_FANCY")
			err = runTUI(fm)
		}
	case "posts":
		if opts.list || opts.asJSON {
			err = a.cmdPostsList(ctx, opts.n, opts.asJSON)
		} else {
			applyTheme(opts.theme)
			pm := newPostsModel(a.App, func(ctx context.Context, cursor string) ([]bsky.FeedItem, string, error) {
				return a.Client.GetOwnPosts(ctx, cursor, 50)
			})
			pm.refresh = opts.refresh
			pm.fancy = opts.fancy || envOn("BSKYBAN_FANCY")
			err = runTUI(pm)
		}
	case "review":
		applyTheme(opts.theme)
		err = runTUI(newModel(a.App, "review", func(ctx context.Context) ([]bsky.Profile, map[string]app.RepostCtx, error) {
			l, err := a.Followers(ctx, opts.since, false, opts.backlog)
			return l, nil, err
		}))
	default:
		err = a.run(ctx, cmd, args)
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

type tuiOptions struct {
	pages, n, since              int
	backlog, list, asJSON, fancy bool
	refresh                      time.Duration
	theme                        Theme
}

func themeFlag(fs *flag.FlagSet) *string {
	return fs.String("theme", "", "colour theme (default BSKYBAN_THEME or auto)")
}

// Parse before login so usage errors never require credentials or network access.
func parseTUIOptions(cmd string, args []string) (tuiOptions, error) {
	var opts tuiOptions
	fs := newFlagSet(cmd)
	name := themeFlag(fs)
	if cmd == "review" {
		fs.IntVar(&opts.since, "since", 0, "")
	} else {
		fs.BoolVar(&opts.list, "list", false, "")
		fs.BoolVar(&opts.asJSON, "json", false, "")
		fs.DurationVar(&opts.refresh, "refresh", 30*time.Second, "")
		fs.BoolVar(&opts.fancy, "fancy", false, "")
	}
	if cmd == "posts" {
		fs.IntVar(&opts.n, "n", 20, "")
	} else {
		fs.BoolVar(&opts.backlog, "backlog", false, "")
	}
	if cmd == "feed" {
		fs.IntVar(&opts.pages, "pages", 3, "")
	}
	if err := fs.Parse(args); err != nil {
		return opts, fmt.Errorf("%w: %v", errUsage, err)
	}
	if opts.refresh != 0 && opts.refresh < minRefresh {
		return opts, fmt.Errorf("%w: --refresh must be 0 (off) or at least %s", errUsage, minRefresh)
	}
	// Printed lists do not use themes, including the environment default.
	if !opts.list && (cmd != "posts" || !opts.asJSON) {
		var err error
		opts.theme, err = resolveTheme(*name, os.Getenv("BSKYBAN_THEME"))
		if err != nil {
			return opts, err
		}
	} else if *name != "" {
		if _, err := themeByName(*name); err != nil {
			return opts, err
		}
	}
	return opts, nil
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
	fmt.Fprintln(os.Stderr, "error:", termtext.Sanitize(err.Error()))
	if errors.Is(err, errUsage) {
		fmt.Fprint(os.Stderr, "\n", usageText)
		return 2
	}
	return 1
}
