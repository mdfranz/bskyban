package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/rivo/uniseg"
)

var errUsage = errors.New("usage")

const usageText = `bskyban - review and block Bluesky followers

Usage:
  bskyban [review] [--since N] [--backlog]   interactive TUI (new followers only)
  bskyban feed [--pages N] [--backlog] [--refresh 30s] [--fancy] [--list [--json]]
                                        review authors of reposts in your timeline
  bskyban list [--since N] [--backlog] [--all] [--json]
  bskyban log [--since 7d] [--action block] [--who handle] [-n 50] [--json]
                                        show the audit log (no login needed)
  bskyban baseline                      mark all current non-followed followers as old
  bskyban show <handle|did>
  bskyban block [--dry-run] <handle|did>... (use - to read from stdin)
  bskyban keep <handle|did>...
  bskyban unblock [--dry-run] <handle|did>...
  bskyban posts [--refresh 30s] [--fancy] [--list [-n 20] [--json]]  browse your own posts (TUI), or print them
  bskyban post [--dry-run] <text>...    publish a post (use - to read the text from stdin)
  bskyban delete-post [--dry-run] <post>...  delete your own posts (at:// URI, bsky.app link or rkey)
  bskyban like [--dry-run] <post>...    like posts (at:// URI or bsky.app link)
  bskyban unlike [--dry-run] <post>...
  bskyban follow [--dry-run] <handle|did>...
  bskyban unfollow [--dry-run] <handle|did>...

Environment:
  BSKY_HANDLE, BSKY_APP_PASSWORD   required (use an app password)
  BSKY_PDS                         optional, default https://bsky.social
  BSKYBAN_STATE                    optional state file (default ~/.local/state/bskyban/state.json)
  BSKYBAN_AUDIT                    optional audit log (default ~/.local/state/bskyban/audit.jsonl)
  XDG_STATE_HOME                   moves the default state directory
  BSKYBAN_FANCY                    1 = same as --fancy (highlight newly inserted posts)
`

type app struct {
	c      *Client
	st     *State
	audit  *Audit
	stdin  io.Reader
	stdout io.Writer
}

// followers returns followers (newest first) you don't follow back and have no
// recorded decision for (new ones),
// plus skipped ones when backlog is true, or every follower when all is true. since > 0 limits to the newest N followers.
func (a *app) followers(ctx context.Context, since int, all, backlog bool) ([]Profile, error) {
	fs, err := a.c.GetFollowers(ctx, since)
	if err != nil {
		return nil, err
	}
	if all {
		return fs, nil
	}
	var out []Profile
	for _, f := range fs {
		if a.isNew(f, backlog) {
			out = append(out, f)
		}
	}
	return out, nil
}

// isNew reports whether p is someone you don't follow, haven't blocked, and have
// no recorded decision for (skipped ones count when backlog is true).
func (a *app) isNew(p Profile, backlog bool) bool {
	if p.Viewer.Following != "" || p.Viewer.Blocking != "" || p.DID == a.c.DID {
		return false
	}
	e, done := a.st.Entries[p.DID]
	return !done || (backlog && e.Decision == decSkipped)
}

// repostCtx records how a candidate reached your timeline.
type repostCtx struct {
	Post Post
	Item FeedItem // the reposted post, for liking or replying to it
	By   []string // handles of people you follow who reposted them
}

// feedCandidates returns the original authors of reposts in your timeline that
// are new to you, in order of first appearance (newest first), each once.
func (a *app) feedCandidates(ctx context.Context, pages int, backlog bool) ([]Profile, map[string]repostCtx, error) {
	reposts, err := a.c.GetTimelineReposts(ctx, pages)
	if err != nil {
		return nil, nil, err
	}
	var out []Profile
	rc := map[string]repostCtx{}
	for _, r := range reposts {
		if !a.isNew(r.Author, backlog) {
			continue
		}
		cur, seen := rc[r.Author.DID]
		if !seen {
			out = append(out, r.Author)
			cur.Post, cur.Item = r.Post, r.Item
			cur.Post.Repost = false // shown as the original post, under "Reposted by"
		}
		if !slices.Contains(cur.By, r.By.Handle) {
			cur.By = append(cur.By, r.By.Handle)
		}
		rc[r.Author.DID] = cur
	}
	return out, rc, nil
}

