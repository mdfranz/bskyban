package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
	"github.com/mdfranz/bskyban/internal/termtext"
)

var (
	titleStyle, dimStyle, warnStyle, errStyle    lipgloss.Style
	okStyle, headingStyle, accentStyle, selStyle lipgloss.Style
)

// recentPostCount is how many of an account's latest posts the right panel loads; ↑/↓ scroll through them.
const recentPostCount = 25

type tabID int

const (
	tabFollowing  tabID = iota // your home timeline
	tabCandidates              // new followers (review) or repost authors (feed)
)

type (
	loadedMsg struct {
		list []bsky.Profile
		ctxs map[string]app.RepostCtx
		err  error
	}
	followingMsg struct {
		items  []bsky.FeedItem
		cursor string
		err    error
	}
	refreshTickMsg struct{}
	animTickMsg    struct{} // redraw while a new-post highlight fades (fancy mode)
	newItemsMsg    struct {
		items  []bsky.FeedItem // newest first, none of them already shown or pending
		gap    bool            // more new posts than one check looks at
		manual bool
		err    error
	}
	profileMsg struct {
		p   *bsky.Profile
		did string
		err error
	}
	postsMsg struct {
		did   string
		posts []bsky.Post
		err   error
	}
	postedMsg struct {
		text   string
		uri    string
		reply  *bsky.ReplyRef
		quote  *bsky.PostRef // the post quoted by a comment
		repost bool          // a plain repost: no comment, so no new post of your own
		err    error
	}
	likedMsg struct {
		item  bsky.FeedItem
		like  string // the like record now in place, "" after an unlike
		unlik bool
		err   error
	}
	deletedMsg struct {
		item bsky.FeedItem
		err  error
	}
	actMsg struct {
		undo       undoEntry
		isUndo     bool
		isUnfollow bool
		newURI     string // block/follow record created by this action, if any
		lifted     bool   // an earlier block/follow record was deleted on the way
		err        error
	}
)

type model struct {
	a         *app.App
	title     string
	candLabel string
	loader    loadFunc
	timeline  timelineFunc
	tabs      []tabID
	tab       int

	// candidates tab
	ctxs    map[string]app.RepostCtx // how each candidate reached you (feed mode)
	list    []bsky.Profile
	idx     int
	loading bool

	// following tab
	items         []bsky.FeedItem
	cur           int
	listTop       int
	inPost        bool
	tlCursor      string
	followLoading bool
	moreBusy      bool
	newPosts      []bsky.FeedItem // new posts found by a check, not yet inserted into the list
	newGap        bool            // ...and there were more than a check could fetch
	checking      bool            // a check for new posts is in flight
	applyWhenDone bool            // R was pressed during a check: insert its result when it lands
	refreshErr    error           // last background check failed
	refresh       time.Duration   // how often to check for new posts; 0 = never
	backlog       bool            // include skipped accounts when adding repost authors

	// fancy mode: posts that refresh inserts are highlighted for freshFor
	fancy     bool
	fresh     map[string]time.Time // FeedItem.key() -> when it was inserted
	animating bool                 // an animTickMsg is scheduled
	now       func() time.Time

	pickIndex int             // highlighted link in the picker
	picking   []pickLink      // links offered by o, until one is chosen or it closes
	follows   *followsBrowser // accounts followed by the selected author
	composing bool            // writing a new post (feed mode)
	replyTo   *bsky.FeedItem  // ...as an answer to this post
	quoting   *bsky.FeedItem  // ...or as a comment on (a repost of) this post
	input     textinput.Model // the post being written

	busy     bool
	quitting bool    // wait for the active action to be saved and audited
	pending  *actMsg // remote action completed, local save needs retrying
	spin     spinner.Model
	spinOn   bool                // a spinner tick is scheduled
	clips    map[clipKey]clipped // wrapped list text, by item and width
	profiles map[string]*bsky.Profile
	posts    map[string]*[]bsky.Post // nil value = in flight
	records  map[string]string       // block/follow records behind current decisions (this session)
	history  []undoEntry
	confirm  string // action awaiting confirmation
	scroll   int    // right-panel scroll offset
	status   string
	err      error
	width    int
	height   int
}

// loadFunc returns the candidates to review, plus optional per-candidate context.
type loadFunc func(ctx context.Context) ([]bsky.Profile, map[string]app.RepostCtx, error)

