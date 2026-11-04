package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/termtext"
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
	return m, nil
}

func (m model) followsKey(key string) (model, tea.Cmd) {
	b := m.follows
	switch key {
	case "esc", "backspace", "q":
		m.follows, m.err = nil, nil
	case "up":
		b.selected = max(0, b.selected-1)
	case "down":
		if b.selected+1 < len(b.accounts) {
			b.selected++
		}
		if b.selected >= len(b.accounts)-1 && b.loaded && b.cursor != "" {
			return m.loadFollows()
		}
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

func (m model) followsView() string {
	b := m.follows
	var out strings.Builder
	out.WriteString(headingStyle.Render("Accounts followed by @"+b.actor.Handle) + "\n\n")
	if len(b.accounts) == 0 {
		if b.loading {
			out.WriteString("Loading…\n")
		} else if b.loaded {
			out.WriteString("No accounts found.\n")
		}
		return out.String()
	}
	rows := max((m.height-8)/2, 1)
	if m.height <= 0 {
		rows = 20
	}
	start := max(0, b.selected-rows+1)
	end := min(len(b.accounts), start+rows)
	for i := start; i < end; i++ {
		p := b.accounts[i]
		name := p.DisplayName
		if name == "" {
			name = p.Handle
		}
		label := termtext.TruncateWidth(termtext.Sanitize(name+" (@"+p.Handle+")"), max(m.width-4, 10))
		if i == b.selected {
			out.WriteString(selStyle.Render("▸ " + label))
		} else {
			out.WriteString("  " + label)
		}
		out.WriteByte('\n')
		bio := strings.Join(strings.Fields(termtext.Sanitize(p.Description)), " ")
		if bio != "" {
			bio = termtext.TruncateWidth(bio, max(m.width-4, 1))
			if i == b.selected {
				out.WriteString(selStyle.Render("  " + bio))
			} else {
				out.WriteString(dimStyle.Render("  " + bio))
			}
			out.WriteByte('\n')
		}
	}
	if b.loading {
		out.WriteString(dimStyle.Render("Loading more…") + "\n")
	} else if b.cursor != "" && end == len(b.accounts) {
		out.WriteString(dimStyle.Render("↓ load more") + "\n")
	}
	return out.String()
}
