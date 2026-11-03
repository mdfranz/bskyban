package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rivo/uniseg"
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
	refreshTickMsg struct{}
	newItemsMsg    struct {
		items  []FeedItem // newest first, none of them already shown or pending
		gap    bool       // more new posts than one check looks at
		manual bool
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
	postedMsg struct {
		text  string
		uri   string
		reply *replyRef
		err   error
	}
	likedMsg struct {
		item  FeedItem
		like  string // the like record now in place, "" after an unlike
		unlik bool
		err   error
	}
	deletedMsg struct {
		item FeedItem
		err  error
	}
	actMsg struct {
		undo   undoEntry
		isUndo bool
		newURI string // block/follow record created by this action, if any
		lifted bool   // an earlier block/follow record was deleted on the way
		err    error
	}
)

type undoEntry struct {
	tab      tabID
	idx      int
	profile  Profile
	decision string
	record   string // block/follow record this decision created
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
	newPosts      []FeedItem    // new posts found by a check, not yet inserted into the list
	newGap        bool          // ...and there were more than a check could fetch
	checking      bool          // a check for new posts is in flight
	applyWhenDone bool          // R was pressed during a check: insert its result when it lands
	refreshErr    error         // last background check failed
	refresh       time.Duration // how often to check for new posts; 0 = never
	backlog       bool          // include skipped accounts when adding repost authors

	composing bool            // writing a new post (feed mode)
	replyTo   *FeedItem       // ...as an answer to this post
	input     textinput.Model // the post being written

	busy     bool
	quitting bool    // wait for the active action to be saved and audited
	pending  *actMsg // remote action completed, local save needs retrying
	spin     spinner.Model
	profiles map[string]*Profile
	posts    map[string]*[]Post // nil value = in flight
	records  map[string]string  // block/follow records behind current decisions (this session)
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
		profiles: map[string]*Profile{}, posts: map[string]*[]Post{}, records: map[string]string{},
	}
}

// newFeedModel adds a Following tab (your timeline) in front of the repost-author tab.
func newFeedModel(a *app, load loadFunc, tl timelineFunc) model {
	m := newModel(a, "feed", load)
	m.timeline = tl
	m.tabs = []tabID{tabFollowing, tabCandidates}
	m.followLoading = true
	m.input = textinput.New()
	m.input.Prompt = ""
	m.input.Placeholder = "What's up?"
	return m
}

// newPostsModel is the feed browser limited to your own posts, with n to write
// one and d to delete the selected one.
func newPostsModel(a *app, tl timelineFunc) model {
	m := newFeedModel(a, func(context.Context) ([]Profile, map[string]repostCtx, error) { return nil, nil, nil }, tl)
	m.title = "posts"
	m.tabs = []tabID{tabFollowing}
	m.loading = false
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
		cmds = append(cmds, loadTimeline(m.timeline, ""), m.nextTick())
	}
	return tea.Batch(cmds...)
}

// nextTick schedules the next background check for new posts (nil if disabled).
func (m model) nextTick() tea.Cmd {
	if m.refresh <= 0 {
		return nil
	}
	return tea.Tick(m.refresh, func(time.Time) tea.Msg { return refreshTickMsg{} })
}

// checkPages bounds how far back one check looks for the overlap with what is shown.
const checkPages = 4

// startCheck fetches the newest timeline pages and finds the posts you do not have yet.
// It only reads; nothing is inserted until the result arrives (and, for the
// background check, until you press R).
func (m model) startCheck(manual bool) (model, tea.Cmd) {
	if m.timeline == nil {
		return m, nil
	}
	if m.followLoading {
		if manual {
			m.status = "still loading the timeline"
		}
		return m, nil
	}
	if m.checking {
		if manual { // a check is already running: show its result as soon as it lands
			m.applyWhenDone = true
			m.status = "checking for new posts…"
		}
		return m, nil
	}
	known := make(map[string]bool, len(m.items)+len(m.newPosts))
	for _, it := range m.items {
		known[it.key()] = true
	}
	for _, it := range m.newPosts {
		known[it.key()] = true
	}
	m.checking = true
	if manual {
		m.status = "checking for new posts…"
	}
	tl := m.timeline
	return m, func() tea.Msg {
		ctx := context.Background()
		var fresh []FeedItem
		got := map[string]bool{}
		cursor := ""
		for page := 0; page < checkPages; page++ {
			items, next, err := tl(ctx, cursor)
			if err != nil {
				return newItemsMsg{err: err, manual: manual}
			}
			overlap := false
			for _, it := range items {
				switch k := it.key(); {
				case known[k]:
					overlap = true
				case !got[k]:
					got[k] = true
					fresh = append(fresh, it)
				}
			}
			if overlap || next == "" {
				return newItemsMsg{items: fresh, manual: manual}
			}
			cursor = next
		}
		return newItemsMsg{items: fresh, gap: true, manual: manual}
	}
}