// timelineFunc returns one page of the home timeline and the next cursor.
type timelineFunc func(ctx context.Context, cursor string) ([]bsky.FeedItem, string, error)

func newModel(a *app.App, title string, load loadFunc) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	label := "Reposts"
	if title == "review" {
		label = "New followers"
	}
	return model{
		a: a, title: title, candLabel: label, loader: load, loading: true, spin: sp, spinOn: true, // Init ticks
		clips:    map[clipKey]clipped{},
		tabs:     []tabID{tabCandidates},
		profiles: map[string]*bsky.Profile{}, posts: map[string]*[]bsky.Post{}, records: map[string]string{},
		fresh: map[string]time.Time{}, now: time.Now,
	}
}

// newFeedModel adds a Following tab (your timeline) in front of the repost-author tab.
func newFeedModel(a *app.App, load loadFunc, tl timelineFunc) model {
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
func newPostsModel(a *app.App, tl timelineFunc) model {
	m := newFeedModel(a, func(context.Context) ([]bsky.Profile, map[string]app.RepostCtx, error) { return nil, nil, nil }, tl)
	m.title = "posts"
	m.tabs = []tabID{tabFollowing}
	m.loading = false
	return m
}

func runTUI(m model) error {
	if theme.adaptive() {
		// Ask the terminal for its background now, while nothing else reads from it.
		// Lip Gloss would otherwise ask on the first draw, inside the program, where
		// Bubble Tea's input reader can swallow the reply (or read it as keys) and a
		// silent terminal stalls the first frame for termenv's 5s timeout.
		lipgloss.HasDarkBackground()
	}
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

func (m model) fetchProfile(did string) tea.Cmd {
	if _, ok := m.profiles[did]; ok {
		return nil
	}
	m.profiles[did] = nil // mark in flight
	m.posts[did] = nil
	c := m.a.Client
	return tea.Batch(
		func() tea.Msg {
			p, err := c.GetProfile(context.Background(), did)
			return profileMsg{p, did, err}
		},
		func() tea.Msg {
			posts, err := c.GetRecentPosts(context.Background(), did, recentPostCount)
			return postsMsg{did, posts, err}
		},
	)
}

// subject is the account being looked at: the current candidate, or the author
// of the current timeline post. Fetched details replace the summary when loaded.
func (m model) subject() (bsky.Profile, bool) {
	var p bsky.Profile
	switch {
	case m.follows != nil:
		b := m.follows
		if b.selected < 0 || b.selected >= len(b.accounts) {
			return bsky.Profile{}, false
		}
		p = b.accounts[b.selected]
	case m.active() == tabCandidates:
		if m.idx < 0 || m.idx >= len(m.list) {
			return bsky.Profile{}, false
		}
		p = m.list[m.idx]
	default:
		if m.cur < 0 || m.cur >= len(m.items) {
			return bsky.Profile{}, false
		}
		p = m.items[m.cur].Author
	}
	if full := m.profiles[p.DID]; full != nil {
		p = *full
	}
	return p, true
}

// currentAndNext is did of xs[i] and xs[i+1], whichever exist.
func currentAndNext[T any](xs []T, i int, did func(T) string) []string {
	var out []string
	for j := max(i, 0); j <= i+1 && j < len(xs); j++ {
		out = append(out, did(xs[j]))
	}
	return out
}

// prefetch loads the current and next profiles (and recent posts).
func (m model) prefetch() tea.Cmd {
	var dids []string
	switch {
	case m.follows != nil:
		dids = currentAndNext(m.follows.accounts, m.follows.selected, func(p bsky.Profile) string { return p.DID })
	case m.active() == tabCandidates:
		dids = currentAndNext(m.list, m.idx, func(p bsky.Profile) string { return p.DID })
	case m.inPost:
		dids = currentAndNext(m.items, m.cur, func(it bsky.FeedItem) string { return it.Author.DID })
	}
	cmds := make([]tea.Cmd, len(dids))
	for i, did := range dids {
		cmds[i] = m.fetchProfile(did)
	}
	return tea.Batch(cmds...)
}

// Update handles msg, then starts the spinner if something it shows began loading.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(model); ok && !nm.spinOn && nm.needsSpin() {
		nm.spinOn = true
		return nm, tea.Batch(cmd, nm.spin.Tick)
	}
	return next, cmd
}

