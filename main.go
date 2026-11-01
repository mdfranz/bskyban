package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

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
	st, err := LoadState(statePath)
	if err != nil {
		return fail(err)
	}
	ctx := context.Background()
	c := NewClient(pds)
	if err := c.Login(ctx, handle, pw); err != nil {
		return fail(fmt.Errorf("login: %w", err))
	}
	a := &app{c: c, st: st, stdin: os.Stdin, stdout: os.Stdout}

	if cmd == "review" {
		fs := newFlagSet("review")
		since := fs.Int("since", 0, "")
		backlog := fs.Bool("backlog", false, "")
		if err := fs.Parse(args); err != nil {
			return fail(fmt.Errorf("%w: %v", errUsage, err))
		}
		err = runTUI(a, *since, *backlog)
	} else {
		err = a.run(ctx, cmd, args)
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	if errors.Is(err, errUsage) {
		fmt.Fprint(os.Stderr, "\n", usageText)
		return 2
	}
	return 1
}
