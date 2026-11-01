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
	titleStyle   = lipgloss.NewStyle().Bold(true)
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	headingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	accentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	selStyle     = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236"))
)

const panelGap = 4

type tabID int

const (
	tabFollowing  tabID = iota // your home timeline
	tabCandidates              // new followers (review) or repost authors (feed)
)

type (
	loadedMsg struct {
		list []Profile
		ctxs map[string]repostCtx
		err  error
	}
	followingMsg struct {
		items  []FeedItem
		cursor string
		err    error
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
		newURI string // block record created by this action, if any
		err    error
	}
)

type undoEntry struct {
	tab      tabID
	idx      int
	profile  Profile
	decision string
	blockURI string // block record this decision created
	prev     *Entry // decision recorded before this one
}

type model struct {
	a         *app
	title     string
	candLabel string
	loader    loadFunc
	timeline  timelineFunc
	tabs      []tabID
	tab       int

	// candidates tab
	ctxs    map[string]repostCtx // how each candidate reached you (feed mode)
	list    []Profile
	idx     int
	loading bool

	// following tab
	items         []FeedItem
	cur           int
	listTop       int
	inPost        bool
	tlCursor      string
	followLoading bool
	moreBusy      bool

	busy     bool
	spin     spinner.Model
	profiles map[string]*Profile
	posts    map[string]*[]Post // nil value = in flight
	blockURI map[string]string  // block records created this session
	history  []undoEntry
	confirm  string // DID awaiting a second `b` to block someone you follow
	scroll   int    // right-panel scroll offset
	status   string
	err      error
	width    int
	height   int
}

// loadFunc returns the candidates to review, plus optional per-candidate context.
type loadFunc func(ctx context.Context) ([]Profile, map[string]repostCtx, error)

// timelineFunc returns one page of the home timeline and the next cursor.
type timelineFunc func(ctx context.Context, cursor string) ([]FeedItem, string, error)

func newModel(a *app, title string, load loadFunc) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	label := "Reposts"
	if title == "review" {
		label = "New followers"
	}
	return model{
		a: a, title: title, candLabel: label, loader: load, loading: true, spin: sp,
		tabs:     []tabID{tabCandidates},
		profiles: map[string]*Profile{}, posts: map[string]*[]Post{}, blockURI: map[string]string{},
	}
}

// newFeedModel adds a Following tab (your timeline) in front of the repost-author tab.
func newFeedModel(a *app, load loadFunc, tl timelineFunc) model {
	m := newModel(a, "feed", load)
	m.timeline = tl
	m.tabs = []tabID{tabFollowing, tabCandidates}
	m.followLoading = true
	return m
}

func runTUI(m model) error {
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m model) active() tabID { return m.tabs[m.tab] }

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spin.Tick, func() tea.Msg {
		l, ctxs, err := m.loader(context.Background())
		return loadedMsg{l, ctxs, err}
	}}
	if m.timeline != nil {
		cmds = append(cmds, loadTimeline(m.timeline, ""))
	}
	return tea.Batch(cmds...)
}

