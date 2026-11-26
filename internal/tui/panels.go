package tui

// The two-column layout: profile panel on the left, the scrollable posts panel
// on the right, and how posts are rendered in it.

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/termtext"
)

const panelGap = 4

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
	case m.follows != nil:
		if p, ok := m.subject(); ok {
			return m.recentLines(p.DID, rw, bsky.Post{})
		}
		return nil
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

func styleLines(lines []string, st lipgloss.Style) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = st.Render(l)
	}
	return out
}

func (m model) profilePanel(cw int, pos string) []string {
	p, _ := m.subject()
	name := p.DisplayName
	if name == "" {
		name = "(no display name)"
	}
	lines := styleLines(termtext.Wrap(name, cw-12), headingStyle)
	lines[len(lines)-1] += "  " + dimStyle.Render(pos)
	lines = append(lines, accentStyle.Render("@"+p.Handle),
		dimStyle.Render(termtext.OSC8(bsky.ProfileURL(p.Handle), "open profile ↗")))
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
	row("Account age", termtext.HumanAge(p.Age()))
	if t, err := time.Parse(time.RFC3339, p.CreatedAt); err == nil {
		row("Joined", t.Format("2006-01-02"))
	}
	row("Avatar", avatar)
	row("Posts", fmt.Sprint(p.PostsCount))
	row("Followers", fmt.Sprint(p.FollowersCount))
	row("Following", fmt.Sprint(p.FollowsCount))
	if days := p.Age().Hours() / 24; days >= 1 {
		row("Posts/day", fmt.Sprintf("%.1f", float64(p.PostsCount)/days))
	}
	if p.FollowsCount > 0 {
		row("Fol. ratio", fmt.Sprintf("%.1f", float64(p.FollowersCount)/float64(p.FollowsCount)))
	}
	if p.Viewer.FollowedBy != "" {
		row("Relation", "follows you")
	}
	lines = append(lines, "")
	if bio := strings.TrimSpace(p.Description); bio != "" {
		return append(lines, termtext.Wrap(bio, cw)...)
	}
	return append(lines, dimStyle.Render("(no bio)"))
}

// currentPost marks the post you are on: a heading and an accent bar down its side,
// so it stands apart from the author's other posts listed below it.
func currentPost(title string, p bsky.Post, it bsky.FeedItem, cw int) []string {
	lines := []string{headingStyle.Render("▸ " + title)}
	bar := accentStyle.Render("┃ ")
	for _, l := range renderPost(p, max(cw-2, 10), true) {
		if l == "" {
			continue
		}
		lines = append(lines, bar+l)
	}
	if it.URI != "" {
		lines = append(lines, bar+statsLine(it))
	}
	return append(lines, "", dimStyle.Render(strings.Repeat("─", max(cw-1, 1))), "")
}

// statsLine is the post's engagement counts and a link to it on bsky.app.
func statsLine(it bsky.FeedItem) string {
	count := func(n int, one string) string {
		switch {
		case n == 1:
			return "1 " + one
		case one == "reply":
			return fmt.Sprintf("%d replies", n)
		}
		return fmt.Sprintf("%d %ss", n, one)
	}
	s := it.Stats
	likes := dimStyle.Render(count(s.Likes, "like"))
	if it.Like != "" {
		likes = errStyle.Render("♥ " + count(s.Likes, "like") + " (you liked this)")
	}
	rest := dimStyle.Render(" · " + count(s.Reposts, "repost") + " · " + count(s.Replies, "reply") + " · " + count(s.Quotes, "quote") + " · ")
	return likes + rest + accentStyle.Render(termtext.OSC8(it.PostURL(), "open post ↗"))
}

// recentLines lists the author's latest posts, leaving out cur (shown above as the current post).
func (m model) recentLines(did string, cw int, cur bsky.Post) []string {
	lines := []string{headingStyle.Render("Recent posts"), ""}
	switch pp := m.posts[did]; {
	case pp == nil:
		return append(lines, m.spin.View()+" loading posts…")
	case len(*pp) == 0:
		return append(lines, dimStyle.Render("(no posts)"))
	default:
		for _, post := range *pp {
			if post.CreatedAt == cur.CreatedAt && post.Text == cur.Text {
				continue
			}
			lines = append(lines, renderPost(post, cw, false)...)
		}
	}
	return lines
}

// rc0 is the reposted post for did, or the zero Post.
func rc0(ctxs map[string]app.RepostCtx, did string) bsky.Post { return ctxs[did].Post }

func (m model) candRight(cw int) []string {
	p, _ := m.subject()
	var lines []string
	if rc, ok := m.ctxs[p.DID]; ok {
		lines = append(lines, accentStyle.Render("Reposted by @"+strings.Join(rc.By, ", @")), "")
		lines = append(lines, currentPost("Reposted post", rc.Post, rc.Item, cw)...)
	}
	return append(lines, m.recentLines(p.DID, cw, rc0(m.ctxs, p.DID))...)
}

func (m model) postRight(cw int) []string {
	it := m.items[m.cur]
	var lines []string
	if it.By != "" {
		by := "↻ Reposted by @" + it.By
		if it.RepostedAt != "" {
			by += " · " + termtext.HumanAgeISO(it.RepostedAt) + " ago"
		}
		lines = append(lines, accentStyle.Render(by), "")
	}
	lines = append(lines, currentPost(fmt.Sprintf("This post · %d of %d", m.cur+1, len(m.items)), it.Post, it, cw)...)
	return append(lines, m.recentLines(it.Author.DID, cw, it.Post)...)
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
		b.WriteString(l + strings.Repeat(" ", max(leftW-termtext.VisibleWidth(l), 0)+panelGap) + r + "\n")
	}
	return b.String()
}

// panelsView puts the profile beside (or above) rl, the lines from rightLines.
func (m model) panelsView(pos string, rl []string) string {
	lw, _, stacked := m.panelWidths()
	left := m.profilePanel(lw, pos)
	right := window(rl, m.scroll, m.rightAvail())
	if stacked {
		return strings.Join(append(append(left, ""), right...), "\n") + "\n"
	}
	return joinColumns(left, right, lw)
}

// scrollPos is the posts-panel scroll position as "[34%]", or "" if everything fits.
func (m model) scrollPos(rl []string) string {
	n, av := len(rl), m.rightAvail()
	if n <= av {
		return ""
	}
	return dimStyle.Render(fmt.Sprintf("   [%d%%]", (min(m.scroll, n-av)*100)/max(n-av, 1)))
}

// renderPost shows a post in full: a dim header line, then the wrapped text
// indented. full adds reply context and image links (used for the post under review).
func renderPost(p bsky.Post, w int, full bool) []string {
	kind := ""
	switch {
	case p.Repost:
		kind = " · repost"
	case p.Reply:
		kind = " · reply"
	}
	age := termtext.HumanAgeISO(p.CreatedAt)
	lines := []string{dimStyle.Render(age + " ago" + kind)}
	indent := "  "
	body := func(text string, st *lipgloss.Style, prefix string) {
		for _, l := range termtext.Wrap(text, w-len(indent)-len(prefix)) {
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
	if p.Link != nil {
		if l := p.Link.Label(p.Text); l != "" {
			body(l, &dimStyle, "")
		}
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