func (a *app) cmdFeedList(ctx context.Context, pages int, backlog, asJSON bool) error {
	list, rc, err := a.feedCandidates(ctx, pages, backlog)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(a.stdout)
	for _, p := range list {
		r := rc[p.DID]
		if asJSON {
			_ = enc.Encode(map[string]any{"did": p.DID, "handle": p.Handle, "displayName": p.DisplayName,
				"repostedBy": r.By, "text": r.Post.Text})
			continue
		}
		// fit the terminal exactly; when piped, don't truncate at all
		line := fmt.Sprintf("%-32s via %-40s %s", p.Handle, "@"+strings.Join(r.By, ", @"), formatPost(r.Post, 0))
		if tw := termWidth(a.stdout); tw > 0 {
			line = truncateWidth(line, tw-1)
		}
		fmt.Fprintln(a.stdout, line)
	}
	return nil
}

func (a *app) run(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "list":
		return a.cmdList(ctx, args)
	case "baseline":
		return a.cmdBaseline(ctx)
	case "show":
		return a.cmdShow(ctx, args)
	case "post":
		return a.cmdPost(ctx, args)
	case "delete-post":
		return a.cmdDeletePost(ctx, args)
	case "like", "unlike":
		return a.cmdLike(ctx, cmd, args)
	case "block", "keep", "unblock", "follow", "unfollow":
		return a.cmdAct(ctx, cmd, args)
	}
	return fmt.Errorf("%w: unknown command %q", errUsage, cmd)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func (a *app) cmdList(ctx context.Context, args []string) error {
	fs := newFlagSet("list")
	since := fs.Int("since", 0, "")
	all := fs.Bool("all", false, "")
	backlog := fs.Bool("backlog", false, "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	list, err := a.followers(ctx, *since, *all, *backlog)
	if err != nil {
		return err
	}
	dids := make([]string, len(list))
	for i, f := range list {
		dids[i] = f.DID
	}
	profiles, err := a.c.GetProfiles(ctx, dids)
	if err != nil {
		return err
	}
	byDID := map[string]Profile{}
	for _, p := range profiles {
		byDID[p.DID] = p
	}
	enc := json.NewEncoder(a.stdout)
	for _, f := range list {
		p, ok := byDID[f.DID]
		if !ok {
			p = f
		}
		status := a.st.Entries[p.DID].Decision
		if status == "" {
			status = "new"
		}
		if *asJSON {
			_ = enc.Encode(map[string]any{
				"did": p.DID, "handle": p.Handle, "displayName": p.DisplayName,
				"status": status, "createdAt": p.CreatedAt, "posts": p.PostsCount,
				"followers": p.FollowersCount, "following": p.FollowsCount,
				"hasAvatar": p.Avatar != "", "description": p.Description,
			})
			continue
		}
		fmt.Fprintf(a.stdout, "%-6s %-32s posts=%-5d followers=%-6d following=%-6d age=%-8s %s\n",
			status, p.Handle, p.PostsCount, p.FollowersCount, p.FollowsCount, humanAge(p.Age()), p.DID)
	}
	return nil
}

// cmdBaseline marks every follower without a decision as skipped, so only
// followers who arrive afterwards show up by default.
func (a *app) cmdBaseline(ctx context.Context) error {
	list, err := a.followers(ctx, 0, false, false)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, f := range list {
		a.st.Entries[f.DID] = Entry{Handle: f.Handle, Decision: decSkipped, At: now}
	}
	if err := a.st.Save(); err != nil {
		return err
	}
	a.record(AuditEvent{Action: "baseline", OK: true, Detail: fmt.Sprintf("%d followers marked as old", len(list))})
	fmt.Fprintf(a.stdout, "baselined %d existing followers; only new ones will show by default (--backlog to revisit)\n", len(list))
	return nil
}

func (a *app) cmdShow(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("%w: show takes exactly one handle or DID", errUsage)
	}
	p, err := a.c.GetProfile(ctx, normalizeActor(args[0]))
	if err != nil {
		return err
	}
	status := a.st.Entries[p.DID].Decision
	if p.Viewer.Blocking != "" {
		status = "blocked"
	}
	if status == "" {
		status = "new"
	}
	fmt.Fprintf(a.stdout, "%s (@%s)\nDID:       %s\nStatus:    %s\nCreated:   %s (%s)\nPosts:     %d\nFollowers: %d\nFollowing: %d\nAvatar:    %v\nURL:       https://bsky.app/profile/%s\n\n%s\n",
		p.DisplayName, p.Handle, p.DID, status, p.CreatedAt, humanAge(p.Age()),
		p.PostsCount, p.FollowersCount, p.FollowsCount, p.Avatar != "", p.Handle, linkifyIfTTY(p.Description))
	posts, err := a.c.GetRecentPosts(ctx, p.DID, 5)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recent posts: %v\n", err)
		return nil
	}
	fmt.Fprintf(a.stdout, "\nRecent posts:\n")
	if len(posts) == 0 {
		fmt.Fprintln(a.stdout, "  (none)")
	}
	for _, post := range posts {
		fmt.Fprintf(a.stdout, "  %s\n", linkifyIfTTY(formatPost(post, 0)))
	}
	return nil
}

