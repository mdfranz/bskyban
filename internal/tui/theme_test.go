package tui

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mdfranz/bskyban/internal/termtext"
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
		if selStyle.GetBold() || (th.SelBG == nil && !selStyle.GetReverse()) {
			t.Errorf("%s has no selection styling", th.Name)
		}
		if got, err := ThemeByName(strings.ToUpper(th.Name)); err != nil || got.Name != th.Name {
			t.Errorf("lookup %s: %s, %v", th.Name, got.Name, err)
		}
	}
	_, err := ThemeByName("nope")
	if !errors.Is(err, ErrUnknownTheme) {
		t.Fatalf("want ErrUnknownTheme, got %v", err)
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
		th, err := ResolveTheme(tc.flag, tc.env)
		if err != nil || th.Name != tc.want {
			t.Errorf("resolve(%q, %q): %s, %v", tc.flag, tc.env, th.Name, err)
		}
	}
	if _, err := ResolveTheme("invalid", "auto"); !errors.Is(err, ErrUnknownTheme) {
		t.Errorf("invalid flag: %v", err)
	}
	if _, err := ResolveTheme("", "invalid"); !errors.Is(err, ErrUnknownTheme) {
		t.Errorf("invalid environment: %v", err)
	}
}

func TestApplyThemeSelection(t *testing.T) {
	preserveTheme(t)
	ansi, _ := ThemeByName("ansi")
	dracula, _ := ThemeByName("dracula")
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

func TestCmdThemes(t *testing.T) {
	previous := theme.Name
	var out bytes.Buffer
	if err := PrintThemes(&out); err != nil {
		t.Fatal(err)
	}
	plain := termtext.StripANSI(out.String())
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
	if err := PrintThemes(failingThemeWriter{}); !errors.Is(err, io.ErrClosedPipe) || theme.Name != previous {
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
func TestOnlyAdaptiveThemesDetectTheBackground(t *testing.T) {
	want := map[string]bool{"auto": true, "dark": false, "light": false, "ansi": false, "dracula": false, "catppuccin": true}
	for _, th := range themes {
		if got := th.adaptive(); got != want[th.Name] {
			t.Errorf("%s: adaptive = %v, want %v", th.Name, got, want[th.Name])
		}
	}
}
