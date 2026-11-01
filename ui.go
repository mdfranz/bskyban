package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

type (
	loadedMsg struct {
		list []Profile
		err  error
	}
	profileMsg struct {
		p   *Profile
		did string
		err error
	}
	postsMsg struct {
		did   string
		posts []Post
		err   error
	}
	actMsg struct {
		undo   undoEntry
		isUndo bool
		err    error
	}
)

type undoEntry struct {
	idx      int
	profile  Profile
	decision string
	blockURI string
}

type model struct {
	a        *app
	since    int
	backlog  bool
	list     []Profile
	idx      int
	loading  bool
	busy     bool
	spin     spinner.Model
	profiles map[string]*Profile
	posts    map[string]*[]Post // nil value = in flight
	history  []undoEntry
	status   string
	err      error
	width    int
	height   int
}

func newModel(a *app, since int, backlog bool) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return model{a: a, since: since, backlog: backlog, loading: true, spin: sp, profiles: map[string]*Profile{}, posts: map[string]*[]Post{}}
}

func runTUI(a *app, since int, backlog bool) error {
	_, err := tea.NewProgram(newModel(a, since, backlog), tea.WithAltScreen()).Run()
	return err
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, func() tea.Msg {
		l, err := m.a.followers(context.Background(), m.since, false, m.backlog)
		return loadedMsg{l, err}
	})
}

func (m model) fetchProfile(did string) tea.Cmd {
	if _, ok := m.profiles[did]; ok {
		return nil
	}
	m.profiles[did] = nil // mark in flight
	m.posts[did] = nil
	c := m.a.c
	return tea.Batch(
		func() tea.Msg {
			p, err := c.GetProfile(context.Background(), did)
			return profileMsg{p, did, err}
		},
		func() tea.Msg {
			posts, err := c.GetRecentPosts(context.Background(), did, 5)
			return postsMsg{did, posts, err}
		},
	)
}

// prefetch loads the current and next profiles.
func (m model) prefetch() tea.Cmd {
	var cmds []tea.Cmd
	for i := m.idx; i <= m.idx+1 && i < len(m.list); i++ {
		cmds = append(cmds, m.fetchProfile(m.list[i].DID))
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case loadedMsg:
		m.loading = false
		m.err = msg.err
		m.list = msg.list
		return m, m.prefetch()
	case profileMsg:
		if msg.err != nil {
			delete(m.profiles, msg.did)
			m.err = msg.err
		} else {
			m.profiles[msg.did] = msg.p
		}
	case postsMsg:
		if msg.err != nil {
			delete(m.posts, msg.did)
			m.err = msg.err
		} else {
			m.posts[msg.did] = &msg.posts
		}
	case actMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		e := msg.undo
		if msg.isUndo {
			m.idx = e.idx
			m.status = fmt.Sprintf("undid %s for @%s", e.decision, e.profile.Handle)
			return m, m.prefetch()
		}
		m.history = append(m.history, e)
		m.idx++
		m.status = fmt.Sprintf("%s @%s", e.decision, e.profile.Handle)
		return m, m.prefetch()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) current() (Profile, bool) {
	if m.idx < 0 || m.idx >= len(m.list) {
		return Profile{}, false
	}
	p := m.list[m.idx]
	if full := m.profiles[p.DID]; full != nil {
		p = *full
	}
	return p, true
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	}
	if m.loading || m.busy {
		return m, nil
	}
	p, ok := m.current()
	a := m.a
	switch k.String() {
	case "b":
		if !ok {
			return m, nil
		}
		m.busy = true
		idx := m.idx
		return m, func() tea.Msg {
			uri, err := a.c.Block(context.Background(), p.DID)
			if err == nil {
				err = a.st.Set(p.DID, p.Handle, decBlocked)
			}
			return actMsg{undo: undoEntry{idx, p, decBlocked, uri}, err: err}
		}
	case "k":
		if !ok {
			return m, nil
		}
		err := a.st.Set(p.DID, p.Handle, decKept)
		if err != nil {
			m.err = err
			return m, nil
		}
		m.history = append(m.history, undoEntry{m.idx, p, decKept, ""})
		m.idx++
		m.err = nil
		m.status = "kept @" + p.Handle
		return m, m.prefetch()
	case "s":
		if ok {
			if err := a.st.Set(p.DID, p.Handle, decSkipped); err != nil {
				m.err = err
				return m, nil
			}
			m.history = append(m.history, undoEntry{m.idx, p, decSkipped, ""})
			m.idx++
			m.err = nil
			m.status = "skipped @" + p.Handle
			return m, m.prefetch()
		}
	case "u":
		if len(m.history) == 0 {
			m.status = "nothing to undo"
			return m, nil
		}
		e := m.history[len(m.history)-1]
		m.history = m.history[:len(m.history)-1]
		m.busy = true
		return m, func() tea.Msg {
			var err error
			if e.decision == decBlocked && e.blockURI != "" {
				err = a.c.Unblock(context.Background(), e.blockURI)
			}
			if err == nil {
				err = a.st.Delete(e.profile.DID)
			}
			return actMsg{undo: e, isUndo: true, err: err}
		}
	}
	return m, nil
}

const panelGap = 4

var (
	headingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	accentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
)