func (a *app) cmdAct(ctx context.Context, cmd string, args []string) error {
	fs := newFlagSet(cmd)
	dry := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	ids, err := a.identifiers(fs.Args())
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("%w: %s needs at least one handle or DID", errUsage, cmd)
	}
	var failed int
	for _, id := range ids {
		if err := a.act(ctx, cmd, id, *dry); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", id, err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d failed", failed, len(ids))
	}
	return nil
}

// cmdPostsList prints up to n of your own posts, newest first, with the record
// key that delete-post accepts.
func (a *app) cmdPostsList(ctx context.Context, n int, asJSON bool) error {
	enc := json.NewEncoder(a.stdout)
	cursor := ""
	for printed := 0; printed < n; {
		items, next, err := a.c.GetOwnPosts(ctx, cursor, min(max(n-printed, 1), 100))
		if err != nil {
			return err
		}
		for _, it := range items {
			if printed++; printed > n {
				break
			}
			rkey := it.URI[strings.LastIndex(it.URI, "/")+1:]
			if asJSON {
				_ = enc.Encode(map[string]any{"uri": it.URI, "rkey": rkey, "createdAt": it.Post.CreatedAt, "text": it.Post.Text})
				continue
			}
			line := fmt.Sprintf("%-14s %s", rkey, formatPost(it.Post, 0))
			if tw := termWidth(a.stdout); tw > 0 {
				line = truncateWidth(line, tw-1)
			}
			fmt.Fprintln(a.stdout, line)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return nil
}

// cmdPost publishes a text post; the text is the arguments joined by spaces,
// or all of stdin when the only argument is "-".
func (a *app) cmdPost(ctx context.Context, args []string) (err error) {
	fs := newFlagSet("post")
	dry := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	var text string
	if rest := fs.Args(); len(rest) == 1 && rest[0] == "-" {
		b, err := io.ReadAll(a.stdin)
		if err != nil {
			return err
		}
		text = string(b)
	} else {
		text = strings.Join(rest, " ")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("%w: post needs some text", errUsage)
	}
	if n := uniseg.GraphemeClusterCount(text); n > maxPostGraphemes {
		return fmt.Errorf("post is %d characters; the limit is %d", n, maxPostGraphemes)
	}
	ev := AuditEvent{Action: "post", DID: a.c.DID, Handle: a.c.Handle, DryRun: *dry, Detail: truncateWidth(strings.Join(strings.Fields(text), " "), 200)}
	defer func() {
		ev.OK = err == nil
		if err != nil {
			ev.Error = err.Error()
		}
		a.record(ev)
	}()
	if *dry {
		fmt.Fprintf(a.stdout, "[dry-run] would post (%d characters):\n%s\n", uniseg.GraphemeClusterCount(text), terminalText(text))
		return nil
	}
	uri, err := a.c.Post(ctx, text, nil)
	if err != nil {
		return err
	}
	ev.Record = uri
	fmt.Fprintf(a.stdout, "posted %s\n", postURL(uri))
	return nil
}

// cmdDeletePost deletes your own posts, each given as an at:// URI, a
// bsky.app post link, or a bare record key.
func (a *app) cmdDeletePost(ctx context.Context, args []string) error {
	fs := newFlagSet("delete-post")
	dry := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%w: delete-post needs at least one post", errUsage)
	}
	var failed int
	for _, ref := range fs.Args() {
		if err := a.deletePost(ctx, ref, *dry); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", terminalText(ref), err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d failed", failed, fs.NArg())
	}
	return nil
}

