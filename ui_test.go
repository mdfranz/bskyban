package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// fakePDS records the write calls the UI makes.
type fakePDS struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakePDS) log(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }

func (f *fakePDS) joined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

func newTestApp(t *testing.T) (*app, *fakePDS) {
	t.Helper()
	f := &fakePDS{}
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xrpc/com.atproto.repo.createRecord":
			n++
			f.log("block")
			fmt.Fprintf(w, `{"uri":"at://did:me/app.bsky.graph.block/rk%d"}`, n)
		case "/xrpc/com.atproto.repo.deleteRecord":
			f.log("unblock")
			fmt.Fprint(w, `{}`)
		case "/xrpc/app.bsky.actor.getProfile":
			fmt.Fprint(w, `{"did":"x","handle":"x","viewer":{"blocking":"at://did:me/app.bsky.graph.block/rk0"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	c.DID = "did:me"
	st := &State{path: t.TempDir() + "/state.json", Entries: map[string]Entry{}}
	return &app{c: c, st: st, audit: NewAudit(t.TempDir() + "/audit.jsonl")}, f
}

func key(s string) tea.KeyMsg {
	switch s {
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press sends a key and, if it started an API action, runs it and feeds the result back.
func press(m model, k string) model {
	next, cmd := m.Update(key(k))
	m = next.(model)
	if cmd != nil {
		if am, ok := cmd().(actMsg); ok {
			next, _ = m.Update(am)
			m = next.(model)
		}
	}
	return m
}

func candidates(t *testing.T) (model, *fakePDS) {
	a, f := newTestApp(t)
	m := newModel(a, "review", nil)
	m.loading = false
	m.width, m.height = 120, 40
	for _, h := range []string{"a", "b", "c"} {
		m.list = append(m.list, Profile{DID: "did:" + h, Handle: h + ".bsky.social", DisplayName: h})
	}
	return m, f
}

func TestViewTwoPanels(t *testing.T) {
	m, _ := candidates(t)
	p := Profile{DID: "did:a", Handle: "junoview.bsky.social", DisplayName: "Junoview",
		Description: "A modern presentation editor. junoview.com", PostsCount: 43, FollowersCount: 40, FollowsCount: 580}
	m.list = []Profile{p}
	m.profiles[p.DID] = &p
	posts := []Post{{Text: "Code boxes in 13 colour themes. Free, in your browser: junoview.com #Python", CreatedAt: "2026-11-01T06:00:00Z"},
		{Text: "reply text", Reply: true, CreatedAt: "2026-11-01T06:00:00Z"}}
	m.posts[p.DID] = &posts

	v := m.View()
	if !strings.Contains(v, "\x1b]8;;https://bsky.app/profile/junoview.bsky.social") {
		t.Error("profile link missing")
	}
	if strings.Count(v, "bsky.app/profile/junoview.bsky.social") != 1 {
		t.Error("profile URL appears more than once (nested link?)")
	}
	v = regexp.MustCompile(`\x1b\]8;;[^\x1b]*\x1b\\`).ReplaceAllString(v, "")
	for _, w := range []string{"Junoview", "Recent posts", "Code boxes", "Followers", "← → move"} {
		if !strings.Contains(v, w) {
			t.Errorf("view missing %q", w)
		}
	}
	side := false
	for i, line := range strings.Split(v, "\n") {
		if lipgloss.Width(line) > 120 {
			t.Errorf("line %d is %d wide (>120)", i, lipgloss.Width(line))
		}
		if strings.Contains(line, "Followers") && strings.Contains(line, "reply text") {
			side = true
		}
	}
	if !side {
		t.Error("panels are not side by side")
	}
}

func TestArrowNavigationDoesNotDecide(t *testing.T) {
	m, f := candidates(t)
	m = press(m, "right")
	m = press(m, "right")
	if m.idx != 2 {
		t.Fatalf("idx = %d, want 2", m.idx)
	}
	m = press(m, "left")
	m = press(m, "h")
	if m.idx != 0 {
		t.Fatalf("idx = %d, want 0", m.idx)
	}
	m = press(m, "left") // clamped at the start
	if m.idx != 0 {
		t.Fatalf("idx = %d after left at start", m.idx)
	}
	m = press(m, "end")
	if m.idx != 2 {
		t.Fatalf("end -> idx = %d", m.idx)
	}
	m = press(m, "home")
	if m.idx != 0 {
		t.Fatalf("home -> idx = %d", m.idx)
	}
	if len(m.a.st.Entries) != 0 || f.joined() != "" {
		t.Errorf("navigation recorded something: %v / %s", m.a.st.Entries, f.joined())
	}
}

func TestScrollClampsAndResets(t *testing.T) {
	m, _ := candidates(t)
	m.height = 14 // small window: avail = 8
	var posts []Post
	for i := 0; i < 10; i++ {
		posts = append(posts, Post{Text: fmt.Sprintf("post number %d", i), CreatedAt: "2026-11-01T06:00:00Z"})
	}
	m.posts["did:a"] = &posts
	m.profiles["did:a"] = &m.list[0]
	total := len(m.rightLines())
	if total <= m.avail() {
		t.Fatalf("test needs overflow: %d lines, avail %d", total, m.avail())
	}
	for i := 0; i < total+20; i++ {
		m = press(m, "down")
	}
	if want := total - m.avail(); m.scroll != want {
		t.Errorf("scroll = %d, want clamp at %d", m.scroll, want)
	}
	m = press(m, "pgdown")
	if m.scroll != total-m.avail() {
		t.Errorf("pgdown overshot: %d", m.scroll)
	}
	for i := 0; i < total+20; i++ {
		m = press(m, "up")
	}
	if m.scroll != 0 {
		t.Errorf("scroll = %d, want 0", m.scroll)
	}
	m = press(m, "down")
	m = press(m, "right")
	if m.scroll != 0 {
		t.Errorf("moving to next follower should reset scroll, got %d", m.scroll)
	}
}

func TestRevisitShowsDecisionAndLettersOverride(t *testing.T) {
	m, f := candidates(t)
	m = press(m, "b") // block a, advance to b
	if m.idx != 1 || m.a.st.Entries["did:a"].Decision != decBlocked {
		t.Fatalf("after b: idx=%d entries=%v", m.idx, m.a.st.Entries)
	}
	m = press(m, "left") // revisit a
	if v := m.View(); !strings.Contains(v, "blocked") {
		t.Error("revisited follower should show its blocked badge")
	}
	m = press(m, "b")
	if !strings.Contains(m.status, "already blocked") || f.joined() != "block" {
		t.Errorf("repeat block should be a no-op, status=%q calls=%s", m.status, f.joined())
	}
	m = press(m, "k") // override: keep lifts the block
	if got := m.a.st.Entries["did:a"].Decision; got != decKept {
		t.Errorf("decision = %q, want kept", got)
	}
	if f.joined() != "block,unblock" {
		t.Errorf("calls = %s, want block,unblock", f.joined())
	}
	m = press(m, "u") // undo the keep: the earlier block is restored
	if got := m.a.st.Entries["did:a"].Decision; got != decBlocked {
		t.Errorf("after undo decision = %q, want blocked", got)
	}
	if f.joined() != "block,unblock,block" {
		t.Errorf("calls = %s, want the block re-created", f.joined())
	}
}

func TestUndoBlockRemovesBlock(t *testing.T) {
	m, f := candidates(t)
	m = press(m, "b")
	m = press(m, "u")
	if _, ok := m.a.st.Entries["did:a"]; ok {
		t.Error("undo should clear the decision")
	}
	if m.idx != 0 || f.joined() != "block,unblock" {
		t.Errorf("idx=%d calls=%s", m.idx, f.joined())
	}
}

func followingModel(t *testing.T) (model, *fakePDS) {
	a, f := newTestApp(t)
	m := newFeedModel(a, nil, nil)
	m.loading, m.followLoading = false, false
	m.width, m.height = 120, 40
	m.items = []FeedItem{
		{Author: Profile{DID: "did:friend", Handle: "friend.bsky.social", DisplayName: "Friend", Viewer: Profile{}.Viewer},
			Post: Post{Text: "hello world", CreatedAt: "2026-11-01T06:00:00Z", ParentHandle: "x.bsky.social", ParentText: "parent text", Reply: true}},
		{Author: Profile{DID: "did:rando", Handle: "rando.bsky.social", DisplayName: "Rando"},
			Post: Post{Text: "buy now", CreatedAt: "2026-11-01T05:00:00Z", Quote: "@q.bsky.social: quoted", Images: []string{"https://cdn.bsky.app/img/1.jpg"}},
			By:   "friend.bsky.social"},
	}
	m.items[0].Author.Viewer.Following = "at://follow/1"
	return m, f
}

func TestFollowingListAndPostView(t *testing.T) {
	m, _ := followingModel(t)
	v := m.View()
	for _, w := range []string{"Following", "Friend (@friend.bsky.social)", "hello world", "↻ @friend.bsky.social", "enter open"} {
		if !strings.Contains(v, w) {
			t.Errorf("list view missing %q:\n%s", w, v)
		}
	}
	m = press(m, "down")
	if m.cur != 1 {
		t.Fatalf("cur = %d", m.cur)
	}
	m = press(m, "enter")
	if !m.inPost {
		t.Fatal("enter should open the post")
	}
	v = regexp.MustCompile(`\x1b\][^\x1b]*\x1b\\`).ReplaceAllString(m.View(), "")
	for _, w := range []string{"Rando", "buy now", "Reposted by @friend.bsky.social", "▎ @q.bsky.social: quoted", "image 1: https://cdn.bsky.app/img/1.jpg", "esc back"} {
		if !strings.Contains(v, w) {
			t.Errorf("post view missing %q:\n%s", w, v)
		}
	}
	m = press(m, "left")
	if m.cur != 0 || !m.inPost {
		t.Errorf("left in post view should show the previous post: cur=%d inPost=%v", m.cur, m.inPost)
	}
	if v := oscRe.ReplaceAllString(m.View(), ""); !strings.Contains(v, "replying to @x.bsky.social: parent text") {
		t.Errorf("reply context missing:\n%s", v)
	}
	m = press(m, "esc")
	if m.inPost {
		t.Error("esc should go back to the list")
	}
}

func TestBlockingSomeoneYouFollowNeedsConfirmation(t *testing.T) {
	m, f := followingModel(t)
	m = press(m, "b") // friend, whom we follow
	if f.joined() != "" || !strings.Contains(m.status, "Press b again") {
		t.Fatalf("first b should only ask: status=%q calls=%s", m.status, f.joined())
	}
	m = press(m, "down") // anything else cancels the confirmation
	m = press(m, "up")
	m = press(m, "b")
	if f.joined() != "" {
		t.Fatal("confirmation should not survive another key")
	}
	m = press(m, "b")
	if f.joined() != "block" || m.a.st.Entries["did:friend"].Decision != decBlocked {
		t.Errorf("second b should block: calls=%s entries=%v", f.joined(), m.a.st.Entries)
	}
	if m.cur != 0 {
		t.Errorf("timeline cursor should stay put, cur=%d", m.cur)
	}
}

func TestBlockStrangerInTimelineAndTabSwitch(t *testing.T) {
	m, f := followingModel(t)
	m = press(m, "down")
	m = press(m, "b") // rando: not followed, no confirmation
	if f.joined() != "block" {
		t.Fatalf("calls = %s", f.joined())
	}
	if v := m.View(); !strings.Contains(v, "blocked") {
		t.Error("list should flag the blocked author")
	}
	m = press(m, "tab")
	if m.active() != tabCandidates {
		t.Fatal("tab should switch to the candidates tab")
	}
	m = press(m, "tab")
	if m.active() != tabFollowing {
		t.Fatal("tab should switch back")
	}
}

func TestListWindowFollowsCursor(t *testing.T) {
	m, _ := followingModel(t)
	m.height = 12 // avail 6 -> 3 rows
	m.items = nil
	for i := 0; i < 20; i++ {
		m.items = append(m.items, FeedItem{Author: Profile{DID: fmt.Sprint(i), Handle: fmt.Sprintf("u%d.bsky.social", i)},
			Post: Post{Text: fmt.Sprintf("p%d", i)}})
	}
	for i := 0; i < 10; i++ {
		m = press(m, "down")
	}
	if m.cur != 10 || m.cur < m.listTop || m.cur >= m.listTop+3 {
		t.Errorf("cursor %d outside window [%d,%d)", m.cur, m.listTop, m.listTop+3)
	}
	if v := m.View(); !strings.Contains(v, "u10.bsky.social") {
		t.Error("selected row not visible")
	}
}
