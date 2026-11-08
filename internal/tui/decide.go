package tui

// Account decisions: block, follow, keep and skip, unfollowing, undo, and the
// audit events that record them.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
)

type undoEntry struct {
	tab      tabID
	idx      int
	profile  bsky.Profile
	decision string
	record   string       // block/follow record this decision created
	prev     *store.Entry // decision recorded before this one
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

var decisionAction = map[string]string{store.Blocked: "block", store.Followed: "follow", store.Kept: "keep", store.Skipped: "skip"}

// liftAction names the audit event for deleting the record behind an overridden decision.
var liftAction = map[string]string{store.Blocked: "unblock", store.Followed: "unfollow"}

// auditEvent is the base event for msg, whether it succeeded or failed.
func (m model) auditEvent(msg actMsg) store.AuditEvent {
	e := msg.undo
	ev := store.AuditEvent{
		Action: decisionAction[e.decision], DID: e.profile.DID, Handle: e.profile.Handle,
		Source: m.source(e.tab), Via: m.via(e), Snapshot: store.SnapshotOf(e.profile, m.profiles[e.profile.DID] != nil),
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
		if app.IsRemote(e.decision) {
			ev.Record = e.record // the block/follow record that was removed
		}
		ev.Prev = e.decision
	}
	if msg.isUnfollow {
		ev.Action, ev.Record = "unfollow", e.record
	}
	return ev
}

// auditEvents lists what a successful action did. Overriding a block or follow
// with another decision deletes that record, which gets its own unblock/unfollow event.
func (m model) auditEvents(msg actMsg) []store.AuditEvent {
	e := msg.undo
	main := m.auditEvent(msg)
	if !msg.isUndo && !msg.isUnfollow && e.prev != nil && app.IsRemote(e.prev.Decision) && e.prev.Decision != e.decision {
		lift := main
		lift.Action, lift.Detail, lift.Record = liftAction[e.prev.Decision], "lifted by "+e.decision, ""
		return []store.AuditEvent{lift, main}
	}
	return []store.AuditEvent{main}
}

// audit appends to the audit log; a failure to log is shown, never swallowed.
func (m *model) audit(ev store.AuditEvent) {
	if err := m.a.Audit.Log(ev); err != nil && m.err == nil {
		m.err = fmt.Errorf("audit log: %w", err)
	}
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
	if msg.isUnfollow {
		if entry, ok := m.a.State.Entries[did]; ok && entry.Decision == store.Followed {
			err = m.a.State.Delete(did)
		}
	} else if msg.isUndo {
		if e.prev != nil {
			err = m.a.State.Put(did, *e.prev)
		} else {
			err = m.a.State.Delete(did)
		}
	} else {
		err = m.a.State.Set(did, e.profile.Handle, e.decision)
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
	if msg.isUnfollow {
		delete(m.records, did)
		m.history = slices.DeleteFunc(m.history, func(h undoEntry) bool { return h.profile.DID == did })
		for i := range m.list {
			if m.list[i].DID == did {
				m.list[i].Viewer.Following = ""
			}
		}
		for i := range m.items {
			if m.items[i].Author.DID == did {
				m.items[i].Author.Viewer.Following = ""
			}
		}
		if p := m.profiles[did]; p != nil {
			p.Viewer.Following = ""
		}
		m.status = fmt.Sprintf("unfollowed @%s", e.profile.Handle)
	} else if msg.isUndo {
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
		if app.IsRemote(e.decision) {
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

// unfollowKey confirms the selected account before removing its follow record.
func (m model) unfollowKey(confirm string) (model, tea.Cmd) {
	p, ok := m.subject()
	if !ok {
		return m, nil
	}
	if confirm != "unfollow:"+p.DID {
		m.confirm = "unfollow:" + p.DID
		m.status = fmt.Sprintf("Unfollow @%s? Press u again to confirm.", p.Handle)
		return m, nil
	}
	uri := p.Viewer.Following
	if uri == "" && m.a.State.Entries[p.DID].Decision == store.Followed {
		uri = m.records[p.DID]
	}
	idx := m.idx
	if m.active() != tabCandidates {
		idx = m.cur
	}
	e := undoEntry{tab: m.active(), idx: idx, profile: p, decision: store.Followed, record: uri}
	c := m.a.Client
	m.busy, m.err, m.status = true, nil, ""
	return m, func() tea.Msg {
		msg := actMsg{undo: e, isUnfollow: true}
		if msg.undo.record == "" {
			profile, err := c.GetProfile(context.Background(), p.DID)
			if err != nil {
				msg.err = err
				return msg
			}
			msg.undo.record = profile.Viewer.Following
		}
		if msg.undo.record == "" {
			msg.err = fmt.Errorf("you do not follow @%s", p.Handle)
			return msg
		}
		msg.err = c.Unfollow(context.Background(), msg.undo.record)
		return msg
	}
}

// decideKey applies decision to p, unless that is already recorded.
func (m model) decideKey(p bsky.Profile, decision string) (model, tea.Cmd) {
	if e, ok := m.a.State.Entries[p.DID]; ok && e.Decision == decision {
		m.status = fmt.Sprintf("@%s is already %s", p.Handle, decision)
		return m, nil
	}
	if decision == store.Followed && p.Viewer.Following != "" {
		m.status = fmt.Sprintf("you already follow @%s", p.Handle)
		return m, nil
	}
	return m.decide(p, decision)
}

// decide records decision for p. Blocking or following (and lifting an earlier
// block or follow when it is overridden) calls the API first; the state file is
// only touched in applyAct.
func (m model) decide(p bsky.Profile, decision string) (model, tea.Cmd) {
	prev, had := m.a.State.Entries[p.DID]
	var prevP *store.Entry
	if had {
		prevP = &prev
	}
	idx := m.idx
	if m.active() == tabFollowing {
		idx = m.cur
	}
	e := undoEntry{tab: m.active(), idx: idx, profile: p, decision: decision, prev: prevP}
	lift := had && app.IsRemote(prev.Decision) && prev.Decision != decision
	known := ""
	if lift {
		known = m.records[p.DID]
		if known == "" {
			if fp := m.profiles[p.DID]; fp != nil {
				known = app.ViewerURI(fp, prev.Decision)
			}
		}
	}
	c := m.a.Client
	m.busy, m.err = true, nil
	return m, func() tea.Msg {
		ctx := context.Background()
		msg := actMsg{undo: e}
		if lift { // e.g. keep/skip on a blocked account lifts the block
			if known == "" {
				if fp, err := c.GetProfile(ctx, p.DID); err == nil {
					known = app.ViewerURI(fp, prev.Decision)
				}
			}
			if known != "" {
				if msg.err = app.DeleteFor(ctx, c, prev.Decision, known); msg.err != nil {
					return msg
				}
			}
			msg.lifted = true
		}
		if app.IsRemote(decision) {
			msg.newURI, msg.err = app.CreateFor(ctx, c, decision, p.DID)
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
	c := m.a.Client
	m.busy, m.err = true, nil
	return m, func() tea.Msg {
		ctx := context.Background()
		msg := actMsg{undo: e, isUndo: true}
		if app.IsRemote(e.decision) && e.record != "" {
			msg.err = app.DeleteFor(ctx, c, e.decision, e.record)
		}
		if msg.err == nil && e.prev != nil && app.IsRemote(e.prev.Decision) && e.prev.Decision != e.decision {
			msg.newURI, msg.err = app.CreateFor(ctx, c, e.prev.Decision, e.profile.DID) // restore what we lifted
		}
		return msg
	}
}

// decisionBadge shows what was decided about did earlier, if anything.
// decisionStyle is how a recorded decision is coloured; ok is false when there is none.
func decisionStyle(decision string) (st lipgloss.Style, ok bool) {
	switch decision {
	case store.Blocked:
		return errStyle, true
	case store.Followed, store.Kept:
		return okStyle, true
	case store.Skipped:
		return dimStyle, true
	}
	return st, false
}

func (m model) decisionBadge(did string) string {
	d := m.a.State.Entries[did].Decision
	if st, ok := decisionStyle(d); ok {
		return st.Render("● " + d)
	}
	return ""
}