func loadTimeline(tl timelineFunc, cursor string) tea.Cmd {
	return func() tea.Msg {
		items, next, err := tl(context.Background(), cursor)
		return followingMsg{items, next, err}
	}
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

// subject is the account being looked at: the current candidate, or the author
// of the current timeline post. Fetched details replace the summary when loaded.
func (m model) subject() (Profile, bool) {
	var p Profile
	switch m.active() {
	case tabCandidates:
		if m.idx < 0 || m.idx >= len(m.list) {
			return Profile{}, false
		}
		p = m.list[m.idx]
	default:
		if m.cur < 0 || m.cur >= len(m.items) {
			return Profile{}, false
		}
		p = m.items[m.cur].Author
	}
	if full := m.profiles[p.DID]; full != nil {
		p = *full
	}
	return p, true
}

// prefetch loads the current and next profiles (and recent posts).
func (m model) prefetch() tea.Cmd {
	var cmds []tea.Cmd
	switch m.active() {
	case tabCandidates:
		for i := m.idx; i <= m.idx+1 && i < len(m.list); i++ {
			cmds = append(cmds, m.fetchProfile(m.list[i].DID))
		}
	default:
		if m.inPost {
			for i := m.cur; i <= m.cur+1 && i < len(m.items); i++ {
				cmds = append(cmds, m.fetchProfile(m.items[i].Author.DID))
			}
		}
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m = m.ensureVisible()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case loadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
		}
		m.list, m.ctxs = msg.list, msg.ctxs
		return m, m.prefetch()
	case followingMsg:
		m.followLoading, m.moreBusy = false, false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.items = append(m.items, msg.items...)
		m.tlCursor = msg.cursor
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
		return m.applyAct(msg)
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// applyAct records the outcome of a finished action. All state writes happen
// here, on the UI goroutine, so the view never reads state mid-write.
func (m model) applyAct(msg actMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	e := msg.undo
	if msg.err != nil {
		m.err = msg.err
		if msg.isUndo {
			m.history = append(m.history, e) // still undoable
		}
		m.audit(m.auditEvent(msg))
		return m, nil
	}
	var err error
	did := e.profile.DID
	if msg.isUndo {
		if e.prev != nil {
			err = m.a.st.Put(did, *e.prev)
		} else {
			err = m.a.st.Delete(did)
		}
		delete(m.blockURI, did)
		if msg.newURI != "" {
			m.blockURI[did] = msg.newURI
		}
		if e.tab == tabCandidates && m.active() == tabCandidates {
			m.idx = e.idx
		}
		m.status = fmt.Sprintf("undid %s for @%s", e.decision, e.profile.Handle)
	} else {
		err = m.a.st.Set(did, e.profile.Handle, e.decision)
		delete(m.blockURI, did)
		if e.decision == decBlocked {
			m.blockURI[did] = msg.newURI
		}
		e.blockURI = msg.newURI
		m.history = append(m.history, e)
		if e.tab == tabCandidates && m.active() == tabCandidates {
			m.idx = e.idx + 1
			m.scroll = 0
		}
		m.status = fmt.Sprintf("%s @%s", e.decision, e.profile.Handle)
	}
	m.err = err
	for _, ev := range m.auditEvents(msg) {
		m.audit(ev)
	}
	return m, m.prefetch()
}

// audit appends to the audit log; a failure to log is shown, never swallowed.
func (m *model) audit(ev AuditEvent) {
	if err := m.a.audit.Log(ev); err != nil && m.err == nil {
		m.err = fmt.Errorf("audit log: %w", err)
	}
}

func (m model) source(t tabID) string {
	switch {
	case t == tabFollowing:
		return "tui:following"
	case m.title == "review":
		return "tui:review"
	}
	return "tui:reposts"
}

// via names who reposted the account into your timeline, when that is how it got here.
func (m model) via(e undoEntry) string {
	if e.tab == tabCandidates {
		return strings.Join(m.ctxs[e.profile.DID].By, ", ")
	}
	if e.idx >= 0 && e.idx < len(m.items) && m.items[e.idx].Author.DID == e.profile.DID {
		return m.items[e.idx].By
	}
	return ""
}

var decisionAction = map[string]string{decBlocked: "block", decKept: "keep", decSkipped: "skip"}

// auditEvent is the base event for msg, whether it succeeded or failed.
func (m model) auditEvent(msg actMsg) AuditEvent {
	e := msg.undo
	ev := AuditEvent{
		Action: decisionAction[e.decision], DID: e.profile.DID, Handle: e.profile.Handle,
		Source: m.source(e.tab), Via: m.via(e), Snapshot: snapshotOf(e.profile, m.profiles[e.profile.DID] != nil),
		OK: msg.err == nil, Record: msg.newURI,
	}
	if e.prev != nil {
		ev.Prev = e.prev.Decision
	}
	if msg.err != nil {
		ev.Error = msg.err.Error()
	}
	if msg.isUndo {
		ev.Action, ev.Detail, ev.Record = "undo", "undid "+e.decision, msg.newURI
		if e.decision == decBlocked {
			ev.Record = e.blockURI // the block record that was removed
		}
		ev.Prev = e.decision
	}
	return ev
}

// auditEvents lists what a successful action did. Overriding a block with keep or
// skip lifts the block, which gets its own unblock event.
func (m model) auditEvents(msg actMsg) []AuditEvent {
	e := msg.undo
	main := m.auditEvent(msg)
	if !msg.isUndo && e.prev != nil && e.prev.Decision == decBlocked && e.decision != decBlocked {
		lift := main
		lift.Action, lift.Detail, lift.Record = "unblock", "lifted by "+e.decision, ""
		return []AuditEvent{lift, main}
	}
	return []AuditEvent{main}
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	confirm := m.confirm
	m.confirm = ""
	if m.busy {
		return m, nil
	}
	if key == "tab" && len(m.tabs) > 1 {
		m.tab = (m.tab + 1) % len(m.tabs)
		m.scroll, m.status, m.err = 0, "", nil
		return m, m.prefetch()
	}
	var cmd tea.Cmd
	if m.active() == tabCandidates {
		if m.loading {
			return m, nil
		}
		m, cmd = m.candKey(key)
	} else {
		m, cmd = m.browseKey(key, confirm)
	}
	m.scroll = m.clampScroll(m.scroll)
	return m, cmd
}

// scrollKeys handles ↑/↓/PgUp/PgDn for the right panel.
func (m model) scrollKeys(key string) (model, bool) {
	page := max(m.rightAvail()-2, 1)
	switch key {
	case "up":
		m.scroll--
	case "down":
		m.scroll++
	case "pgup":
		m.scroll -= page
	case "pgdown":
		m.scroll += page
	default:
		return m, false
	}
	return m, true
}

func (m model) candKey(key string) (model, tea.Cmd) {
	if next, ok := m.scrollKeys(key); ok {
		return next, nil
	}
	move := func(i int) (model, tea.Cmd) {
		m.idx = min(max(i, 0), len(m.list))
		m.scroll, m.status, m.err = 0, "", nil
		return m, m.prefetch()
	}
	switch key {
	case "left", "h":
		return move(m.idx - 1)
	case "right", "l":
		return move(m.idx + 1)
	case "home":
		return move(0)
	case "end":
		return move(len(m.list) - 1)
	case "u":
		return m.undo()
	}
	p, ok := m.subject()
	if !ok {
		return m, nil
	}
	switch key {
	case "b":
		return m.decideKey(p, decBlocked)
	case "k":
		return m.decideKey(p, decKept)
	case "s":
		return m.decideKey(p, decSkipped)
	}
	return m, nil
}

func (m model) browseKey(key, confirm string) (model, tea.Cmd) {
	if m.inPost {
		if next, ok := m.scrollKeys(key); ok {
			return next, nil
		}
	}
	setCur := func(i int) {
		m.cur = min(max(i, 0), max(len(m.items)-1, 0))
		m.scroll, m.status, m.err = 0, "", nil
		m = m.ensureVisible()
	}
	rows := max(m.avail()/2, 1)
	switch key {
	case "up":
		setCur(m.cur - 1)
	case "down":
		setCur(m.cur + 1)
	case "pgup":
		setCur(m.cur - rows)
	case "pgdown":
		setCur(m.cur + rows)
	case "home":
		setCur(0)
	case "end":
		setCur(len(m.items) - 1)
	case "left", "h":
		if m.inPost {
			setCur(m.cur - 1)
		}
	case "right", "l":
		if m.inPost {
			setCur(m.cur + 1)
		} else if len(m.items) > 0 {
			m.inPost, m.scroll = true, 0
		}
	case "enter":
		if !m.inPost && len(m.items) > 0 {
			m.inPost, m.scroll = true, 0
		}
	case "esc", "backspace":
		m.inPost, m.scroll = false, 0
		m = m.ensureVisible()
	case "u":
		return m.undo()
	case "k", "b":
		p, ok := m.subject()
		if !ok {
			break
		}
		if key == "k" {
			return m.decideKey(p, decKept)
		}
		if p.DID == m.a.c.DID {
			m.status = "that's you"
			break
		}
		if p.Viewer.Following != "" && confirm != p.DID {
			m.confirm = p.DID
			m.status = fmt.Sprintf("You follow @%s. Press b again to block them.", p.Handle)
			break
		}
		return m.decideKey(p, decBlocked)
	}
	next, more := m.maybeLoadMore()
	return next, tea.Batch(more, next.prefetch())
}

// maybeLoadMore fetches the next timeline page when the cursor nears the end.
func (m model) maybeLoadMore() (model, tea.Cmd) {
	if m.timeline == nil || m.tlCursor == "" || m.moreBusy || m.followLoading || len(m.items)-m.cur > 8 {
		return m, nil
	}
	m.moreBusy = true
	return m, loadTimeline(m.timeline, m.tlCursor)
}

// decideKey applies decision to p, unless that is already recorded.
func (m model) decideKey(p Profile, decision string) (model, tea.Cmd) {
	if e, ok := m.a.st.Entries[p.DID]; ok && e.Decision == decision {
		m.status = fmt.Sprintf("@%s is already %s", p.Handle, decision)
		return m, nil
	}
	return m.decide(p, decision)
}

// decide records decision for p. Blocking (and un-blocking a previously blocked
// account) calls the API first; the state file is only touched in applyAct.
func (m model) decide(p Profile, decision string) (model, tea.Cmd) {
	prev, had := m.a.st.Entries[p.DID]
	var prevP *Entry
	if had {
		prevP = &prev
	}
	idx := m.idx
	if m.active() == tabFollowing {
		idx = m.cur
	}
	e := undoEntry{tab: m.active(), idx: idx, profile: p, decision: decision, prev: prevP}
	wasBlocked := had && prev.Decision == decBlocked
	known := m.blockURI[p.DID]
	if known == "" {
		if fp := m.profiles[p.DID]; fp != nil {
			known = fp.Viewer.Blocking
		}
	}
	c := m.a.c
	m.busy, m.err = true, nil
	return m, func() tea.Msg {
		ctx := context.Background()
		msg := actMsg{undo: e}
		switch {
		case decision == decBlocked:
			msg.newURI, msg.err = c.Block(ctx, p.DID)
		case wasBlocked: // keep/skip on a blocked account lifts the block
			if known == "" {
				if fp, err := c.GetProfile(ctx, p.DID); err == nil {
					known = fp.Viewer.Blocking
				}
			}
			if known != "" {
				msg.err = c.Unblock(ctx, known)
			}
		}
		return msg
	}
}

// undo reverts the latest decision made on the current tab.
func (m model) undo() (model, tea.Cmd) {
	at := -1
	for i := len(m.history) - 1; i >= 0; i-- {
		if m.history[i].tab == m.active() {
			at = i
			break
		}
	}
	if at < 0 {
		m.status = "nothing to undo"
		return m, nil
	}
	e := m.history[at]
	m.history = append(m.history[:at:at], m.history[at+1:]...)
	c := m.a.c
	m.busy, m.err = true, nil
	return m, func() tea.Msg {
		ctx := context.Background()
		msg := actMsg{undo: e, isUndo: true}
		if e.decision == decBlocked && e.blockURI != "" {
			msg.err = c.Unblock(ctx, e.blockURI)
		}
		if msg.err == nil && e.prev != nil && e.prev.Decision == decBlocked && e.decision != decBlocked {
			msg.newURI, msg.err = c.Block(ctx, e.profile.DID) // restore the block we lifted
		}
		return msg
	}
}

// ---------------------------------------------------------------- layout

// avail is the number of body lines that fit between header and footer.
func (m model) avail() int {
	if m.height <= 0 {
		return 1000
	}
	return max(m.height-6, 4)
}

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

// rightLines are the scrollable lines of the right-hand panel for the current screen.
func (m model) rightLines() []string {
	_, rw, _ := m.panelWidths()
	switch {
	case m.active() == tabCandidates && !m.loading && m.idx < len(m.list):
		return m.candRight(rw)
	case m.active() == tabFollowing && m.inPost && m.cur < len(m.items):
		return m.postRight(rw)
	}
	return nil
}

// rightAvail is the height of the scrollable posts area. When the panels are
// stacked it is what remains below the profile.
func (m model) rightAvail() int {
	lw, _, stacked := m.panelWidths()
	if !stacked {
		return m.avail()
	}
	return max(m.avail()-len(m.profilePanel(lw, ""))-1, 3)
}

func (m model) clampScroll(s int) int {
	return min(max(s, 0), max(len(m.rightLines())-m.rightAvail(), 0))
}

// ensureVisible keeps the timeline cursor inside the visible list window.
func (m model) ensureVisible() model {
	rows := max(m.avail()/2, 1)
	if m.cur < m.listTop {
		m.listTop = m.cur
	}
	if m.cur >= m.listTop+rows {
		m.listTop = m.cur - rows + 1
	}
	m.listTop = max(m.listTop, 0)
	return m
}

func styleLines(lines []string, st lipgloss.Style) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = st.Render(l)
	}
	return out
}