func (a *app) deletePost(ctx context.Context, ref string, dry bool) (err error) {
	ev := AuditEvent{Action: "delete-post", DID: a.c.DID, Handle: a.c.Handle, DryRun: dry, Detail: truncateWidth(terminalText(ref), 200)}
	defer func() {
		ev.OK = err == nil
		if err != nil {
			ev.Error = err.Error()
		}
		a.record(ev)
	}()
	uri, err := a.postURI(ctx, ref)
	if err != nil {
		return err
	}
	ev.Record = uri
	if dry {
		fmt.Fprintf(a.stdout, "[dry-run] would delete post %s\n", postURL(uri))
		return nil
	}
	if err := a.c.DeletePost(ctx, uri); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "deleted post %s\n", postURL(uri))
	return nil
}

// postURI resolves ref to the AT URI of one of your own posts.
func (a *app) postURI(ctx context.Context, ref string) (string, error) {
	uri, err := a.resolvePost(ctx, ref)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(uri, "at://"+a.c.DID+"/") {
		return "", errors.New("that post isn't yours; only your own posts can be deleted")
	}
	return uri, nil
}

// resolvePost turns an at:// URI, a bsky.app post link or a bare record key
// (one of your own posts) into the post's AT URI.
func (a *app) resolvePost(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	var actor, rkey string
	switch {
	case strings.HasPrefix(ref, "at://"):
		parts := strings.Split(strings.TrimPrefix(ref, "at://"), "/")
		if len(parts) != 3 || parts[1] != postCollection {
			return "", fmt.Errorf("%w: not a post URI", errUsage)
		}
		actor, rkey = parts[0], parts[2]
	case strings.HasPrefix(ref, "https://"):
		parts := strings.Split(strings.TrimPrefix(ref, "https://"), "/")
		if len(parts) < 5 || parts[0] != "bsky.app" || parts[1] != "profile" || parts[3] != "post" {
			return "", fmt.Errorf("%w: not a bsky.app post link", errUsage)
		}
		actor, rkey = parts[2], parts[4]
	case ref != "" && !strings.ContainsAny(ref, "/:"):
		actor, rkey = a.c.DID, ref
	default:
		return "", fmt.Errorf("%w: expected an at:// URI, bsky.app link or record key", errUsage)
	}
	if !strings.HasPrefix(actor, "did:") {
		p, err := a.c.GetProfile(ctx, normalizeActor(actor))
		if err != nil {
			return "", err
		}
		actor = p.DID
	}
	return "at://" + actor + "/" + postCollection + "/" + rkey, nil
}

