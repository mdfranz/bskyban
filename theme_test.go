package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func preserveTheme(t *testing.T) {
	t.Helper()
	previous := theme
	t.Cleanup(func() { applyTheme(previous) })
}

func TestThemesComplete(t *testing.T) {
	preserveTheme(t)
	seen := map[string]bool{}
	for _, th := range themes {
		if th.Name == "" || seen[th.Name] {
			t.Fatalf("empty or duplicate name %q", th.Name)
		}
		seen[th.Name] = true
		for role, c := range map[string]lipgloss.TerminalColor{
			"text": th.Text, "dim": th.Dim, "warn": th.Warn, "error": th.Error,
			"ok": th.OK, "heading": th.Heading, "accent": th.Accent,
		} {
			if c == nil {
				t.Errorf("%s has no %s", th.Name, role)
			}
		}
		if len(th.Fresh) < 2 {
			t.Errorf("%s has too few fade colours", th.Name)
		}
		for _, c := range th.Fresh {
			if c == nil {
				t.Errorf("%s has a nil fade colour", th.Name)
			}
		}
		applyTheme(th)
		if !selStyle.GetBold() || (th.SelBG == nil && !selStyle.GetReverse()) {
			t.Errorf("%s has no selection styling", th.Name)
		}
		if got, err := themeByName(strings.ToUpper(th.Name)); err != nil || got.Name != th.Name {
			t.Errorf("lookup %s: %s, %v", th.Name, got.Name, err)
		}
	}
	_, err := themeByName("nope")
	if !errors.Is(err, errUsage) {
		t.Fatalf("want usage error, got %v", err)
	}
	for _, th := range themes {
		if !strings.Contains(err.Error(), th.Name) {
			t.Errorf("error omits %s: %v", th.Name, err)
		}
	}
}

func TestResolveTheme(t *testing.T) {
	for _, tc := range []struct{ flag, env, want string }{
		{"", "", "auto"}, {"", "catppuccin", "catppuccin"},
		{"ansi", "catppuccin", "ansi"}, {"LiGhT", "invalid", "light"},
	} {
		th, err := resolveTheme(tc.flag, tc.env)
		if err != nil || th.Name != tc.want {
			t.Errorf("resolve(%q, %q): %s, %v", tc.flag, tc.env, th.Name, err)
		}
	}
	if _, err := resolveTheme("invalid", "auto"); !errors.Is(err, errUsage) {
		t.Errorf("invalid flag: %v", err)
	}
	if _, err := resolveTheme("", "invalid"); !errors.Is(err, errUsage) {
		t.Errorf("invalid environment: %v", err)
	}
}

func TestApplyThemeSelection(t *testing.T) {
	preserveTheme(t)
	ansi, _ := themeByName("ansi")
	dracula, _ := themeByName("dracula")
	applyTheme(dracula)
	applyTheme(ansi)
	if !selStyle.GetReverse() || selStyle.GetBackground() != (lipgloss.NoColor{}) {
		t.Errorf("ANSI selection: reverse=%v bg=%v", selStyle.GetReverse(), selStyle.GetBackground())
	}
	applyTheme(dracula)
	if selStyle.GetReverse() || selStyle.GetBackground() != lipgloss.Color("#44475a") || selStyle.GetForeground() != dracula.Text {
		t.Errorf("Dracula selection: %v", selStyle)
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
			if _, err := parseTUIOptions(cmd, []string{"--theme", "nope"}); !errors.Is(err, errUsage) {
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

func TestCmdThemes(t *testing.T) {
	previous := theme.Name
	var out bytes.Buffer
	if err := cmdThemes(&out); err != nil {
		t.Fatal(err)
	}
	plain := ansiRe.ReplaceAllString(out.String(), "")
	for _, th := range themes {
		if !strings.Contains(plain, th.Name+"\n") {
			t.Errorf("preview omits %s", th.Name)
		}
	}
	if strings.Count(plain, "▸ 4m Sample post") != len(themes) || strings.Count(plain, "fresh ") != len(themes) {
		t.Errorf("missing samples:\n%s", plain)
	}
	if theme.Name != previous {
		t.Error("preview changed active theme")
	}
	if err := cmdThemes(failingThemeWriter{}); !errors.Is(err, io.ErrClosedPipe) || theme.Name != previous {
		t.Errorf("write failure: %v, theme=%s", err, theme.Name)
	}
}

type failingThemeWriter struct{}

func (failingThemeWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestThemeRendering(t *testing.T) {
	preserveTheme(t)
	previous := lipgloss.DefaultRenderer()
	t.Cleanup(func() { lipgloss.SetDefaultRenderer(previous) })
	for _, dark := range []bool{false, true} {
		for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.ANSI, termenv.Ascii} {
			r := lipgloss.NewRenderer(io.Discard)
			r.SetColorProfile(profile)
			r.SetHasDarkBackground(dark)
			lipgloss.SetDefaultRenderer(r)
			for _, th := range themes {
				applyTheme(th)
				got := selStyle.Render("▸ Sample post")
				if !strings.Contains(got, "▸ Sample post") {
					t.Errorf("%s lost selection marker", th.Name)
				}
				if profile == termenv.Ascii && strings.Contains(got, "\x1b[") {
					t.Errorf("%s emitted escapes without colours: %q", th.Name, got)
				}
			}
		}
	}
}

func TestThemeFadeEndpoints(t *testing.T) {
	preserveTheme(t)
	now := time.Now()
	m := model{fresh: map[string]time.Time{"post": now}, now: func() time.Time { return now }}
	for _, th := range themes {
		applyTheme(th)
		m.fresh["post"] = now
		first, ok := m.freshStyle("post")
		if !ok || first.GetForeground() != th.Fresh[0] {
			t.Errorf("%s: wrong initial colour", th.Name)
		}
		now = now.Add(freshFor - time.Nanosecond)
		last, ok := m.freshStyle("post")
		if !ok || last.GetForeground() != th.Fresh[len(th.Fresh)-1] {
			t.Errorf("%s: wrong final colour", th.Name)
		}
		now = now.Add(time.Nanosecond)
		if _, ok := m.freshStyle("post"); ok {
			t.Errorf("%s: highlight did not expire", th.Name)
		}
	}
}

// Separate processes exercise realMain and fresh renderer environment detection.
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

func TestOnlyAdaptiveThemesDetectTheBackground(t *testing.T) {
	want := map[string]bool{"auto": true, "dark": false, "light": false, "ansi": false, "dracula": false, "catppuccin": true}
	for _, th := range themes {
		if got := th.adaptive(); got != want[th.Name] {
			t.Errorf("%s: adaptive = %v, want %v", th.Name, got, want[th.Name])
		}
	}
}
