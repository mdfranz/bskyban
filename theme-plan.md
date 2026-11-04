# Plan: colour themes for bskyban

Steps 1 (semantic roles + adaptive colours) and 2 (a few named themes, chosen with
`--theme` / `BSKYBAN_THEME`).

## Context

All TUI colours live in package-level styles at the top of `ui.go`:

| Style          | Today                         | Used for                                      |
|----------------|-------------------------------|-----------------------------------------------|
| `titleStyle`   | bold                          | names, titles                                 |
| `dimStyle`     | fg `8`                        | ages, post text in the list, hints, separators (30 uses) |
| `warnStyle`    | fg `3`                        | status line, picker                           |
| `errStyle`     | fg `1`                        | errors, ♥, "blocked"                          |
| `okStyle`      | fg `2`                        | "kept", "followed", "all caught up"           |
| `headingStyle` | bold fg `6`                   | headings, tab names, "▸ This post"            |
| `accentStyle`  | fg `5`                        | ↻ reposted by, ▎, ┃, ↗, @handles              |
| `selStyle`     | bold, bg `236`                | the selected row in the list                  |
| `freshRamp`    | `213 177 141 105 69 61 60 59 238` | `--fancy` new-post fade                   |

Seven of the nine already use the 16 ANSI colours, so they follow the terminal's own
palette. The two fixed 256-colour values are the problem:
- **`236`**, near-black grey: on a light terminal the selected row becomes a dark bar with
  dark text.
- **`freshRamp`**: fades towards dark greys, which vanish on dark terminals and look heavy
  on light ones.

There's also no way to choose a look.

The goal: readable on light and dark terminals by default, plus a small set of named
themes. No new dependencies, and no Lip Gloss v2 upgrade (the project is on
`lipgloss v1.1.0` / `bubbletea v1.3.10`).

## Design

### Roles, not colours (`theme.go`, new)

```go
// Theme fills the colour roles the UI draws with.
type Theme struct {
	Name    string
	Text    lipgloss.TerminalColor   // "" / NoColor = terminal default
	Dim     lipgloss.TerminalColor
	Warn    lipgloss.TerminalColor
	Error   lipgloss.TerminalColor
	OK      lipgloss.TerminalColor
	Heading lipgloss.TerminalColor
	Accent  lipgloss.TerminalColor
	SelBG   lipgloss.TerminalColor   // nil: mark the selected row with reverse video instead
	Fresh   []lipgloss.TerminalColor // --fancy fade, bright to faded (≥ 2 entries)
}
```

- `lipgloss.TerminalColor` accepts `lipgloss.Color("5")`, hex, `lipgloss.AdaptiveColor`
  and `lipgloss.NoColor{}`, so one struct covers fixed, ANSI and light/dark-adaptive
  themes.
- `applyTheme(t Theme)` rebuilds the existing package-level styles (`dimStyle`,
  `selStyle`, …) from the roles. Call sites stay as they are: same variables, same
  names, no plumbing through `model`. This matches how the code is written today.
- `freshStyle()` in `ui.go` reads `theme.Fresh` instead of `freshRamp`. `freshRamp` is
  deleted.
- `selStyle` uses `Background(SelBG)` when set, otherwise `Reverse(true)`.

### Themes (step 1 is `auto`; step 2 adds the rest)

| Name | Colours | Notes |
|---|---|---|
| `auto` (default) | Today's ANSI roles (`8 3 1 2 6 5`). `SelBG` and `Fresh` become `AdaptiveColor`: SelBG `{Dark: "236", Light: "254"}`; the Fresh dark ramp is today's, the light ramp is `201 165 129 93 63 103 145 188 252` | Lip Gloss picks light or dark from its own detection. Looks identical to today on dark terminals. |
| `dark` | `auto`'s dark side, fixed | For when detection guesses wrong (tmux, ssh, some ChromeOS setups). |
| `light` | `auto`'s light side, fixed | Same. |
| `ansi` | Only colours 0–15: roles as today, `SelBG = nil` (reverse video), Fresh `13, 5, 8` | Follows the terminal's colour scheme exactly (Solarized, Gruvbox, …). |
| `dracula` | Hex from the official palette: Dim `#6272a4`, Warn `#f1fa8c`, Error `#ff5555`, OK `#50fa7b`, Heading `#8be9fd`, Accent `#ff79c6`, SelBG `#44475a`, Fresh `#ff79c6 → #bd93f9 → #6272a4 → #44475a` | Dark-only (Dracula has no official light variant). |
| `catppuccin` | `AdaptiveColor` per role: Mocha on dark (Dim `#6c7086`, Warn `#f9e2af`, Error `#f38ba8`, OK `#a6e3a1`, Heading `#89dceb`, Accent `#cba6f7`, SelBG `#313244`), Latte on light (Dim `#9ca0b0`, Warn `#df8e1d`, Error `#d20f39`, OK `#40a02b`, Heading `#04a5e5`, Accent `#8839ef`, SelBG `#ccd0da`); Fresh from pink/mauve to surface | Hex values typed in from the published palettes; no `catppuccin/go` dependency. |