// needsSpin reports whether the screen shows a spinner. It only ticks (and so
// redraws the screen) while something is loading.
func (m model) needsSpin() bool {
	if m.loading || m.followLoading || m.busy {
		return true
	}
	if m.follows != nil && m.follows.loading && len(m.follows.accounts) == 0 {
		return true
	}
	if m.follows != nil || m.active() == tabCandidates || m.inPost {
		if p, ok := m.subject(); ok {
			return m.profiles[p.DID] == nil || m.posts[p.DID] == nil
		}
	}
	return false
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width != m.width {
			clear(m.clips) // wrapped for the old width
		}
		m.width, m.height = msg.Width, msg.Height
		m = m.ensureVisible()
	case spinner.TickMsg:
		if !m.needsSpin() {
			m.spinOn = false // nothing is loading: let the tick chain end
			return m, nil
		}
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
	case animTickMsg:
		m.animating = false
		for k, t := range m.fresh {
			if m.now().Sub(t) >= freshFor {
				delete(m.fresh, k)
			}
		}
		return m.startAnim()
	case refreshTickMsg:
		next, check := m.startCheck(false)
		return next, tea.Batch(m.nextTick(), check)
	case newItemsMsg:
		m.checking = false
		manual := msg.manual || m.applyWhenDone
		m.applyWhenDone = false
		if msg.err != nil {
			// A background check retries every interval; log a failure when it starts or
			// changes, not once per tick while the same problem persists.
			repeat := m.refreshErr != nil && m.refreshErr.Error() == msg.err.Error()
			if manual || !repeat {
				detail := "background check"
				if manual {
					detail = "manual refresh (R)"
				}
				m.audit(store.AuditEvent{Action: "refresh", DID: m.a.Client.DID, Handle: m.a.Client.Handle, Source: "tui:following",
					Detail: detail, Error: msg.err.Error()})
			}
			if manual {
				m.err = fmt.Errorf("refresh: %w", msg.err)
			} else {
				m.refreshErr = msg.err
			}
			return m, nil
		}
		m.refreshErr = nil
		m.newPosts = append(append([]bsky.FeedItem{}, msg.items...), m.newPosts...)
		m.newGap = m.newGap || msg.gap
		m = m.mergeCandidates(msg.items)
		if manual || m.readingTop() {
			m = m.applyPending()
			m, anim := m.startAnim()
			return m, tea.Batch(m.prefetch(), anim)
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
	case followsPageMsg:
		return m.applyFollowsPage(msg)
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

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.composing {
		return m.composeKey(k)
	}
	if m.picking != nil && key != "ctrl+c" {
		return m.pickKey(key)
	}
	if m.follows != nil && key != "ctrl+c" {
		return m.followsKey(key)
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
	if key == "u" {
		return m.unfollowKey(confirm)
	}
	if key == "F" {
		return m.openFollows()
	}
	if key == "n" && m.timeline != nil {
		m.composing, m.replyTo, m.quoting, m.status, m.err = true, nil, nil, "", nil
		m.input.SetValue("")
		return m, m.input.Focus()
	}
	if key == "r" && m.timeline != nil {
		it, ok := m.selected()
		if !ok {
			return m, nil
		}
		m.composing, m.replyTo, m.quoting, m.status, m.err = true, &it, nil, "", nil
		m.input.SetValue("")
		return m, m.input.Focus()
	}
	if key == "t" && m.timeline != nil { // share: a comment makes it a quote post, none a plain repost
		it, ok := m.selected()
		if !ok || it.URI == "" {
			return m, nil
		}
		m.composing, m.replyTo, m.quoting, m.status, m.err = true, nil, &it, "", nil
		m.input.SetValue("")
		return m, m.input.Focus()
	}
	if key == "h" && m.timeline != nil { // h for heart; in feed mode the arrow keys move left
		return m.likeKey()
	}
	if key == "o" && m.timeline != nil {
		return m.openKey()
	}
	if key == "i" && m.timeline != nil {
		return m.openImagesKey()
	}
	if key == "R" && m.timeline != nil {
		// R takes you to the top of the feed, where new posts land: the newest is selected.
		m.cur, m.listTop, m.inPost = 0, 0, false
		if m.active() == tabFollowing {
			m.scroll = 0
		}
		if len(m.newPosts) > 0 {
			m = m.applyPending() // what the header announced; no need to wait for or start a check
			m, anim := m.startAnim()
			return m, tea.Batch(m.prefetch(), anim)
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
		if key == "l" && m.timeline != nil {
			return m, nil // in the feed h is the heart, so l goes too; use the arrow keys
		}
		return move(m.idx + 1)
	case "home":
		return move(0)
	case "end":
		return move(len(m.list) - 1)
	case "U":
		return m.undo()
	}
	p, ok := m.subject()
	if !ok {
		return m, nil
	}
	switch key {
	case "b":
		return m.decideKey(p, store.Blocked)
	case "k":
		return m.decideKey(p, store.Kept)
	case "s":
		return m.decideKey(p, store.Skipped)
	case "f":
		return m.decideKey(p, store.Followed)
	}
	return m, nil
}

func (m model) browseKey(key, confirm string) (model, tea.Cmd) {
	if m.inPost { // ↑ ↓ PgUp PgDn scroll the panel, ← → step through posts, like the Reposts tab
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
	case "right":
		if m.inPost {
			setCur(m.cur + 1)
		} else if len(m.items) > 0 {
			m.inPost, m.scroll = true, 0
		}
	case "enter":
		if m.timeline != nil {
			return m.openKey()
		} else if !m.inPost && len(m.items) > 0 {
			m.inPost, m.scroll = true, 0
		}
	case "esc", "backspace":
		m.inPost, m.scroll = false, 0
		m = m.ensureVisible()
	case "U":
		return m.undo()
	case "d":
		return m.deleteKey(confirm)
	case "k", "b", "f":
		p, ok := m.subject()
		if !ok {
			break
		}
		if key == "k" {
			return m.decideKey(p, store.Kept)
		}
		if key == "f" {
			return m.decideKey(p, store.Followed)
		}
		if p.DID == m.a.Client.DID {
			m.status = "that's you"
			break
		}
		if p.Viewer.Following != "" && confirm != p.DID {
			m.confirm = p.DID
			m.status = fmt.Sprintf("You follow @%s. Press b again to block them.", p.Handle)
			break
		}
		return m.decideKey(p, store.Blocked)
	}
	next, more := m.maybeLoadMore()
	return next, tea.Batch(more, next.prefetch())
}

// ---------------------------------------------------------------- layout

// tabBar is the title row; rl (the right panel's lines) gives the scroll position.
func (m model) tabBar(rl []string) string {
	if m.follows != nil {
		return titleStyle.Render("bskyban") + "   " + headingStyle.Render("Accounts followed by @"+termtext.Sanitize(m.follows.actor.Handle)) + m.scrollPos(rl)
	}
	if len(m.tabs) == 1 {
		title := titleStyle.Render("bskyban " + m.title)
		if n := len(m.newPosts); n > 0 {
			title += fmt.Sprintf(" (+%d new)", n)
		}
		return title + m.scrollPos(rl)
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
	return strings.Join(parts, "   ") + flag + m.scrollPos(rl)
}

// hintItems are the key hints for the current screen.
func (m model) hintItems() []string {
	if m.follows != nil {
		if m.err != nil {
			return []string{"R retry", "esc back"}
		}
		return []string{"← → move", "↑ ↓ scroll", "enter open profile", "esc back"}
	}
	if m.pending != nil {
		return []string{"r retry saving (actions and quit wait for a successful save)"}
	}
	if m.picking != nil {
		return []string{"↑ ↓ select", "enter open", "esc cancel"}
	}
	if m.composing {
		if m.replyTo != nil {
			return []string{"enter reply", "esc cancel"}
		}
		if m.quoting != nil {
			return []string{"enter share (no comment: plain repost)", "esc cancel"}
		}
		return []string{"enter post", "esc cancel"}
	}
	var items []string
	switch {
	case m.active() == tabCandidates:
		items = []string{"← → move", "↑ ↓ scroll", "b block", "f follow", "k keep", "s skip", "u unfollow", "U undo"}
	case m.inPost:
		items = []string{"← → prev/next", "↑ ↓ scroll", "esc back", "b block", "f follow", "k keep", "u unfollow", "U undo"}
	default:
		items = []string{"↑ ↓ move", "enter open", "b block", "f follow", "k keep", "u unfollow", "U undo"}
		if m.timeline != nil {
			items[1] = "→ post"
		}
	}
	if len(m.tabs) > 1 {
		items = append(items, "tab switch")
	}
	if m.timeline != nil {
		if _, own := m.ownItem(); own {
			items = append(items, "d delete")
		}
		items = append(items, "r reply", "t share", "h like", "enter/o open link")
		if _, ok := m.postImages(); ok {
			items = append(items, "i open images")
		}
		items = append(items, "n new post", "R refresh")
	}
	items = append(items, "F following")
	return append(items, "q quit")
}

// hintLines lays the hints out on one line if they fit, otherwise wraps them.
func (m model) hintLines() []string { return wrapItems(m.hintItems(), m.width) }

// wrapItems lays items out on one line if they fit in width, otherwise wraps them.
func wrapItems(items []string, width int) []string {
	if width <= 0 {
		return []string{strings.Join(items, " · ")}
	}
	for _, sep := range []string{" · ", "  "} {
		if h := strings.Join(items, sep); lipgloss.Width(h) <= width {
			return []string{h}
		}
	}
	var lines []string
	cur := ""
	for _, it := range items {
		switch {
		case cur == "":
			cur = it
		case lipgloss.Width(cur)+2+lipgloss.Width(it) <= width:
			cur += "  " + it
		default:
			lines = append(lines, cur)
			cur = it
		}
	}
	return append(lines, cur)
}

func (m model) hint() string { return strings.Join(m.hintLines(), "\n") }

// View draws the screen. The status and key hints are pinned to the bottom rows:
// the content above is padded (or, if it is ever too tall, cut) to fit the window.
func (m model) View() string {
	var b strings.Builder
	var rl []string // built once: the tab bar's scroll position and the panels both use it
	if !m.composing {
		rl = m.rightLines()
	}
	b.WriteString(m.tabBar(rl) + "\n\n")
	if m.composing {
		b.WriteString(m.composeView())
	} else {
		b.WriteString(m.content(rl))
	}

	var bottom []string
	if m.err != nil {
		bottom = append(bottom, errStyle.Render("error: "+termtext.Sanitize(m.err.Error())))
	}
	switch {
	case m.busy && m.composing:
		bottom = append(bottom, m.spin.View()+" posting…")
	case m.busy:
		bottom = append(bottom, m.spin.View()+" working…")
	case m.moreBusy:
		bottom = append(bottom, dimStyle.Render("loading more…"))
	case m.picking != nil:
		bottom = append(bottom, strings.Join(m.pickerLines(), "\n"))
	case m.status != "" && !m.composing:
		bottom = append(bottom, warnStyle.Render(m.status))
	}
	bottom = append(bottom, styleLinesJoin(m.hintLines(), dimStyle))

	if m.height <= 0 { // size not known yet
		return b.String() + "\n" + strings.Join(bottom, "\n") + "\n"
	}
	bottom = strings.Split(strings.Join(bottom, "\n"), "\n") // hints may wrap onto several lines
	top := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	room := max(m.height-len(bottom)-1, 1) // one blank row between content and footer
	if len(top) > room {
		top = top[:room]
	}
	for len(top) < room {
		top = append(top, "")
	}
	return strings.Join(append(append(top, ""), bottom...), "\n")
}

// content is the active tab's main area; rl is the right panel from rightLines.
func (m model) content(rl []string) string {
	if m.follows != nil {
		return m.followsView(rl)
	}
	var b strings.Builder
	switch m.active() {
	case tabCandidates:
		switch {
		case m.loading:
			b.WriteString(m.spin.View() + " loading…\n")
		case m.idx >= len(m.list):
			b.WriteString(okStyle.Render("All caught up — nothing new.") + " (← to go back)\n")
		default:
			b.WriteString(m.panelsView(fmt.Sprintf("%d of %d", m.idx+1, len(m.list)), rl))
		}
	default:
		switch {
		case m.followLoading:
			b.WriteString(m.spin.View() + " loading timeline…\n")
		case len(m.items) == 0:
			b.WriteString(dimStyle.Render("Your timeline is empty.") + "\n")
		case m.inPost:
			b.WriteString(m.panelsView(fmt.Sprintf("%d of %d", m.cur+1, len(m.items)), rl))
		default:
			b.WriteString(m.listView())
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- timeline list

// ---------------------------------------------------------------- posts