// cmdLike likes or unlikes posts given as at:// URIs or bsky.app links.
func (a *app) cmdLike(ctx context.Context, cmd string, args []string) error {
	fs := newFlagSet(cmd)
	dry := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%w: %s needs at least one post", errUsage, cmd)
	}
	var failed int
	for _, ref := range fs.Args() {
		if err := a.likePost(ctx, cmd, ref, *dry); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", terminalText(ref), err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d failed", failed, fs.NArg())
	}
	return nil
}

func (a *app) likePost(ctx context.Context, cmd, ref string, dry bool) (err error) {
	ev := AuditEvent{Action: cmd, DID: a.c.DID, Handle: a.c.Handle, DryRun: dry, Detail: truncateWidth(terminalText(ref), 200)}
	defer func() {
		ev.OK = err == nil
		if err != nil {
			ev.Error = err.Error()
		}
		a.record(ev)
	}()
	uri, err := a.resolvePost(ctx, ref)
	if err != nil {
		return err
	}
	post, like, err := a.c.GetPost(ctx, uri)
	if err != nil {
		return err
	}
	prefix := ""
	if dry {
		prefix = "[dry-run] would "
	}
	switch {
	case cmd == "like" && like != "":
		ev.Detail, ev.Record = "already liked", like
		fmt.Fprintf(a.stdout, "already liked %s\n", postURL(uri))
	case cmd == "unlike" && like == "":
		ev.Detail = "not liked"
		fmt.Fprintf(a.stdout, "you haven't liked %s\n", postURL(uri))
	case cmd == "like":
		fmt.Fprintf(a.stdout, "%slike %s\n", prefix, postURL(uri))
		if !dry {
			ev.Record, err = a.c.Like(ctx, post)
		}
	default:
		ev.Record = like
		fmt.Fprintf(a.stdout, "%sunlike %s\n", prefix, postURL(uri))
		if !dry {
			err = a.c.Unlike(ctx, like)
		}
	}
	return err
}

// postURL turns at://did/app.bsky.feed.post/rkey into its bsky.app link.
func postURL(uri string) string {
	if i := strings.LastIndex(uri, "/"); i >= 0 && strings.HasPrefix(uri, "at://") {
		if parts := strings.Split(strings.TrimPrefix(uri, "at://"), "/"); len(parts) == 3 {
			return "https://bsky.app/profile/" + parts[0] + "/post/" + uri[i+1:]
		}
	}
	return uri
}