// panelWidths splits the terminal width into left (profile) and right (posts)
// widths. stacked is true when the window is too narrow for two columns.
func (m model) panelWidths() (left, right int, stacked bool) {
	w := m.width
	if w <= 0 {
		w = 110
	}
	if w < 90 {
		return w - 2, w - 2, true
	}
	// Posts get the narrower column; the profile panel gets the rest, up to 64.
	r := min(max(w*2/5, 44), 72)
	l := min(max(w-r-panelGap, 44), 64)
	return l, r, false
}

func styleLines(lines []string, st lipgloss.Style) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = st.Render(l)
	}
	return out
}

func (m model) profilePanel(cw int) []string {
	p, _ := m.current()
	name := p.DisplayName
	if name == "" {
		name = "(no display name)"
	}
	lines := wrapText(name, cw-10)
	lines = styleLines(lines, headingStyle)
	lines[len(lines)-1] += "  " + dimStyle.Render(fmt.Sprintf("%d of %d", m.idx+1, len(m.list)))
	lines = append(lines, accentStyle.Render("@"+p.Handle),
		dimStyle.Render(osc8("https://bsky.app/profile/"+p.Handle, "open profile ↗")), "")
	if m.profiles[p.DID] == nil {
		return append(lines, m.spin.View()+" loading profile…")
	}
	avatar := "yes"
	if p.Avatar == "" {
		avatar = warnStyle.Render("no")
	}
	row := func(label, val string) {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("%-12s", label))+" "+val)
	}
	row("Account age", humanAge(p.Age()))
	row("Avatar", avatar)
	row("Posts", fmt.Sprint(p.PostsCount))
	row("Followers", fmt.Sprint(p.FollowersCount))
	row("Following", fmt.Sprint(p.FollowsCount))
	lines = append(lines, "")
	if bio := strings.TrimSpace(p.Description); bio != "" {
		return append(lines, wrapText(bio, cw)...)
	}
	return append(lines, dimStyle.Render("(no bio)"))
}

// postsPanel shows full posts, dropping trailing ones only if they don't fit in maxLines.
func (m model) postsPanel(cw, maxLines int) []string {
	p, _ := m.current()
	lines := []string{headingStyle.Render("Recent posts"), ""}
	switch pp := m.posts[p.DID]; {
	case pp == nil:
		return append(lines, m.spin.View()+" loading posts…")
	case len(*pp) == 0:
		return append(lines, dimStyle.Render("(no posts)"))
	default:
		shown := 0
		for _, post := range *pp {
			r := renderPost(post, cw)
			if shown > 0 && m.height > 0 && len(lines)+len(r) > maxLines {
				break
			}
			lines = append(lines, r...)
			shown++
		}
		if shown < len(*pp) {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("(+%d more posts hidden; enlarge the window)", len(*pp)-shown)))
		}
	}
	return lines
}

// joinColumns puts two line blocks side by side, padding by visible width.
func joinColumns(left, right []string, leftW int) string {
	var b strings.Builder
	for i := 0; i < max(len(left), len(right)); i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		b.WriteString(l + strings.Repeat(" ", max(leftW-visibleWidth(l), 0)+panelGap) + r + "\n")
	}
	return b.String()
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("bskyban") + dimStyle.Render("  b block · k keep · s skip · u undo · q quit") + "\n\n")
	switch {
	case m.loading:
		b.WriteString(m.spin.View() + " loading followers…\n")
	case m.idx >= len(m.list):
		b.WriteString(okStyle.Render("All caught up — no new followers.") + fmt.Sprintf(" %d followers reviewed this session.\n", len(m.history)))
	default:
		lw, rw, stacked := m.panelWidths()
		maxLines := 1000
		if m.height > 0 {
			maxLines = max(m.height-6, 6) // header and footer
		}
		left, right := m.profilePanel(lw), m.postsPanel(rw, maxLines)
		if stacked {
			b.WriteString(strings.Join(append(append(left, ""), right...), "\n") + "\n")
		} else {
			b.WriteString(joinColumns(left, right, lw))
		}
	}
	b.WriteString("\n")
	if m.err != nil {
		b.WriteString(errStyle.Render("error: "+m.err.Error()) + "\n")
	}
	if m.busy {
		b.WriteString(m.spin.View() + " working…\n")
	} else if m.status != "" {
		b.WriteString(dimStyle.Render(m.status) + "\n")
	}
	return b.String()
}

// renderPost shows a post in full: a dim header line, then the wrapped text indented.
func renderPost(p Post, w int) []string {
	kind := ""
	switch {
	case p.Repost:
		kind = " · repost"
	case p.Reply:
		kind = " · reply"
	}
	age := "?"
	if t, err := time.Parse(time.RFC3339, p.CreatedAt); err == nil {
		age = humanAge(time.Since(t))
	}
	lines := []string{dimStyle.Render(age + " ago" + kind)}
	indent := "  "
	if text := strings.TrimSpace(p.Text); text != "" {
		for _, l := range wrapText(text, w-len(indent)) {
			lines = append(lines, indent+l)
		}
	}
	for _, a := range p.Attach {
		for _, l := range wrapText(a, w-len(indent)) {
			lines = append(lines, indent+dimStyle.Render(l))
		}
	}
	if len(lines) == 1 {
		lines = append(lines, indent+"(no text)")
	}
	return append(lines, "")
}