Hex colours need a truecolor or 256-colour terminal. Lip Gloss v1 already lowers them
to the nearest colour the terminal supports, so nothing extra is needed. `NO_COLOR` is
already honoured by the colour profile (termenv), so it needs no work either; check it
in verification rather than assume.

### Choosing a theme (`main.go`, `cli.go`)

- `--theme NAME` on `review`, `feed` and `posts` (every command that starts the TUI),
  defaulting to `$BSKYBAN_THEME`, then `auto`. The flag wins over the environment.
- An unknown name is a usage error (exit 2) that lists the valid names, using the
  existing `errUsage` path.
- `applyTheme` runs once, after flag parsing and before `runTUI`. Non-TUI commands
  (`list`, `show`, `log`, …) print plain text and are unaffected.
- **Preview command:** `bskyban themes` prints each theme's name, a sample list row, a
  selected row, the status/error colours and the fresh ramp, so you can pick one
  without starting the TUI. It needs no login, like `bskyban log`.

## Files

- `theme.go` (new): `Theme`, the six theme values, `themes` (name → Theme in display
  order), `themeByName(name) (Theme, error)`, `applyTheme(Theme)`, `cmdThemes(w io.Writer)`.
- `ui.go`: the style `var` block becomes the zero-value declarations `applyTheme`
  fills, with an `init()` that applies `auto` so tests and code paths that never parse
  flags get today's look. `freshRamp` is removed and `freshStyle` uses `theme.Fresh`.
- `main.go`: the `--theme` flag in the three TUI flag sets (one helper so they don't
  drift), and the `themes` command next to `log` (before login).
- `cli.go`: usage text (`[--theme NAME]`, the `themes` command, `BSKYBAN_THEME` under
  Environment).
- `README.md`: a short "Themes" section (names, flag/env, `bskyban themes`), plus
  `BSKYBAN_THEME` in the variables table.
- `fancy_test.go`: `TestFadeColourMovesAlongTheRamp` currently compares foregrounds
  from `freshRamp`; switch it to the active theme's `Fresh`.

## Tests (`theme_test.go`, new)

- Every theme in `themes` fills every role. `Fresh` has at least 2 entries, and `SelBG`
  is either set or reverse video is used (checked through `selStyle`).
- `themeByName`: every listed name resolves; matching is case-insensitive; an unknown
  name errors and the message lists the valid names.
- Flag/env precedence: `--theme` beats `BSKYBAN_THEME`, which beats `auto`. Keep this
  testable with a small `resolveTheme(flag, env string)` helper.
- `applyTheme(ansi)` makes `selStyle` reverse video with no background; `applyTheme(dracula)`
  sets `selStyle`'s background to `#44475a`. Restore `auto` with `t.Cleanup`.
- `cmdThemes` output names every theme and includes a sample row for each.
- The existing suite still passes under `auto` (the `init()` default).

## Verification

1. `go vet ./... && go test ./...`.
2. `./bskyban themes` in your terminal: every theme readable, with the selected row
   clearly marked.
3. `./bskyban feed --theme light` in a light terminal profile and `--theme dark` in a
   dark one. Check the selected row, the hints, `--fancy` highlights and the post view.
4. `BSKYBAN_THEME=catppuccin ./bskyban feed`, then the same with `--theme ansi` to check
   the flag wins.
5. `NO_COLOR=1 ./bskyban feed`: no colours, and the selected row is still visible (bold).
   Reverse video may also be dropped under `NO_COLOR`; if the row disappears, keep `▸` as
   the marker, which already exists.
6. `./bskyban feed --theme nope`: exit code 2 with the list of valid names.

## Out of scope (later)

- User theme files (`~/.config/bskyban/theme.toml`).
- bubbletint's catalogue of terminal schemes.
- Lip Gloss / Bubble Tea v2 and `tea.BackgroundColorMsg`-based detection.