func (a *app) act(ctx context.Context, cmd, id string, dry bool) (err error) {
	p, perr := a.c.GetProfile(ctx, id)
	if perr != nil {
		a.record(AuditEvent{Action: cmd, Handle: id, DryRun: dry, Error: perr.Error()})
		return perr
	}
	prev := a.st.Entries[p.DID].Decision
	ev := AuditEvent{Action: cmd, DID: p.DID, Handle: p.Handle, DryRun: dry, Prev: prev, Snapshot: snapshotOf(*p, true)}
	defer func() {
		ev.OK = err == nil
		if err != nil {
			ev.Error = err.Error()
		}
		a.record(ev)
	}()
	prefix := ""
	if dry {
		prefix = "[dry-run] would "
	}
	switch cmd {
	case "block":
		if p.Viewer.Blocking != "" {
			fmt.Fprintf(a.stdout, "%s@%s is already blocked\n", strings.Replace(prefix, "would ", "", 1), p.Handle)
			ev.Detail, ev.Record = "already blocked", p.Viewer.Blocking
			if dry {
				return nil
			}
			return a.st.Set(p.DID, p.Handle, decBlocked)
		}
		fmt.Fprintf(a.stdout, "%sblock @%s (%s)\n", prefix, p.Handle, p.DID)
		if dry {
			return nil
		}
		uri, err := a.c.Block(ctx, p.DID)
		if err != nil {
			return err
		}
		ev.Record = uri
		return a.st.Set(p.DID, p.Handle, decBlocked)
	case "keep":
		fmt.Fprintf(a.stdout, "%skeep @%s\n", prefix, p.Handle)
		if dry {
			return nil
		}
		return a.st.Set(p.DID, p.Handle, decKept)
	case "follow":
		if p.Viewer.Following != "" {
			fmt.Fprintf(a.stdout, "%s@%s: you already follow them\n", strings.Replace(prefix, "would ", "", 1), p.Handle)
			ev.Detail, ev.Record = "already following", p.Viewer.Following
			return nil
		}
		fmt.Fprintf(a.stdout, "%sfollow @%s (%s)\n", prefix, p.Handle, p.DID)
		if dry {
			return nil
		}
		uri, err := a.c.Follow(ctx, p.DID)
		if err != nil {
			return err
		}
		ev.Record = uri
		return a.st.Set(p.DID, p.Handle, decFollowed)
	case "unfollow":
		fmt.Fprintf(a.stdout, "%sunfollow @%s (%s)\n", prefix, p.Handle, p.DID)
		ev.Record = p.Viewer.Following
		if dry {
			return nil
		}
		if p.Viewer.Following == "" {
			ev.Detail = "not following"
			return nil
		}
		if err := a.c.Unfollow(ctx, p.Viewer.Following); err != nil {
			return err
		}
		if a.st.Entries[p.DID].Decision == decFollowed {
			return a.st.Delete(p.DID)
		}
		return nil
	case "unblock":
		fmt.Fprintf(a.stdout, "%sunblock @%s (%s)\n", prefix, p.Handle, p.DID)
		ev.Record = p.Viewer.Blocking
		if dry {
			return nil
		}
		if p.Viewer.Blocking != "" {
			if err := a.c.Unblock(ctx, p.Viewer.Blocking); err != nil {
				return err
			}
		}
		return a.st.Delete(p.DID)
	}
	return nil
}

// record writes an audit event; a failing log is reported but never hides the action's result.
func (a *app) record(e AuditEvent) {
	if e.Source == "" {
		e.Source = "cli"
	}
	if err := a.audit.Log(e); err != nil {
		fmt.Fprintf(os.Stderr, "warning: audit log: %v\n", err)
	}
}

// identifiers expands "-" into whitespace-separated first fields of stdin lines.
func (a *app) identifiers(args []string) ([]string, error) {
	var ids []string
	for _, arg := range args {
		if arg != "-" {
			ids = append(ids, normalizeActor(arg))
			continue
		}
		sc := bufio.NewScanner(a.stdin)
		for sc.Scan() {
			if f := strings.Fields(sc.Text()); len(f) > 0 {
				ids = append(ids, normalizeActor(f[0]))
			}
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// formatPost renders a post as a single line: "<age> [tag] text".
func formatPost(p Post, max int) string {
	tag := ""
	switch {
	case p.Repost:
		tag = "[repost] "
	case p.Reply:
		tag = "[reply] "
	}
	age := "?"
	if t, err := time.Parse(time.RFC3339, p.CreatedAt); err == nil {
		age = humanAge(time.Since(t))
	}
	text := strings.Join(strings.Fields(terminalText(p.Text)), " ")
	if r := []rune(text); max > 0 && len(r) > max {
		text = string(r[:max-1]) + "…"
	}
	if len(p.Attach) > 0 {
		text = strings.TrimSpace(text + " " + terminalText(strings.Join(p.Attach, " ")))
	}
	if text == "" {
		text = "(no text)"
	}
	return fmt.Sprintf("%-5s %s%s", age, tag, text)
}

// termWidth is the width of w if it is a terminal, or 0 (unknown, e.g. piped).
func termWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		return 0
	}
	width, _, err := term.GetSize(f.Fd())
	if err != nil {
		return 0
	}
	return width
}

func normalizeActor(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "@") }

func humanAge(d time.Duration) string {
	switch {
	case d <= 0:
		return "?"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 90*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
}