// applyPending inserts the held-back posts at the top of the list. The post you
// are on stays selected, unless you are at the very top of the list, where the
// newest post becomes the selection.
func (m model) applyPending() model {
	n := len(m.newPosts)
	if n == 0 {
		m.status = "no new posts"
		return m
	}
	atTop := m.cur == 0 && m.listTop == 0 && !m.inPost
	m.items = append(append([]FeedItem{}, m.newPosts...), m.items...)
	if !atTop {
		m.cur += n
		m.listTop += n
	}
	m.status = fmt.Sprintf("%d new posts", n)
	if n == 1 {
		m.status = "1 new post"
	}
	if m.newGap {
		m.status += " (more arrived than could be fetched; the newest are shown)"
	}
	m.newPosts, m.newGap = nil, false
	return m.ensureVisible()
}

// mergeCandidates adds authors of newly reposted posts to the Reposts queue
// (at the end, so the account you are reviewing does not move).
func (m model) mergeCandidates(items []FeedItem) model {
	if m.loading || m.ctxs == nil {
		return m
	}
	for _, it := range items {
		if it.By == "" || !m.a.isNew(it.Author, m.backlog) {
			continue
		}
		rc, seen := m.ctxs[it.Author.DID]
		if !seen {
			m.list = append(m.list, it.Author)
			rc.Post = it.Post
		}
		if !slices.Contains(rc.By, it.By) {
			rc.By = append(rc.By, it.By)
		}
		m.ctxs[it.Author.DID] = rc
	}
	return m
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
	case refreshTickMsg:
		next, check := m.startCheck(false)
		return next, tea.Batch(m.nextTick(), check)
	case newItemsMsg:
		m.checking = false
		manual := msg.manual || m.applyWhenDone
		m.applyWhenDone = false
		if msg.err != nil {
			if manual {
				m.err = fmt.Errorf("refresh: %w", msg.err)
			} else {
				m.refreshErr = msg.err
			}
			return m, nil
		}
		m.refreshErr = nil
		m.newPosts = append(append([]FeedItem{}, msg.items...), m.newPosts...)
		m.newGap = m.newGap || msg.gap
		m = m.mergeCandidates(msg.items)
		if manual {
			m = m.applyPending()
			return m, m.prefetch()
		}
		return m, nil
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
	case postedMsg:
		return m.applyPosted(msg)
	case deletedMsg:
		return m.applyDeleted(msg)
	case likedMsg:
		return m.applyLiked(msg)
	case tea.KeyMsg:
		return m.handleKey(msg)
	default:
		if m.composing { // cursor blink
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
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
		if msg.lifted {
			delete(m.records, e.profile.DID) // that record is gone; look it up afresh next time
		}
		m.audit(m.auditEvent(msg))
		m.quitting = false // show the failure before allowing another quit
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
	} else {
		err = m.a.st.Set(did, e.profile.Handle, e.decision)
	}
	if err != nil {
		m.pending = &msg
		m.quitting = false
		m.err = fmt.Errorf("save decision: %w (press r to retry saving)", err)
		m.status = "action completed; decision has not been saved"
		ev := m.auditEvent(msg)
		ev.OK, ev.Error, ev.Detail = false, m.err.Error(), "action completed; local persistence failed"
		m.audit(ev)
		return m, nil
	}
	m.pending = nil
	if msg.isUndo {
		delete(m.records, did)
		if msg.newURI != "" {
			m.records[did] = msg.newURI
			// Restoring a lifted block/follow creates a different record. The
			// earlier decision must undo that new record, not the deleted one.
			for i := len(m.history) - 1; i >= 0 && e.prev != nil; i-- {
				if m.history[i].profile.DID == did && m.history[i].decision == e.prev.Decision {
					m.history[i].record = msg.newURI
					break
				}
			}
		}
		if e.tab == tabCandidates && m.active() == tabCandidates {
			m.idx = e.idx
		}
		m.status = fmt.Sprintf("undid %s for @%s", e.decision, e.profile.Handle)
	} else {
		delete(m.records, did)
		if isRemote(e.decision) {
			m.records[did] = msg.newURI
		}
		e.record = msg.newURI
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
	if m.quitting && m.err == nil {
		return m, tea.Quit
	}
	m.quitting = false
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

var decisionAction = map[string]string{decBlocked: "block", decFollowed: "follow", decKept: "keep", decSkipped: "skip"}

// liftAction names the audit event for deleting the record behind an overridden decision.
var liftAction = map[string]string{decBlocked: "unblock", decFollowed: "unfollow"}

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
		if isRemote(e.decision) {
			ev.Record = e.record // the block/follow record that was removed
		}
		ev.Prev = e.decision
	}
	return ev
}

