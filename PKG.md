# 3rd Party Dependencies

This document lists and classifies the 3rd party dependencies used in the `bskyban` project (see `go.mod`). Everything else is the Go standard library: Bluesky is reached with `net/http` and `encoding/json` against the AT Protocol XRPC endpoints, with no Bluesky SDK. The Go toolchain is 1.26+.

## Terminal UI (The Charm Stack)

*   **[github.com/charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea)**: TUI framework based on the Elm architecture. `internal/tui/ui.go` implements the `tea.Model` (`Init`/`Update`/`View`) for all three TUIs (`review`, `feed`, `posts`); background work (API calls, refresh ticks, animation ticks) comes back to `Update` as messages. `runTUI` starts it in the alternate screen.
*   **[github.com/charmbracelet/bubbles](https://github.com/charmbracelet/bubbles)**: Common Bubble Tea components. This repo uses `spinner` (loading indicators) and `textinput` (the post/reply composer).
*   **[github.com/charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss)**: Styles and layout. `internal/tui/theme.go` defines the colour themes as Lip Gloss styles (`applyTheme`), `ui.go` uses them for the two-panel layout, and `lipgloss.HasDarkBackground()` is called once in `runTUI` before the program starts so adaptive themes pick light or dark colours.
*   **[github.com/charmbracelet/x/ansi](https://github.com/charmbracelet/x)**: ANSI parsing helpers. `internal/termtext` uses `ansi.Strip` in `Sanitize` to remove escape sequences from remote text before the program adds its own styling.
*   **[github.com/charmbracelet/x/term](https://github.com/charmbracelet/x)**: Terminal size and TTY detection, used in `internal/cli` (`termWidth`) to wrap printed CLI output.

## Testing

*   **[github.com/muesli/termenv](https://github.com/muesli/termenv)**: Terminal colour profile and background detection, which Lip Gloss uses underneath. This repo imports it only in `internal/tui/theme_test.go`, to render each theme at every colour depth. At run time it is why the background must be queried before Bubble Tea starts reading input (see `ARCHITECTURE.md`), and it handles `NO_COLOR`.

## Text Processing

*   **[github.com/rivo/uniseg](https://github.com/rivo/uniseg)**: Unicode text segmentation. `internal/tui` uses `uniseg.GraphemeClusterCount` to enforce Bluesky's 300-grapheme post limit and show the counter in the composer, and `internal/cli` applies the same limit to `bskyban post`.

## Indirect dependencies

Pulled in underneath the packages above; this repo does not import them.

*   **[github.com/charmbracelet/colorprofile](https://github.com/charmbracelet/colorprofile)**, **[github.com/lucasb-eyer/go-colorful](https://github.com/lucasb-eyer/go-colorful)**: colour depth detection and colour maths for the themes.
*   **[github.com/mattn/go-runewidth](https://github.com/mattn/go-runewidth)**, **[github.com/clipperhouse/displaywidth](https://github.com/clipperhouse/displaywidth)** (with `stringish` and `uax29`): display-width calculation for wide characters and emoji.
*   **[github.com/muesli/ansi](https://github.com/muesli/ansi)**, **[github.com/charmbracelet/x/cellbuf](https://github.com/charmbracelet/x)**, **[github.com/xo/terminfo](https://github.com/xo/terminfo)**: ANSI-aware string handling and the terminal cell buffer used by the renderer.
*   **[github.com/muesli/cancelreader](https://github.com/muesli/cancelreader)**, **[github.com/mattn/go-localereader](https://github.com/mattn/go-localereader)**, **[github.com/erikgeiser/coninput](https://github.com/erikgeiser/coninput)**, **[github.com/mattn/go-isatty](https://github.com/mattn/go-isatty)**: cancellable terminal input and TTY detection.
*   **[github.com/atotto/clipboard](https://github.com/atotto/clipboard)**, **[github.com/aymanbagabas/go-osc52](https://github.com/aymanbagabas/go-osc52)**: clipboard support used by the `textinput` component.
*   **[golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys)**, **[golang.org/x/text](https://pkg.go.dev/golang.org/x/text)**: OS primitives and text tables for the packages above.

## Not dependencies

*   **No Bluesky/AT Protocol SDK.** `internal/bsky` calls `com.atproto.*` and `app.bsky.*` XRPC methods directly with `net/http` (30s client timeout) and `encoding/json`.
*   **No browser or image library.** Links and images open through the platform launcher in `internal/tui/images.go`: `$BROWSER` when set, otherwise `open` (macOS) or `xdg-open` (Linux). The `chafa` terminal viewer was removed.
*   **No database.** Decisions live in a JSON file and the audit log is JSON Lines (both in `internal/store`), both from `encoding/json` and `os`.
*   **No CLI framework.** Subcommands are dispatched in `cmd/bskyban/main.go` and flags use the standard `flag` package.
