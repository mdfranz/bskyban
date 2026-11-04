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
	"github.com/mdfranz/bskyban/internal/cli"
	"github.com/mdfranz/bskyban/internal/store"
	"github.com/mdfranz/bskyban/internal/termtext"
	"github.com/mdfranz/bskyban/internal/tui"
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
		fmt.Print(cli.UsageText)
		return 0
	}
	if cmd == "-h" || cmd == "--help" {
		fmt.Print(cli.UsageText)
		return 0
	}

	if cmd == "log" {
		if err := cli.RunLog(args, os.Stdout); err != nil {
			return fail(err)
		}
		return 0
	}
	if cmd == "themes" {
		if len(args) != 0 {
			return fail(fmt.Errorf("%w: themes takes no arguments", cli.ErrUsage))
		}
		if err := tui.PrintThemes(os.Stdout); err != nil {
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
	a := &app.App{Client: c, State: st, Audit: store.NewAudit(auditPath)}
	topts := tui.Options{Theme: opts.theme, Refresh: opts.refresh, Fancy: opts.fancy || envOn("BSKYBAN_FANCY"), Backlog: opts.backlog}

	switch cmd {
	case "feed":
		if opts.list {
			err = cli.New(a, os.Stdin, os.Stdout).FeedList(ctx, opts.pages, opts.backlog, opts.asJSON)
		} else {
			err = tui.RunFeed(a, opts.pages, topts)
		}
	case "posts":
		if opts.list || opts.asJSON {
			err = cli.New(a, os.Stdin, os.Stdout).PostsList(ctx, opts.n, opts.asJSON)
		} else {
			err = tui.RunPosts(a, topts)
		}
	case "review":
		err = tui.RunReview(a, opts.since, topts)
	default:
		err = cli.New(a, os.Stdin, os.Stdout).Run(ctx, cmd, args)
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
	theme                        tui.Theme
}

func themeFlag(fs *flag.FlagSet) *string {
	return fs.String("theme", "", "colour theme (default BSKYBAN_THEME or auto)")
}

// Parse before login so usage errors never require credentials or network access.
func parseTUIOptions(cmd string, args []string) (tuiOptions, error) {
	var opts tuiOptions
	fs := cli.NewFlagSet(cmd)
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
		return opts, fmt.Errorf("%w: %v", cli.ErrUsage, err)
	}
	if opts.refresh != 0 && opts.refresh < minRefresh {
		return opts, fmt.Errorf("%w: --refresh must be 0 (off) or at least %s", cli.ErrUsage, minRefresh)
	}
	// Printed lists do not use themes, including the environment default.
	if !opts.list && (cmd != "posts" || !opts.asJSON) {
		var err error
		opts.theme, err = tui.ResolveTheme(*name, os.Getenv("BSKYBAN_THEME"))
		if err != nil {
			return opts, usageIfUnknownTheme(err)
		}
	} else if *name != "" {
		if _, err := tui.ThemeByName(*name); err != nil {
			return opts, usageIfUnknownTheme(err)
		}
	}
	return opts, nil
}

// usageIfUnknownTheme reports a bad theme name as a usage error (exit status 2).
func usageIfUnknownTheme(err error) error {
	if errors.Is(err, tui.ErrUnknownTheme) {
		return fmt.Errorf("%w: %v", cli.ErrUsage, err)
	}
	return err
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
	if errors.Is(err, cli.ErrUsage) {
		fmt.Fprint(os.Stderr, "\n", cli.UsageText)
		return 2
	}
	return 1
}