// auditEvents lists what a successful action did. Overriding a block or follow
// with another decision deletes that record, which gets its own unblock/unfollow event.
func (m model) auditEvents(msg actMsg) []AuditEvent {
	e := msg.undo
	main := m.auditEvent(msg)
	if !msg.isUndo && e.prev != nil && isRemote(e.prev.Decision) && e.prev.Decision != e.decision {
		lift := main
		lift.Action, lift.Detail, lift.Record = liftAction[e.prev.Decision], "lifted by "+e.decision, ""
		return []AuditEvent{lift, main}
	}
	return []AuditEvent{main}
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.composing {
		return m.composeKey(k)
	}
	switch key {
	case "q", "ctrl+c":
		if m.pending != nil {
			m.status = "decision has not been saved; press r to retry saving before quitting"
			return m, nil
		}
		if m.busy {
			m.quitting = true
			m.status = "finishing action before quitting…"
			return m, nil
		}
		return m, tea.Quit
	}
	if m.pending != nil {
		if key == "r" {
			return m.applyAct(*m.pending)
		}
		return m, nil
	}
	confirm := m.confirm
	m.confirm = ""
	if m.busy {
		return m, nil
	}
	if key == "n" && m.timeline != nil {
		m.composing, m.replyTo, m.status, m.err = true, nil, "", nil
		m.input.SetValue("")
		return m, m.input.Focus()
	}
	if key == "r" && m.timeline != nil && m.active() == tabFollowing {
		it, ok := m.selected()
		if !ok {
			return m, nil
		}
		m.composing, m.replyTo, m.status, m.err = true, &it, "", nil
		m.input.SetValue("")
		return m, m.input.Focus()
	}
	if key == "L" && m.timeline != nil && m.active() == tabFollowing {
		return m.likeKey()
	}
	if key == "R" && m.timeline != nil {
		if len(m.newPosts) > 0 {
			m = m.applyPending() // what the header announced; no need to wait for or start a check
			m.scroll = 0
			return m, m.prefetch()
		}
		return m.startCheck(true)
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

// composeKey edits the post being written: enter publishes it, esc discards it.
func (m model) composeKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "ctrl+c" {
		if m.busy {
			m.quitting = true
			m.status = "finishing action before quitting…"
			return m, nil
		}
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}
	switch key {
	case "esc":
		m.composing, m.replyTo, m.err = false, nil, nil
		m.input.Blur()
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		if n := uniseg.GraphemeClusterCount(text); n > maxPostGraphemes {
			m.err = fmt.Errorf("post is %d characters; the limit is %d", n, maxPostGraphemes)
			return m, nil
		}
		m.busy, m.err = true, nil
		c := m.a.c
		var reply *replyRef
		if m.replyTo != nil {
			reply = m.replyTo.replyTo()
		}
		return m, func() tea.Msg {
			uri, err := c.Post(context.Background(), text, reply)
			return postedMsg{text: text, uri: uri, reply: reply, err: err}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	m.err = nil
	return m, cmd
}

// applyPosted logs a finished post and pulls it into the timeline.
func (m model) applyPosted(msg postedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	ev := AuditEvent{Action: "post", DID: m.a.c.DID, Handle: m.a.c.Handle, Source: "tui:following", OK: msg.err == nil, Record: msg.uri,
		Detail: truncateWidth(strings.Join(strings.Fields(msg.text), " "), 200)}
	if msg.reply != nil {
		ev.Via = "reply to " + msg.reply.Parent.URI
	}
	if msg.err != nil {
		ev.Error = msg.err.Error()
		m.audit(ev)
		m.err = msg.err // the text stays in the box so nothing typed is lost
		m.quitting = false
		return m, nil
	}
	m.audit(ev)
	m.composing, m.replyTo = false, nil
	m.input.Blur()
	m.input.SetValue("")
	if m.quitting && m.err == nil {
		return m, tea.Quit
	}
	m.quitting = false
	m, cmd := m.startCheck(true) // shows the new post at the top
	m.status = "posted " + postURL(msg.uri)
	return m, cmd
}

// likeKey likes the selected post, or removes your like if you already liked it.
func (m model) likeKey() (model, tea.Cmd) {
	it, ok := m.selected()
	if !ok || it.URI == "" {
		return m, nil
	}
	m.busy, m.err, m.status = true, nil, ""
	c := m.a.c
	return m, func() tea.Msg {
		ctx := context.Background()
		if it.Like != "" {
			return likedMsg{item: it, unlik: true, err: c.Unlike(ctx, it.Like)}
		}
		uri, err := c.Like(ctx, postRef{it.URI, it.CID})
		return likedMsg{item: it, like: uri, err: err}
	}
}

// applyLiked logs a like or unlike and marks the post (every copy of it) in the lists.
func (m model) applyLiked(msg likedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	it := msg.item
	ev := AuditEvent{Action: "like", DID: m.a.c.DID, Handle: m.a.c.Handle, Source: "tui:following", OK: msg.err == nil,
		Record: msg.like, Detail: it.URI, Via: "@" + it.Author.Handle}
	if msg.unlik {
		ev.Action, ev.Record = "unlike", it.Like
	}
	if msg.err != nil {
		ev.Error = msg.err.Error()
		m.audit(ev)
		m.err = msg.err
		m.quitting = false
		return m, nil
	}
	m.audit(ev)
	for _, list := range [][]FeedItem{m.items, m.newPosts} {
		for i := range list {
			if list[i].URI == it.URI {
				list[i].Like = msg.like
			}
		}
	}
	m.status = "liked"
	if msg.unlik {
		m.status = "removed your like"
	}
	if m.quitting {
		return m, tea.Quit
	}
	return m, nil
}

// selected is the timeline post under the cursor.
func (m model) selected() (FeedItem, bool) {
	if m.cur >= len(m.items) {
		return FeedItem{}, false
	}
	return m.items[m.cur], true
}

// ownItem is the selected timeline post when it is yours.
func (m model) ownItem() (FeedItem, bool) {
	if m.active() != tabFollowing || m.cur >= len(m.items) {
		return FeedItem{}, false
	}
	it := m.items[m.cur]
	return it, it.Author.DID == m.a.c.DID && it.URI != ""
}

// deleteKey deletes your own selected post, after a second press of d.
func (m model) deleteKey(confirm string) (model, tea.Cmd) {
	it, ok := m.ownItem()
	if !ok {
		if m.active() == tabFollowing && m.timeline != nil {
			m.status = "you can only delete your own posts"
		}
		return m, nil
	}
	if confirm != "del:"+it.URI {
		m.confirm = "del:" + it.URI
		m.status = "Press d again to delete this post."
		return m, nil
	}
	m.busy, m.err = true, nil
	c := m.a.c
	return m, func() tea.Msg {
		return deletedMsg{item: it, err: c.DeletePost(context.Background(), it.URI)}
	}
}

// applyDeleted logs a deletion and drops the post from the list.
func (m model) applyDeleted(msg deletedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	it := msg.item
	ev := AuditEvent{Action: "delete-post", DID: m.a.c.DID, Handle: m.a.c.Handle, Source: "tui:following", OK: msg.err == nil, Record: it.URI,
		Detail: truncateWidth(strings.Join(strings.Fields(it.Post.Text), " "), 200)}
	if msg.err != nil {
		ev.Error = msg.err.Error()
		m.audit(ev)
		m.err = msg.err
		m.quitting = false
		return m, nil
	}
	m.audit(ev)
	keep := func(items []FeedItem) []FeedItem {
		return slices.DeleteFunc(slices.Clone(items), func(x FeedItem) bool { return x.URI == it.URI })
	}
	removedBefore := 0
	for i, x := range m.items {
		if x.URI == it.URI && i < m.cur {
			removedBefore++
		}
	}
	m.items, m.newPosts = keep(m.items), keep(m.newPosts)
	m.cur = min(max(m.cur-removedBefore, 0), max(len(m.items)-1, 0))
	if len(m.items) == 0 {
		m.inPost = false
	}
	m.listTop, m.scroll = min(m.listTop, m.cur), 0
	m = m.ensureVisible()
	m.status = "deleted post"
	if m.quitting {
		return m, tea.Quit
	}
	return m, m.prefetch()
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
	case "f":
		return m.decideKey(p, decFollowed)
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
	rows := max(m.avail()/(previewLines+2), 1)
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
	case "d":
		return m.deleteKey(confirm)
	case "k", "b", "f":
		p, ok := m.subject()
		if !ok {
			break
		}
		if key == "k" {
			return m.decideKey(p, decKept)
		}
		if key == "f" {
			return m.decideKey(p, decFollowed)
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
	if decision == decFollowed && p.Viewer.Following != "" {
		m.status = fmt.Sprintf("you already follow @%s", p.Handle)
		return m, nil
	}
	return m.decide(p, decision)
}

// decide records decision for p. Blocking or following (and lifting an earlier
// block or follow when it is overridden) calls the API first; the state file is
// only touched in applyAct.
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
	lift := had && isRemote(prev.Decision) && prev.Decision != decision
	known := ""
	if lift {
		known = m.records[p.DID]
		if known == "" {
			if fp := m.profiles[p.DID]; fp != nil {
				known = viewerURI(fp, prev.Decision)
			}
		}
	}
	c := m.a.c
	m.busy, m.err = true, nil
	return m, func() tea.Msg {
		ctx := context.Background()
		msg := actMsg{undo: e}
		if lift { // e.g. keep/skip on a blocked account lifts the block
			if known == "" {
				if fp, err := c.GetProfile(ctx, p.DID); err == nil {
					known = viewerURI(fp, prev.Decision)
				}
			}
			if known != "" {
				if msg.err = c.deleteFor(ctx, prev.Decision, known); msg.err != nil {
					return msg
				}
			}
			msg.lifted = true
		}
		if isRemote(decision) {
			msg.newURI, msg.err = c.createFor(ctx, decision, p.DID)
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
		if isRemote(e.decision) && e.record != "" {
			msg.err = c.deleteFor(ctx, e.decision, e.record)
		}
		if msg.err == nil && e.prev != nil && isRemote(e.prev.Decision) && e.prev.Decision != e.decision {
			msg.newURI, msg.err = c.createFor(ctx, e.prev.Decision, e.profile.DID) // restore what we lifted
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
	return max(m.height-6-(len(m.hintLines())-1), 4)
}

// panelWidths splits the terminal width into left (profile) and right (posts)
// widths. stacked is true when the window is too narrow for two columns.
func (m model) panelWidths() (left, right int, stacked bool) {
	w := m.width
	if w <= 0 {
		w = 110
	}
	if w < 96 {
		return w - 2, w - 2, true
	}
	// The profile gets 40% (at least 44, at most 80 columns - its content never
	// needs more); the posts take everything else, so the layout fills any width.
	l := min(max(w*2/5, 44), 80)
	return l, w - l - panelGap, false
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

// ensureVisible scrolls the timeline list so the selected item is fully on screen.
func (m model) ensureVisible() model {
	if len(m.items) == 0 {
		return m
	}
	m.cur = min(max(m.cur, 0), len(m.items)-1)
	m.listTop = min(m.listTop, m.cur)
	w := m.listWidth()
	for m.listTop < m.cur {
		h := 0
		for i := m.listTop; i <= m.cur; i++ {
			h += len(m.itemLines(i, w))
		}
		if h <= m.avail() {
			break
		}
		m.listTop++
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
	case decFollowed:
		return okStyle.Render("● followed")
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
	if it.Like != "" {
		lines = append(lines, "", errStyle.Render("♥ you liked this"))
	}
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

// scrollPos is the posts-panel scroll position as "[34%]", or "" if everything fits.
func (m model) scrollPos() string {
	n, av := len(m.rightLines()), m.rightAvail()
	if n <= av {
		return ""
	}
	return dimStyle.Render(fmt.Sprintf("   [%d%%]", (min(m.scroll, n-av)*100)/max(n-av, 1)))
}

func (m model) tabBar() string {
	if len(m.tabs) == 1 {
		title := titleStyle.Render("bskyban " + m.title)
		if n := len(m.newPosts); n > 0 {
			title += fmt.Sprintf(" (+%d new)", n)
		}
		return title + m.scrollPos()
	}
	parts := []string{titleStyle.Render("bskyban")}
	for i, t := range m.tabs {
		label := m.candLabel
		if t == tabFollowing {
			label = "Following"
			if n := len(m.newPosts); n > 0 {
				label += fmt.Sprintf(" (+%d new)", n)
			}
		} else if !m.loading {
			label = fmt.Sprintf("%s (%d)", label, len(m.list))
		}
		if i == m.tab {
			parts = append(parts, headingStyle.Underline(true).Render(label))
		} else {
			parts = append(parts, dimStyle.Render(label))
		}
	}
	flag := ""
	if m.refreshErr != nil {
		flag = warnStyle.Render("   ⚠ refresh failed")
	}
	return strings.Join(parts, "   ") + flag + m.scrollPos()
}

// hintItems are the key hints for the current screen.
func (m model) hintItems() []string {
	if m.pending != nil {
		return []string{"r retry saving (actions and quit wait for a successful save)"}
	}
	if m.composing {
		if m.replyTo != nil {
			return []string{"enter reply", "esc cancel"}
		}
		return []string{"enter post", "esc cancel"}
	}
	var items []string
	switch {
	case m.active() == tabCandidates:
		items = []string{"← → move", "↑ ↓ scroll", "b block", "f follow", "k keep", "s skip", "u undo"}
	case m.inPost:
		items = []string{"← → prev/next", "↑ ↓ scroll", "esc back", "b block", "f follow", "k keep", "u undo"}
	default:
		items = []string{"↑ ↓ move", "enter open", "b block", "f follow", "k keep", "u undo"}
	}
	if len(m.tabs) > 1 {
		items = append(items, "tab switch")
	}
	if m.timeline != nil {
		if _, own := m.ownItem(); own {
			items = append(items, "d delete")
		}
		if m.active() == tabFollowing {
			items = append(items, "r reply", "L like")
		}
		items = append(items, "n new post", "R refresh")
	}
	return append(items, "q quit")
}

// hintLines lays the hints out on one line if they fit, otherwise wraps them.
func (m model) hintLines() []string {
	items := m.hintItems()
	if m.width <= 0 {
		return []string{strings.Join(items, " · ")}
	}
	for _, sep := range []string{" · ", "  "} {
		if h := strings.Join(items, sep); lipgloss.Width(h) <= m.width {
			return []string{h}
		}
	}
	var lines []string
	cur := ""
	for _, it := range items {
		switch {
		case cur == "":
			cur = it
		case lipgloss.Width(cur)+2+lipgloss.Width(it) <= m.width:
			cur += "  " + it
		default:
			lines = append(lines, cur)
			cur = it
		}
	}
	return append(lines, cur)
}

func (m model) hint() string { return strings.Join(m.hintLines(), "\n") }

func (m model) View() string {
	var b strings.Builder
	b.WriteString(m.tabBar() + "\n\n")
	if m.composing {
		b.WriteString(m.composeView())
		return b.String()
	}
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
		b.WriteString(errStyle.Render("error: "+terminalText(m.err.Error())) + "\n")
	}
	switch {
	case m.busy:
		b.WriteString(m.spin.View() + " working…\n")
	case m.moreBusy:
		b.WriteString(dimStyle.Render("loading more…") + "\n")
	case m.status != "":
		b.WriteString(warnStyle.Render(m.status) + "\n")
	}
	b.WriteString(styleLinesJoin(m.hintLines(), dimStyle) + "\n")
	return b.String()
}

// composeView is the new-post screen: the text box, a length counter and any error.
func (m model) composeView() string {
	var b strings.Builder
	if r := m.replyTo; r != nil {
		b.WriteString(headingStyle.Render("Reply to @"+terminalText(r.Author.Handle)) + "\n")
		quote := strings.Join(strings.Fields(terminalText(r.Post.Text)), " ")
		if w := m.width - 4; w > 10 {
			quote = truncateWidth(quote, w)
		}
		b.WriteString(dimStyle.Render("▎ "+quote) + "\n\n")
	} else {
		b.WriteString(headingStyle.Render("New post") + "\n\n")
	}
	in := m.input
	if m.width > 6 {
		in.Width = m.width - 4
	}
	b.WriteString(in.View() + "\n\n")
	n := uniseg.GraphemeClusterCount(in.Value())
	count := fmt.Sprintf("%d/%d", n, maxPostGraphemes)
	if n > maxPostGraphemes {
		count = errStyle.Render(count)
	} else {
		count = dimStyle.Render(count)
	}
	b.WriteString(count + "\n\n")
	if m.err != nil {
		b.WriteString(errStyle.Render("error: "+terminalText(m.err.Error())) + "\n")
	}
	if m.busy {
		b.WriteString(m.spin.View() + " posting…\n")
	}
	b.WriteString(styleLinesJoin(m.hintLines(), dimStyle) + "\n")
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

// previewLines is how many lines of each post the timeline list shows.
const previewLines = 3

// preview is the text shown for a post in the list: its text, then any
// attachments and quoted post, so image-only posts still say something.
func preview(p Post) string {
	parts := []string{strings.Join(strings.Fields(p.Text), " ")}
	parts = append(parts, p.Attach...)
	if p.Quote != "" {
		parts = append(parts, "▎ "+p.Quote)
	}
	text := strings.Join(strings.Fields(strings.Join(parts, " ")), " ") // one paragraph, no stray line breaks
	if text == "" {
		text = "(no text)"
	}
	return text
}

func (m model) listWidth() int {
	if m.width <= 0 {
		return 110
	}
	return max(m.width-2, 40)
}

// itemLines renders timeline item i: a header line, up to previewLines lines of
// text (ending in … if there is more), and a blank separator line.
func (m model) itemLines(i, w int) []string {
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
	if it.Like != "" {
		add("♥", errStyle)
	}
	switch m.a.st.Entries[it.Author.DID].Decision {
	case decBlocked:
		add("blocked", errStyle)
	case decFollowed:
		add("followed", okStyle)
	case decKept:
		add("kept", okStyle)
	}
	tailPlain, tail := strings.Join(tagsPlain, " "), strings.Join(tags, " ")
	who = truncateWidth(who, max(w-(2+5+1+1+lipgloss.Width(tailPlain)), 10))

	// plain, hard-wrapped preview text (links are not needed in the list)
	body := strings.Split(oscRe.ReplaceAllString(strings.Join(wrapText(preview(it.Post), w-6), "\n"), ""), "\n")
	if len(body) > previewLines {
		body = body[:previewLines]
		body[previewLines-1] = truncateWidth(body[previewLines-1]+" …", w-6)
	}

	var lines []string
	if i == m.cur {
		lines = append(lines, selStyle.Width(w).Render(fmt.Sprintf("▸ %-5s %s %s", age, who, tailPlain)))
		for _, l := range body {
			lines = append(lines, selStyle.Width(w).Render("      "+l))
		}
	} else {
		lines = append(lines, fmt.Sprintf("  %s %s %s", dimStyle.Render(fmt.Sprintf("%-5s", age)), titleStyle.Render(who), tail))
		for _, l := range body {
			lines = append(lines, "      "+dimStyle.Render(l))
		}
	}
	return append(lines, "")
}

func (m model) listView() string {
	w, used := m.listWidth(), 0
	var b strings.Builder
	for i := m.listTop; i < len(m.items); i++ {
		lines := m.itemLines(i, w)
		if used > 0 && used+len(lines) > m.avail() {
			break
		}
		b.WriteString(strings.Join(lines, "\n") + "\n")
		used += len(lines)
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

func styleLinesJoin(lines []string, st lipgloss.Style) string {
	return strings.Join(styleLines(lines, st), "\n")
}