// decisionBadge shows what was decided about did earlier, if anything.
func (m model) decisionBadge(did string) string {
	switch m.a.st.Entries[did].Decision {
	case decBlocked:
		return errStyle.Render("● blocked")
	case decKept:
		return okStyle.Render("● kept")
	case decSkipped:
		return dimStyle.Render("● skipped")
	}
	return ""
}

func (m model) profilePanel(cw int, pos string) []string {
	p, _ := m.subject()
	name := p.DisplayName
	if name == "" {
		name = "(no display name)"
	}
	lines := styleLines(wrapText(name, cw-12), headingStyle)
	lines[len(lines)-1] += "  " + dimStyle.Render(pos)
	lines = append(lines, accentStyle.Render("@"+p.Handle),
		dimStyle.Render(osc8("https://bsky.app/profile/"+p.Handle, "open profile ↗")))
	if b := m.decisionBadge(p.DID); b != "" {
		lines = append(lines, b)
	} else if p.Viewer.Following != "" {
		lines = append(lines, dimStyle.Render("● you follow them"))
	}
	lines = append(lines, "")
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

// recentLines lists the account's latest posts in full.
func (m model) recentLines(did string, cw int) []string {
	lines := []string{headingStyle.Render("Recent posts"), ""}
	switch pp := m.posts[did]; {
	case pp == nil:
		return append(lines, m.spin.View()+" loading posts…")
	case len(*pp) == 0:
		return append(lines, dimStyle.Render("(no posts)"))
	default:
		for _, post := range *pp {
			lines = append(lines, renderPost(post, cw, false)...)
		}
	}
	return lines
}

func (m model) candRight(cw int) []string {
	p, _ := m.subject()
	var lines []string
	if rc, ok := m.ctxs[p.DID]; ok {
		lines = append(lines, accentStyle.Render("Reposted by @"+strings.Join(rc.By, ", @")), "")
		lines = append(lines, renderPost(rc.Post, cw, true)...)
	}
	return append(lines, m.recentLines(p.DID, cw)...)
}

func (m model) postRight(cw int) []string {
	it := m.items[m.cur]
	var lines []string
	if it.By != "" {
		lines = append(lines, accentStyle.Render("↻ Reposted by @"+it.By), "")
	}
	lines = append(lines, renderPost(it.Post, cw, true)...)
	return append(lines, m.recentLines(it.Author.DID, cw)...)
}

// window returns n lines of lines starting at off.
func window(lines []string, off, n int) []string {
	if n >= len(lines) {
		return lines
	}
	off = min(max(off, 0), len(lines)-n)
	return lines[off : off+n]
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

func (m model) panelsView(pos string) string {
	lw, _, stacked := m.panelWidths()
	left := m.profilePanel(lw, pos)
	right := window(m.rightLines(), m.scroll, m.rightAvail())
	if stacked {
		return strings.Join(append(append(left, ""), right...), "\n") + "\n"
	}
	return joinColumns(left, right, lw)
}

func (m model) tabBar() string {
	if len(m.tabs) == 1 {
		return titleStyle.Render("bskyban " + m.title)
	}
	parts := []string{titleStyle.Render("bskyban")}
	for i, t := range m.tabs {
		label := m.candLabel
		if t == tabFollowing {
			label = "Following"
		} else if !m.loading {
			label = fmt.Sprintf("%s (%d)", label, len(m.list))
		}
		if i == m.tab {
			parts = append(parts, headingStyle.Underline(true).Render(label))
		} else {
			parts = append(parts, dimStyle.Render(label))
		}
	}
	return strings.Join(parts, "   ")
}

func (m model) hint() string {
	var items []string
	switch {
	case m.active() == tabCandidates:
		items = []string{"← → move", "↑ ↓ scroll", "b block", "k keep", "s skip", "u undo"}
	case m.inPost:
		items = []string{"← → prev/next", "↑ ↓ scroll", "esc back", "b block", "k keep", "u undo"}
	default:
		items = []string{"↑ ↓ move", "enter open", "b block", "k keep", "u undo"}
	}
	if len(m.tabs) > 1 {
		items = append(items, "tab switch")
	}
	items = append(items, "q quit")
	if n, av := len(m.rightLines()), m.rightAvail(); n > av {
		items = append(items, fmt.Sprintf("[%d%%]", (min(m.scroll, n-av)*100)/max(n-av, 1)))
	}
	h := strings.Join(items, " · ")
	if m.width > 0 && lipgloss.Width(h) > m.width {
		h = strings.Join(items, "  ") // tighter on narrow terminals
	}
	return truncateWidth(h, max(m.width, 20))
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(m.tabBar() + "\n\n")
	switch m.active() {
	case tabCandidates:
		switch {
		case m.loading:
			b.WriteString(m.spin.View() + " loading…\n")
		case m.idx >= len(m.list):
			b.WriteString(okStyle.Render("All caught up — nothing new.") + " (← to go back)\n")
		default:
			b.WriteString(m.panelsView(fmt.Sprintf("%d of %d", m.idx+1, len(m.list))))
		}
	default:
		switch {
		case m.followLoading:
			b.WriteString(m.spin.View() + " loading timeline…\n")
		case len(m.items) == 0:
			b.WriteString(dimStyle.Render("Your timeline is empty.") + "\n")
		case m.inPost:
			b.WriteString(m.panelsView(fmt.Sprintf("%d of %d", m.cur+1, len(m.items))))
		default:
			b.WriteString(m.listView())
		}
	}
	b.WriteString("\n")
	if m.err != nil {
		b.WriteString(errStyle.Render("error: "+m.err.Error()) + "\n")
	}
	switch {
	case m.busy:
		b.WriteString(m.spin.View() + " working…\n")
	case m.moreBusy:
		b.WriteString(dimStyle.Render("loading more…") + "\n")
	case m.status != "":
		b.WriteString(warnStyle.Render(m.status) + "\n")
	}
	b.WriteString(dimStyle.Render(m.hint()) + "\n")
	return b.String()
}

// ---------------------------------------------------------------- timeline list

func truncateWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > w-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + "…"
}

func snippet(p Post) string {
	text := strings.Join(strings.Fields(p.Text), " ")
	if text == "" {
		text = strings.TrimSpace(strings.Join(append(append([]string{}, p.Attach...), p.Quote), " "))
	}
	if text == "" {
		text = "(no text)"
	}
	return text
}

func (m model) listView() string {
	w := min(max(m.width-2, 40), 140)
	if m.width <= 0 {
		w = 110
	}
	rows := max(m.avail()/2, 1)
	var b strings.Builder
	for i := m.listTop; i < min(m.listTop+rows, len(m.items)); i++ {
		it := m.items[i]
		age := "?"
		if t, err := time.Parse(time.RFC3339, it.Post.CreatedAt); err == nil {
			age = humanAge(time.Since(t))
		}
		name := it.Author.DisplayName
		if name == "" {
			name = it.Author.Handle
		}
		who := fmt.Sprintf("%s (@%s)", name, it.Author.Handle)
		var tagsPlain, tags []string
		add := func(plain string, st lipgloss.Style) {
			tagsPlain = append(tagsPlain, plain)
			tags = append(tags, st.Render(plain))
		}
		if it.By != "" {
			add("↻ @"+it.By, accentStyle)
		}
		if it.Post.Reply {
			add("reply", dimStyle)
		}
		switch m.a.st.Entries[it.Author.DID].Decision {
		case decBlocked:
			add("blocked", errStyle)
		case decKept:
			add("kept", okStyle)
		}
		tailPlain, tail := strings.Join(tagsPlain, " "), strings.Join(tags, " ")
		fixed := 2 + 5 + 1 + 1 + lipgloss.Width(tailPlain) // marker, age column, spaces
		who = truncateWidth(who, max(w-fixed, 10))
		snip := truncateWidth(snippet(it.Post), w-6)
		var l1, l2 string
		if i == m.cur {
			l1 = selStyle.Width(w).Render(fmt.Sprintf("▸ %-5s %s %s", age, who, tailPlain))
			l2 = selStyle.Width(w).Render("      " + snip)
		} else {
			l1 = fmt.Sprintf("  %s %s %s", dimStyle.Render(fmt.Sprintf("%-5s", age)), titleStyle.Render(who), tail)
			l2 = "      " + dimStyle.Render(snip)
		}
		b.WriteString(l1 + "\n" + l2 + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------- posts

// renderPost shows a post in full: a dim header line, then the wrapped text
// indented. full adds reply context and image links (used for the post under review).
func renderPost(p Post, w int, full bool) []string {
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
	body := func(text string, st *lipgloss.Style, prefix string) {
		for _, l := range wrapText(text, w-len(indent)-len(prefix)) {
			if st != nil {
				l = st.Render(l)
			}
			lines = append(lines, indent+prefix+l)
		}
	}
	if full && p.ParentHandle != "" {
		body("↳ replying to @"+p.ParentHandle+": "+strings.TrimSpace(p.ParentText), &dimStyle, "")
	}
	if text := strings.TrimSpace(p.Text); text != "" {
		body(text, nil, "")
	}
	for _, a := range p.Attach {
		body(a, &dimStyle, "")
	}
	if p.Quote != "" {
		body(p.Quote, &dimStyle, "▎ ")
	}
	if full {
		for i, u := range p.Images {
			body(fmt.Sprintf("image %d: %s", i+1, u), &dimStyle, "")
		}
	}
	if len(lines) == 1 {
		lines = append(lines, indent+"(no text)")
	}
	return append(lines, "")
}
