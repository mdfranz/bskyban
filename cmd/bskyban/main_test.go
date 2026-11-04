package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mdfranz/bskyban/internal/cli"
)

func TestEnvOn(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, "YES": true, " yes ": true, "": false, "0": false, "no": false} {
		t.Setenv("BSKYBAN_FANCY", v)
		if got := envOn("BSKYBAN_FANCY"); got != want {
			t.Errorf("%q: got %v", v, got)
		}
	}
}
func TestParseTUITheme(t *testing.T) {
	for _, cmd := range []string{"review", "feed", "posts"} {
		t.Run(cmd, func(t *testing.T) {
			t.Setenv("BSKYBAN_THEME", "catppuccin")
			opts, err := parseTUIOptions(cmd, nil)
			if err != nil || opts.theme.Name != "catppuccin" {
				t.Fatalf("env theme: %+v, %v", opts, err)
			}
			opts, err = parseTUIOptions(cmd, []string{"--theme", "ansi"})
			if err != nil || opts.theme.Name != "ansi" {
				t.Fatalf("flag theme: %+v, %v", opts, err)
			}
			if _, err := parseTUIOptions(cmd, []string{"--theme", "nope"}); !errors.Is(err, cli.ErrUsage) {
				t.Errorf("invalid theme: %v", err)
			}
			if cmd != "review" && opts.refresh != 30*time.Second {
				t.Errorf("refresh default changed: %s", opts.refresh)
			}
		})
	}
	t.Setenv("BSKYBAN_THEME", "nope")
	for _, cmd := range []string{"feed", "posts"} {
		if _, err := parseTUIOptions(cmd, []string{"--list"}); err != nil {
			t.Errorf("%s list should ignore theme environment: %v", cmd, err)
		}
	}
}

func TestThemeCLI(t *testing.T) {
	if os.Getenv("BSKYBAN_THEME_TEST_PROCESS") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"bskyban"}, os.Args[i+1:]...)
				os.Exit(realMain())
			}
		}
		t.Fatal("missing subprocess arguments")
	}
	t.Setenv("BSKYBAN_THEME_TEST_PROCESS", "1")
	t.Setenv("BSKY_HANDLE", "")
	t.Setenv("BSKY_APP_PASSWORD", "")
	t.Setenv("BSKYBAN_THEME", "invalid-env")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("NO_COLOR", "1")
	for _, tc := range []struct {
		args     []string
		code     int
		contains string
	}{
		{[]string{"themes"}, 0, "▸ 4m Sample post"},
		{[]string{"themes", "extra"}, 2, "themes takes no arguments"},
		{[]string{"feed", "--theme", "nope"}, 2, "auto, dark, light, ansi, dracula, catppuccin"},
		{[]string{"review", "--theme", "nope"}, 2, "unknown theme"},
		{[]string{"posts", "--theme", "nope"}, 2, "unknown theme"},
		{[]string{"feed", "--theme", "ansi"}, 1, "BSKY_HANDLE and BSKY_APP_PASSWORD must be set"},
		// no command means feed: --pages is a feed flag, so it parses and reaches the
		// (deliberately invalid) theme check; review rejects the flag first
		{[]string{"--pages", "2"}, 2, "unknown theme"},
		{[]string{"review", "--pages", "2"}, 2, "flag provided but not defined"},
	} {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestThemeCLI$", "--"}, tc.args...)...)
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		if code != tc.code || !strings.Contains(string(out), tc.contains) || bytes.Contains(out, []byte("\x1b[")) {
			t.Errorf("%v: exit=%d, output=%q", tc.args, code, out)
		}
	}
}
