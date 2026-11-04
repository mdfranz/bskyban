package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme fills the colour roles the UI draws with. A nil SelBG uses reverse video.
type Theme struct {
	Name                                        string
	Text, Dim, Warn, Error, OK, Heading, Accent lipgloss.TerminalColor
	SelBG                                       lipgloss.TerminalColor
	Fresh                                       []lipgloss.TerminalColor
}

func colours(values ...string) []lipgloss.TerminalColor {
	out := make([]lipgloss.TerminalColor, len(values))
	for i, value := range values {
		out[i] = lipgloss.Color(value)
	}
	return out
}

func adaptive(dark, light string) lipgloss.TerminalColor {
	return lipgloss.AdaptiveColor{Dark: dark, Light: light}
}

func ansiTheme(name string, bg lipgloss.TerminalColor, fresh []lipgloss.TerminalColor) Theme {
	return Theme{
		Name: name, Text: lipgloss.NoColor{}, Dim: lipgloss.Color("8"),
		Warn: lipgloss.Color("3"), Error: lipgloss.Color("1"), OK: lipgloss.Color("2"),
		Heading: lipgloss.Color("6"), Accent: lipgloss.Color("5"), SelBG: bg, Fresh: fresh,
	}
}

// A slice keeps lookup, help, and preview order deterministic.
var themes = makeThemes()

func makeThemes() []Theme {
	dark := colours("213", "177", "141", "105", "69", "61", "60", "59", "238")
	light := colours("201", "165", "129", "93", "63", "103", "145", "188", "252")
	fresh := make([]lipgloss.TerminalColor, len(dark))
	for i := range dark {
		fresh[i] = adaptive(string(dark[i].(lipgloss.Color)), string(light[i].(lipgloss.Color)))
	}
	return []Theme{
		ansiTheme("auto", adaptive("236", "254"), fresh),
		ansiTheme("dark", lipgloss.Color("236"), dark),
		ansiTheme("light", lipgloss.Color("254"), light),
		ansiTheme("ansi", nil, colours("13", "5", "8")),
		// https://draculatheme.com/contribute
		{
			Name: "dracula", Text: lipgloss.Color("#f8f8f2"), Dim: lipgloss.Color("#6272a4"),
			Warn: lipgloss.Color("#f1fa8c"), Error: lipgloss.Color("#ff5555"), OK: lipgloss.Color("#50fa7b"),
			Heading: lipgloss.Color("#8be9fd"), Accent: lipgloss.Color("#ff79c6"), SelBG: lipgloss.Color("#44475a"),
			Fresh: colours("#ff79c6", "#bd93f9", "#6272a4", "#44475a"),
		},
		// Mocha / Latte: https://github.com/catppuccin/catppuccin
		{
			Name: "catppuccin", Text: adaptive("#cdd6f4", "#4c4f69"), Dim: adaptive("#6c7086", "#9ca0b0"),
			Warn: adaptive("#f9e2af", "#df8e1d"), Error: adaptive("#f38ba8", "#d20f39"), OK: adaptive("#a6e3a1", "#40a02b"),
			Heading: adaptive("#89dceb", "#04a5e5"), Accent: adaptive("#cba6f7", "#8839ef"), SelBG: adaptive("#313244", "#ccd0da"),
			Fresh: []lipgloss.TerminalColor{
				adaptive("#f5c2e7", "#ea76cb"), adaptive("#cba6f7", "#8839ef"),
				adaptive("#6c7086", "#9ca0b0"), adaptive("#313244", "#ccd0da"),
			},
		},
	}
}

var theme Theme

// adaptive reports whether any of the theme's colours depend on the terminal's
// background, so it must be detected before the TUI starts.
func (t Theme) adaptive() bool {
	isAdaptive := func(c lipgloss.TerminalColor) bool {
		_, ok := c.(lipgloss.AdaptiveColor)
		return ok
	}
	for _, c := range append([]lipgloss.TerminalColor{t.Text, t.Dim, t.Warn, t.Error, t.OK, t.Heading, t.Accent, t.SelBG}, t.Fresh...) {
		if isAdaptive(c) {
			return true
		}
	}
	return false
}

func init() { applyTheme(themes[0]) }

func themeByName(name string) (Theme, error) {
	names := make([]string, len(themes))
	for i, t := range themes {
		if strings.EqualFold(name, t.Name) {
			return t, nil
		}
		names[i] = t.Name
	}
	return Theme{}, fmt.Errorf("%w: unknown theme %q (choose from: %s)", errUsage, name, strings.Join(names, ", "))
}

func resolveTheme(flag, env string) (Theme, error) {
	if flag != "" {
		return themeByName(flag)
	}
	if env != "" {
		return themeByName(env)
	}
	return themeByName("auto")
}

func applyTheme(t Theme) {
	theme = t
	base := lipgloss.NewStyle().Foreground(t.Text)
	titleStyle = base.Bold(true)
	dimStyle = base.Foreground(t.Dim)
	warnStyle = base.Foreground(t.Warn)
	errStyle = base.Foreground(t.Error)
	okStyle = base.Foreground(t.OK)
	headingStyle = base.Bold(true).Foreground(t.Heading)
	accentStyle = base.Foreground(t.Accent)
	selStyle = base.Bold(true)
	if t.SelBG == nil {
		selStyle = selStyle.Reverse(true)
	} else {
		selStyle = selStyle.Background(t.SelBG)
	}
}

// cmdThemes needs no credentials and leaves the active theme unchanged.
func cmdThemes(w io.Writer) error {
	previous := theme
	defer applyTheme(previous)
	for _, t := range themes {
		applyTheme(t)
		var ramp strings.Builder
		for _, c := range t.Fresh {
			ramp.WriteString(lipgloss.NewStyle().Bold(true).Foreground(c).Render("┃"))
		}
		_, err := fmt.Fprintf(w, "%s\n  %s %s %s\n%s\n  %s  %s  %s  fresh %s\n\n",
			t.Name, dimStyle.Render("4m"), titleStyle.Render("Sample post"), accentStyle.Render("@you.bsky.social"),
			selStyle.Render("▸ 4m Sample post @you.bsky.social"), warnStyle.Render("status"),
			errStyle.Render("error"), okStyle.Render("kept"), ramp.String())
		if err != nil {
			return err
		}
	}
	return nil
}
