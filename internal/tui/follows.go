package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mdfranz/bskyban/internal/bsky"
)

type followsBrowser struct {
	actor    bsky.Profile
	accounts []bsky.Profile
	cursor   string
	selected int
	loading  bool
	loaded   bool
}

type followsPageMsg struct {
	browser  *followsBrowser
	accounts []bsky.Profile
	cursor   string
	err      error
}

func (m model) openFollows() (model, tea.Cmd) {
	p, ok := m.subject()
	if !ok {
		return m, nil
	}
	m.follows = &followsBrowser{actor: p}
	m.err, m.status = nil, ""
	return m.loadFollows()
}

func (m model) loadFollows() (model, tea.Cmd) {
	b := m.follows
	if b.loading {
		return m, nil
	}
	b.loading = true
	c, actor, cursor := m.a.Client, b.actor.DID, b.cursor
	return m, func() tea.Msg {
		accounts, next, err := c.GetFollowsPage(context.Background(), actor, cursor)
		return followsPageMsg{browser: b, accounts: accounts, cursor: next, err: err}
	}
}

func (m model) applyFollowsPage(msg followsPageMsg) (tea.Model, tea.Cmd) {
	if m.follows != msg.browser {
		return m, nil
	}
	b := m.follows
	b.loading = false
	if msg.err != nil {
		m.err = fmt.Errorf("load following: %w", msg.err)
		return m, nil
	}
	b.accounts = append(b.accounts, msg.accounts...)
	b.cursor, b.loaded, m.err = msg.cursor, true, nil
	return m, m.prefetch()
}

// followsKey is the Reposts tab's keys: ← → move between the accounts, ↑ ↓ scroll
// the selected account's recent posts.
func (m model) followsKey(key string) (model, tea.Cmd) {
	b := m.follows
	if next, ok := m.scrollKeys(key); ok {
		next.scroll = next.clampScroll(next.scroll)
		return next, nil
	}
	move := func(i int) (model, tea.Cmd) {
		b.selected = min(max(i, 0), max(len(b.accounts)-1, 0))
		m.scroll, m.status, m.err = 0, "", nil
		cmd := m.prefetch()
		if b.loaded && b.cursor != "" && b.selected >= len(b.accounts)-2 {
			var load tea.Cmd
			m, load = m.loadFollows()
			cmd = tea.Batch(cmd, load)
		}
		return m, cmd
	}
	switch key {
	case "esc", "backspace", "q":
		m.follows, m.err, m.scroll = nil, nil, 0
	case "left", "h":
		return move(b.selected - 1)
	case "right", "l":
		return move(b.selected + 1)
	case "home":
		return move(0)
	case "end":
		return move(len(b.accounts) - 1)
	case "R":
		if !b.loaded || b.cursor != "" {
			return m.loadFollows()
		}
	case "enter", "o":
		if len(b.accounts) > 0 {
			p := b.accounts[b.selected]
			if err := openURL("https://bsky.app/profile/" + p.DID); err != nil {
				m.err = fmt.Errorf("open profile: %w", err)
			} else {
				m.status = "opened @" + p.Handle + "'s profile"
			}
		}
	}
	return m, nil
}

// followsView shows the selected followed account like the Reposts tab does: its
// profile on the left and its recent posts on the right (rl, from rightLines).
func (m model) followsView(rl []string) string {
	b := m.follows
	if len(b.accounts) == 0 {
		switch {
		case b.loading:
			return m.spin.View() + " loading…\n"
		case b.loaded:
			return dimStyle.Render("No accounts found.") + "\n"
		}
		return ""
	}
	pos := fmt.Sprintf("%d of %d", b.selected+1, len(b.accounts))
	if b.cursor != "" {
		pos += "+"
	}
	return m.panelsView(pos, rl)
}
