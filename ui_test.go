package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mdfranz/bskyban/internal/termtext"
)

// fakePDS records the write calls the UI makes.
type fakePDS struct {
	mu      sync.Mutex
	calls   []string
	deleted []string
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
			var body struct{ Collection string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			n++
			kind := "block"
			if strings.HasSuffix(body.Collection, ".follow") {
				kind = "follow"
			} else if strings.HasSuffix(body.Collection, ".post") {
				kind = "post"
			} else if strings.HasSuffix(body.Collection, ".like") {
				kind = "like"
			}
			f.log(kind)
			fmt.Fprintf(w, `{"uri":"at://did:me/%s/rk%d"}`, body.Collection, n)
		case "/xrpc/com.atproto.repo.deleteRecord":
			var body struct {
				Collection string `json:"collection"`
				RKey       string `json:"rkey"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.deleted = append(f.deleted, body.RKey)
			f.mu.Unlock()
			if strings.HasSuffix(body.Collection, ".follow") {
				f.log("unfollow")
			} else if strings.HasSuffix(body.Collection, ".post") {
				f.log("delete-post")
			} else if strings.HasSuffix(body.Collection, ".like") {
				f.log("unlike")
			} else {
				f.log("unblock")
			}
			fmt.Fprint(w, `{}`)
		case "/xrpc/app.bsky.actor.getProfile":
			switch r.URL.Query().Get("actor") {
			case "friend": // someone you follow
				fmt.Fprint(w, `{"did":"did:friend","handle":"friend","viewer":{"following":"at://did:me/app.bsky.graph.follow/rkf"}}`)
			case "newbie": // a stranger
				fmt.Fprint(w, `{"did":"did:newbie","handle":"newbie","viewer":{}}`)
			default: // "x": already blocked
				fmt.Fprint(w, `{"did":"x","handle":"x","viewer":{"blocking":"at://did:me/app.bsky.graph.block/rk0"}}`)
			}
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

func TestUndoRestoredBlockDeletesCurrentRecord(t *testing.T) {
	m, f := candidates(t)
	m = press(m, "b")
	m = press(m, "left")
	m = press(m, "k")
	m = press(m, "u")
	m = press(m, "u")
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := strings.Join(f.deleted, ","); got != "rk1,rk2" {
		t.Fatalf("deleted records = %s, want rk1,rk2", got)
	}
	if _, ok := m.a.st.Entries["did:a"]; ok {
		t.Fatal("second undo should remove the decision")
	}
}

func TestQuitWaitsForActionPersistenceAndAudit(t *testing.T) {
	for _, quit := range []tea.KeyMsg{key("q"), {Type: tea.KeyCtrlC}} {
		t.Run(quit.String(), func(t *testing.T) {
			m, f := candidates(t)
			next, action := m.Update(key("b"))
			m = next.(model)
			next, exit := m.Update(quit)
			m = next.(model)
			if exit != nil || !m.quitting || !m.busy {
				t.Fatal("quit must wait for the pending action")
			}
			next, exit = m.Update(action())
			m = next.(model)
			if exit == nil {
				t.Fatal("expected quit after action completed")
			}
			if _, ok := exit().(tea.QuitMsg); !ok {
				t.Fatal("expected quit message")
			}
			st, err := LoadState(m.a.st.path)
			if err != nil || st.Entries["did:a"].Decision != decBlocked {
				t.Fatalf("decision not persisted: state=%+v err=%v", st, err)
			}
			if got := strings.Join(actions(t, m.a), ","); got != "block" || f.joined() != "block" {
				t.Fatalf("action not audited: %s", got)
			}
		})
	}
}

func TestDeferredQuitShowsActionFailure(t *testing.T) {
	m, _ := candidates(t)
	next, _ := m.Update(key("b"))
	m = next.(model)
	next, _ = m.Update(key("q"))
	m = next.(model)
	next, cmd := m.Update(actMsg{
		undo: undoEntry{profile: m.list[0], decision: decBlocked},
		err:  fmt.Errorf("remote block failed"),
	})
	m = next.(model)
	if cmd != nil || m.quitting || m.busy || m.err == nil {
		t.Fatal("failed action should stay open and show the error")
	}
	if len(m.a.st.Entries) != 0 || strings.Join(actions(t, m.a), ",") != "block!" {
		t.Fatal("failed action should only produce a failure audit")
	}
}

func TestFailedSaveRetriesWithoutRepeatingRemoteAction(t *testing.T) {
	for _, undo := range []bool{false, true} {
		t.Run(fmt.Sprintf("undo=%v", undo), func(t *testing.T) {
			m, f := candidates(t)
			if undo {
				m = press(m, "b")
			}
			path := m.a.st.path
			m.a.st.path = t.TempDir() // rename over a directory must fail
			beforeIdx, beforeHistory := m.idx, len(m.history)
			if undo {
				m = press(m, "u")
			} else {
				m = press(m, "b")
			}
			if m.err == nil || m.pending == nil || m.idx != beforeIdx {
				t.Fatalf("failed save advanced or was forgotten: %+v", m)
			}
			wantDecision := ""
			if undo {
				wantDecision = decBlocked
				beforeHistory--
			}
			if m.a.st.Entries["did:a"].Decision != wantDecision || len(m.history) != beforeHistory {
				t.Fatal("failed save committed local state or history")
			}
			events, err := ReadAudit(m.a.audit.path)
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			if last.OK || !strings.Contains(last.Detail, "local persistence failed") {
				t.Fatalf("failed save audit = %+v", last)
			}
			calls := f.joined()
			m = press(m, "b") // no duplicate remote action while save is pending
			next, exit := m.Update(key("q"))
			m = next.(model)
			if exit != nil {
				t.Fatal("quit must not discard unsaved decision")
			}
			m.a.st.path = path
			m = press(m, "r")
			if m.pending != nil || m.err != nil || f.joined() != calls {
				t.Fatalf("retry repeated action or failed: calls=%s err=%v", f.joined(), m.err)
			}
			st, err := LoadState(path)
			if err != nil {
				t.Fatal(err)
			}
			wantDecision = decBlocked
			if undo {
				wantDecision = ""
			}
			if st.Entries["did:a"].Decision != wantDecision {
				t.Fatalf("persisted decision = %s", st.Entries["did:a"].Decision)
			}
		})
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
	for _, w := range []string{"Following", "Friend (@friend.bsky.social)", "hello world", "↻ reposted by @friend.bsky.social", "enter open"} {
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
	if v := termtext.StripLinks(m.View()); !strings.Contains(v, "replying to @x.bsky.social: parent text") {
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
	m.height = 16 // avail 10 lines
	m.items = nil
	for i := 0; i < 20; i++ {
		m.items = append(m.items, FeedItem{Author: Profile{DID: fmt.Sprint(i), Handle: fmt.Sprintf("u%d.bsky.social", i)},
			Post: Post{Text: strings.Repeat(fmt.Sprintf("words%d ", i), 30)}})
	}
	for i := 0; i < 12; i++ {
		m = press(m, "down")
		v := m.View()
		if !strings.Contains(v, fmt.Sprintf("u%d.bsky.social", m.cur)) {
			t.Fatalf("cursor %d not visible:\n%s", m.cur, v)
		}
		if got := strings.Count(v, "\n"); got > m.height {
			t.Fatalf("view is %d lines, window is %d", got, m.height)
		}
	}
	m = press(m, "up")
	if m.cur != 11 {
		t.Errorf("cur = %d", m.cur)
	}
}

func TestListShowsUpToThreeLinesOfEachPost(t *testing.T) {
	m, _ := followingModel(t)
	m.width = 80
	long := strings.TrimSpace(strings.Repeat("alpha beta gamma delta ", 30))
	m.items = []FeedItem{
		{Author: Profile{DID: "1", Handle: "long.bsky.social"}, Post: Post{Text: long}},
		{Author: Profile{DID: "2", Handle: "short.bsky.social"}, Post: Post{Text: "just one line"}},
		{Author: Profile{DID: "3", Handle: "img.bsky.social"}, Post: Post{Attach: []string{"▣ a cat"}}},
	}
	m.items = append(m.items, FeedItem{Author: Profile{DID: "4", Handle: "quote.bsky.social"},
		Post: Post{Text: "look", Quote: "@q.bsky.social: first line\n\nsecond line\n\nthird"}})
	if ql := m.itemLines(3, m.listWidth()); len(ql) != 4 { // header, own text, quote, separator
		t.Errorf("a quote should be one paragraph on a line of its own, got %d lines:\n%s", len(ql), strings.Join(ql, "\n"))
	} else if q := termtext.StripANSI(ql[2]); !strings.Contains(q, "▎ @q.bsky.social: first line second line third") || strings.Contains(ql[1], "@q.bsky.social") {
		t.Errorf("quote should follow the post's own text on its own line: text=%q quote=%q", ql[1], q)
	}
	lines := m.itemLines(0, m.listWidth())
	if len(lines) != 1+previewLines+1 { // header, 3 text lines, separator
		t.Fatalf("long post uses %d lines, want %d:\n%s", len(lines), 1+previewLines+1, strings.Join(lines, "\n"))
	}
	if last := lines[previewLines]; !strings.HasSuffix(strings.TrimSpace(last), "…") {
		t.Errorf("clipped preview should end in an ellipsis: %q", last)
	}
	if got := len(m.itemLines(1, m.listWidth())); got != 3 {
		t.Errorf("one-line post uses %d lines, want 3 (header, text, separator)", got)
	}
	v := m.View()
	if strings.Count(v, "alpha") < 3*2 { // at least three full lines of text
		t.Errorf("expected three lines of preview text:\n%s", v)
	}
	if !strings.Contains(v, "▣ a cat") {
		t.Error("attachments should show in the preview of a text-less post")
	}
}

func TestStackedLayoutFitsTheWindow(t *testing.T) {
	m, _ := candidates(t)
	m.width, m.height = 80, 30
	p := m.list[0]
	p.Description = strings.Repeat("A long bio sentence. ", 8)
	m.list[0] = p
	m.profiles[p.DID] = &p
	var posts []Post
	for i := 0; i < 8; i++ {
		posts = append(posts, Post{Text: strings.Repeat("some post text ", 6), CreatedAt: "2026-11-01T06:00:00Z"})
	}
	m.posts[p.DID] = &posts
	if _, _, stacked := m.panelWidths(); !stacked {
		t.Fatal("80 columns should stack")
	}
	for step := 0; step < 4; step++ {
		lines := strings.Count(m.View(), "\n")
		if lines > m.height {
			t.Fatalf("scroll step %d: view is %d lines, window is %d", step, lines, m.height)
		}
		m = press(m, "pgdown")
	}
	if m.scroll == 0 {
		t.Error("expected to scroll")
	}
}

func TestHumanAgeShowsMinutes(t *testing.T) {
	for d, want := range map[time.Duration]string{5 * time.Minute: "5m", 90 * time.Minute: "1h", 30 * time.Hour: "30h"} {
		if got := humanAge(d); got != want {
			t.Errorf("humanAge(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestHintFitsNarrowTerminals(t *testing.T) {
	m, _ := followingModel(t)
	m.inPost = true
	for _, w := range []int{50, 60, 80, 100, 140} {
		m.width = w
		for _, line := range m.hintLines() {
			if lw := lipgloss.Width(line); lw > w {
				t.Errorf("width %d: hint line is %d wide: %q", w, lw, line)
			}
		}
		h := m.hint()
		for _, want := range []string{"f follow", "tab switch", "q quit"} {
			if !strings.Contains(h, want) {
				t.Errorf("width %d: hint lost %q: %q", w, want, h)
			}
		}
	}
	m.width = 140
	if len(m.hintLines()) != 1 {
		t.Error("a wide terminal should keep the hint on one line")
	}
	// a wrapped footer must not push the screen past the window height
	m.width, m.height = 60, 24
	if got := strings.Count(m.View(), "\n"); got > m.height {
		t.Errorf("view is %d lines in a %d-line window", got, m.height)
	}
}

func TestLayoutUsesTheWholeWidth(t *testing.T) {
	m, _ := candidates(t)
	prevRight := 0
	for _, w := range []int{96, 120, 140, 200, 300} {
		m.width = w
		l, r, stacked := m.panelWidths()
		if stacked {
			t.Errorf("width %d should not stack", w)
		}
		if l+panelGap+r != w {
			t.Errorf("width %d: panels use %d columns, want all %d", w, l+panelGap+r, w)
		}
		if r <= prevRight {
			t.Errorf("width %d: posts panel (%d) should grow with the window (was %d)", w, r, prevRight)
		}
		prevRight = r
	}
	m.width = 95
	if _, _, stacked := m.panelWidths(); !stacked {
		t.Error("95 columns is too narrow for two panels")
	}
	// the timeline list also fills the window instead of stopping at 140
	f, _ := followingModel(t)
	f.width = 220
	if got := f.listWidth(); got != 218 {
		t.Errorf("listWidth at 220 columns = %d, want 218", got)
	}
	for _, w := range []int{100, 180, 260} {
		m, _ := candidates(t)
		m.width, m.height = w, 40
		p := m.list[0]
		m.profiles[p.DID] = &p
		posts := []Post{{Text: strings.Repeat("wide terminal text ", 40), CreatedAt: "2026-11-01T06:00:00Z"}}
		m.posts[p.DID] = &posts
		var widest int
		for _, line := range strings.Split(m.View(), "\n") {
			widest = max(widest, termtext.VisibleWidth(line))
		}
		if widest > w {
			t.Errorf("width %d: a line is %d wide", w, widest)
		}
		if w >= 180 && widest < w*3/4 {
			t.Errorf("width %d: content only reaches %d columns; wasted space", w, widest)
		}
	}
}
