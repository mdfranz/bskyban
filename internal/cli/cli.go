package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/mdfranz/bskyban/internal/app"
	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/store"
	"github.com/mdfranz/bskyban/internal/termtext"
	"github.com/rivo/uniseg"
)

// ErrUsage marks a mistake in how bskyban was invoked; the caller exits with status 2.
var ErrUsage = errors.New("usage")

const UsageText = `bskyban - review and block Bluesky followers

Usage:
  bskyban [feed] [--pages N] [--backlog] [--refresh 30s] [--fancy] [--theme NAME] [--list [--json]]
                                        your timeline, plus review of repost authors (the default)
  bskyban review [--since N] [--backlog] [--theme NAME]   review new followers one at a time
  bskyban list [--since N] [--backlog] [--all] [--json]
  bskyban log [--since 7d] [--action block] [--who handle] [-n 50] [--json]
                                        show the audit log (no login needed)
  bskyban themes                        preview colour themes (no login needed)
  bskyban baseline                      mark all current non-followed followers as old
  bskyban show <handle|did>
  bskyban block [--dry-run] <handle|did>... (use - to read from stdin)
  bskyban keep <handle|did>...
  bskyban unblock [--dry-run] <handle|did>...
  bskyban posts [--refresh 30s] [--fancy] [--theme NAME] [--list [-n 20] [--json]]  browse your own posts (TUI), or print them
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
  BSKYBAN_THEME                    auto (default), dark, light, ansi, dracula, catppuccin
`

// CLI is the shared application plus the streams the subcommands read and write.
type CLI struct {
	*app.App
	stdin  io.Reader
	stdout io.Writer
}

// New returns the subcommands for a, reading from in and writing to out.
func New(a *app.App, in io.Reader, out io.Writer) *CLI { return &CLI{App: a, stdin: in, stdout: out} }

// FeedList prints the repost authors the Reposts tab would offer (--list).
func (a *CLI) FeedList(ctx context.Context, pages int, backlog, asJSON bool) error {
	list, rc, err := a.FeedCandidates(ctx, pages, backlog)
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
			line = termtext.TruncateWidth(line, tw-1)
		}
		fmt.Fprintln(a.stdout, line)
	}
	return nil
}

// Run executes a subcommand that needs a login and no terminal UI.
func (a *CLI) Run(ctx context.Context, cmd string, args []string) error {
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
	return fmt.Errorf("%w: unknown command %q", ErrUsage, cmd)
}

func NewFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func (a *CLI) cmdList(ctx context.Context, args []string) error {
	fs := NewFlagSet("list")
	since := fs.Int("since", 0, "")
	all := fs.Bool("all", false, "")
	backlog := fs.Bool("backlog", false, "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	list, err := a.Followers(ctx, *since, *all, *backlog)
	if err != nil {
		return err
	}
	dids := make([]string, len(list))
	for i, f := range list {
		dids[i] = f.DID
	}
	profiles, err := a.Client.GetProfiles(ctx, dids)
	if err != nil {
		return err
	}
	byDID := map[string]bsky.Profile{}
	for _, p := range profiles {
		byDID[p.DID] = p
	}
	enc := json.NewEncoder(a.stdout)
	for _, f := range list {
		p, ok := byDID[f.DID]
		if !ok {
			p = f
		}
		status := a.State.Entries[p.DID].Decision
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
			status, p.Handle, p.PostsCount, p.FollowersCount, p.FollowsCount, termtext.HumanAge(p.Age()), p.DID)
	}
	return nil
}

// cmdBaseline marks every follower without a decision as skipped, so only
// followers who arrive afterwards show up by default.
func (a *CLI) cmdBaseline(ctx context.Context) error {
	list, err := a.Followers(ctx, 0, false, false)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, f := range list {
		a.State.Entries[f.DID] = store.Entry{Handle: f.Handle, Decision: store.Skipped, At: now}
	}
	if err := a.State.Save(); err != nil {
		return err
	}
	a.record(store.AuditEvent{Action: "baseline", OK: true, Detail: fmt.Sprintf("%d followers marked as old", len(list))})
	fmt.Fprintf(a.stdout, "baselined %d existing followers; only new ones will show by default (--backlog to revisit)\n", len(list))
	return nil
}

func (a *CLI) cmdShow(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("%w: show takes exactly one handle or DID", ErrUsage)
	}
	p, err := a.Client.GetProfile(ctx, normalizeActor(args[0]))
	if err != nil {
		return err
	}
	status := a.State.Entries[p.DID].Decision
	if p.Viewer.Blocking != "" {
		status = "blocked"
	}
	if status == "" {
		status = "new"
	}
	fmt.Fprintf(a.stdout, "%s (@%s)\nDID:       %s\nStatus:    %s\nCreated:   %s (%s)\nPosts:     %d\nFollowers: %d\nFollowing: %d\nAvatar:    %v\nURL:       %s\n\n%s\n",
		p.DisplayName, p.Handle, p.DID, status, p.CreatedAt, termtext.HumanAge(p.Age()),
		p.PostsCount, p.FollowersCount, p.FollowsCount, p.Avatar != "", bsky.ProfileURL(p.Handle), termtext.LinkifyIfTTY(p.Description))
	posts, err := a.Client.GetRecentPosts(ctx, p.DID, 5)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recent posts: %v\n", err)
		return nil
	}
	fmt.Fprintf(a.stdout, "\nRecent posts:\n")
	if len(posts) == 0 {
		fmt.Fprintln(a.stdout, "  (none)")
	}
	for _, post := range posts {
		fmt.Fprintf(a.stdout, "  %s\n", termtext.LinkifyIfTTY(formatPost(post, 0)))
	}
	return nil
}

func (a *CLI) cmdAct(ctx context.Context, cmd string, args []string) error {
	fs := NewFlagSet(cmd)
	dry := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	ids, err := a.identifiers(fs.Args())
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("%w: %s needs at least one handle or DID", ErrUsage, cmd)
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
// PostsList prints your own recent posts.
func (a *CLI) PostsList(ctx context.Context, n int, asJSON bool) error {
	enc := json.NewEncoder(a.stdout)
	cursor := ""
	for printed := 0; printed < n; {
		items, next, err := a.Client.GetOwnPosts(ctx, cursor, min(max(n-printed, 1), 100))
		if err != nil {
			return err
		}
		for _, it := range items {
			if printed++; printed > n {
				break
			}
			rkey := bsky.RKey(it.URI)
			if asJSON {
				_ = enc.Encode(map[string]any{"uri": it.URI, "rkey": rkey, "createdAt": it.Post.CreatedAt, "text": it.Post.Text})
				continue
			}
			line := fmt.Sprintf("%-14s %s", rkey, formatPost(it.Post, 0))
			if tw := termWidth(a.stdout); tw > 0 {
				line = termtext.TruncateWidth(line, tw-1)
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
func (a *CLI) cmdPost(ctx context.Context, args []string) (err error) {
	fs := NewFlagSet("post")
	dry := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
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
		return fmt.Errorf("%w: post needs some text", ErrUsage)
	}
	if err := bsky.ValidatePostText(text); err != nil {
		return err
	}
	ev := store.AuditEvent{Action: "post", DID: a.Client.DID, Handle: a.Client.Handle, DryRun: *dry, Detail: termtext.TruncateWidth(termtext.OneLine(text), 200)}
	defer func() {
		ev.OK = err == nil
		if err != nil {
			ev.Error = err.Error()
		}
		a.record(ev)
	}()
	if *dry {
		fmt.Fprintf(a.stdout, "[dry-run] would post (%d characters):\n%s\n", uniseg.GraphemeClusterCount(text), termtext.Sanitize(text))
		return nil
	}
	uri, err := a.Client.Post(ctx, text, nil)
	if err != nil {
		return err
	}
	ev.Record = uri
	fmt.Fprintf(a.stdout, "posted %s\n", bsky.PostURL(uri))
	return nil
}

// cmdDeletePost deletes your own posts, each given as an at:// URI, a
// bsky.app post link, or a bare record key.
func (a *CLI) cmdDeletePost(ctx context.Context, args []string) error {
	fs := NewFlagSet("delete-post")
	dry := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%w: delete-post needs at least one post", ErrUsage)
	}
	var failed int
	for _, ref := range fs.Args() {
		if err := a.deletePost(ctx, ref, *dry); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", termtext.Sanitize(ref), err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d failed", failed, fs.NArg())
	}
	return nil
}

func (a *CLI) deletePost(ctx context.Context, ref string, dry bool) (err error) {
	ev := store.AuditEvent{Action: "delete-post", DID: a.Client.DID, Handle: a.Client.Handle, DryRun: dry, Detail: termtext.TruncateWidth(termtext.Sanitize(ref), 200)}
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
		fmt.Fprintf(a.stdout, "[dry-run] would delete post %s\n", bsky.PostURL(uri))
		return nil
	}
	if err := a.Client.DeletePost(ctx, uri); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "deleted post %s\n", bsky.PostURL(uri))
	return nil
}

// postURI resolves ref to the AT URI of one of your own posts.
func (a *CLI) postURI(ctx context.Context, ref string) (string, error) {
	uri, err := a.resolvePost(ctx, ref)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(uri, "at://"+a.Client.DID+"/") {
		return "", errors.New("that post isn't yours; only your own posts can be deleted")
	}
	return uri, nil
}

// resolvePost turns an at:// URI, a bsky.app post link or a bare record key
// (one of your own posts) into the post's AT URI.
func (a *CLI) resolvePost(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	var actor, rkey string
	switch {
	case strings.HasPrefix(ref, "at://"):
		parts := strings.Split(strings.TrimPrefix(ref, "at://"), "/")
		if len(parts) != 3 || parts[1] != bsky.PostCollection {
			return "", fmt.Errorf("%w: not a post URI", ErrUsage)
		}
		actor, rkey = parts[0], parts[2]
	case strings.HasPrefix(ref, "https://"):
		parts := strings.Split(strings.TrimPrefix(ref, "https://"), "/")
		if len(parts) < 5 || parts[0] != "bsky.app" || parts[1] != "profile" || parts[3] != "post" {
			return "", fmt.Errorf("%w: not a bsky.app post link", ErrUsage)
		}
		actor, rkey = parts[2], parts[4]
	case ref != "" && !strings.ContainsAny(ref, "/:"):
		actor, rkey = a.Client.DID, ref
	default:
		return "", fmt.Errorf("%w: expected an at:// URI, bsky.app link or record key", ErrUsage)
	}
	if !strings.HasPrefix(actor, "did:") {
		p, err := a.Client.GetProfile(ctx, normalizeActor(actor))
		if err != nil {
			return "", err
		}
		actor = p.DID
	}
	return "at://" + actor + "/" + bsky.PostCollection + "/" + rkey, nil
}

// cmdLike likes or unlikes posts given as at:// URIs or bsky.app links.
func (a *CLI) cmdLike(ctx context.Context, cmd string, args []string) error {
	fs := NewFlagSet(cmd)
	dry := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%w: %s needs at least one post", ErrUsage, cmd)
	}
	var failed int
	for _, ref := range fs.Args() {
		if err := a.likePost(ctx, cmd, ref, *dry); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", termtext.Sanitize(ref), err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d failed", failed, fs.NArg())
	}
	return nil
}

func (a *CLI) likePost(ctx context.Context, cmd, ref string, dry bool) (err error) {
	ev := store.AuditEvent{Action: cmd, DID: a.Client.DID, Handle: a.Client.Handle, DryRun: dry, Detail: termtext.TruncateWidth(termtext.Sanitize(ref), 200)}
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
	post, like, err := a.Client.GetPost(ctx, uri)
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
		fmt.Fprintf(a.stdout, "already liked %s\n", bsky.PostURL(uri))
	case cmd == "unlike" && like == "":
		ev.Detail = "not liked"
		fmt.Fprintf(a.stdout, "you haven't liked %s\n", bsky.PostURL(uri))
	case cmd == "like":
		fmt.Fprintf(a.stdout, "%slike %s\n", prefix, bsky.PostURL(uri))
		if !dry {
			ev.Record, err = a.Client.Like(ctx, post)
		}
	default:
		ev.Record = like
		fmt.Fprintf(a.stdout, "%sunlike %s\n", prefix, bsky.PostURL(uri))
		if !dry {
			err = a.Client.Unlike(ctx, like)
		}
	}
	return err
}

func (a *CLI) act(ctx context.Context, cmd, id string, dry bool) (err error) {
	p, perr := a.Client.GetProfile(ctx, id)
	if perr != nil {
		a.record(store.AuditEvent{Action: cmd, Handle: id, DryRun: dry, Error: perr.Error()})
		return perr
	}
	prev := a.State.Entries[p.DID].Decision
	ev := store.AuditEvent{Action: cmd, DID: p.DID, Handle: p.Handle, DryRun: dry, Prev: prev, Snapshot: store.SnapshotOf(*p, true)}
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
			return a.State.Set(p.DID, p.Handle, store.Blocked)
		}
		fmt.Fprintf(a.stdout, "%sblock @%s (%s)\n", prefix, p.Handle, p.DID)
		if dry {
			return nil
		}
		uri, err := a.Client.Block(ctx, p.DID)
		if err != nil {
			return err
		}
		ev.Record = uri
		return a.State.Set(p.DID, p.Handle, store.Blocked)
	case "keep":
		fmt.Fprintf(a.stdout, "%skeep @%s\n", prefix, p.Handle)
		if dry {
			return nil
		}
		return a.State.Set(p.DID, p.Handle, store.Kept)
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
		uri, err := a.Client.Follow(ctx, p.DID)
		if err != nil {
			return err
		}
		ev.Record = uri
		return a.State.Set(p.DID, p.Handle, store.Followed)
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
		if err := a.Client.Unfollow(ctx, p.Viewer.Following); err != nil {
			return err
		}
		if a.State.Entries[p.DID].Decision == store.Followed {
			return a.State.Delete(p.DID)
		}
		return nil
	case "unblock":
		fmt.Fprintf(a.stdout, "%sunblock @%s (%s)\n", prefix, p.Handle, p.DID)
		ev.Record = p.Viewer.Blocking
		if dry {
			return nil
		}
		if p.Viewer.Blocking != "" {
			if err := a.Client.Unblock(ctx, p.Viewer.Blocking); err != nil {
				return err
			}
		}
		return a.State.Delete(p.DID)
	}
	return nil
}

// record writes an audit event; a failing log is reported but never hides the action's result.
func (a *CLI) record(e store.AuditEvent) {
	if e.Source == "" {
		e.Source = "cli"
	}
	if err := a.Audit.Log(e); err != nil {
		fmt.Fprintf(os.Stderr, "warning: audit log: %v\n", err)
	}
}

// identifiers expands "-" into whitespace-separated first fields of stdin lines.
func (a *CLI) identifiers(args []string) ([]string, error) {
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
func formatPost(p bsky.Post, max int) string {
	tag := ""
	switch {
	case p.Repost:
		tag = "[repost] "
	case p.Reply:
		tag = "[reply] "
	}
	age := termtext.HumanAgeISO(p.CreatedAt)
	text := termtext.OneLine(termtext.Sanitize(p.Text))
	if r := []rune(text); max > 0 && len(r) > max {
		text = string(r[:max-1]) + "…"
	}
	if len(p.Attach) > 0 {
		text = strings.TrimSpace(text + " " + termtext.Sanitize(strings.Join(p.Attach, " ")))
	}
	if c := p.Link; c != nil { // full URL here: CLI output is copied and piped
		card := "↗ " + c.URI
		if c.Title != "" && !strings.Contains(p.Text, c.URI) {
			card = "↗ " + c.Title + " " + c.URI
		} else if c.Title != "" {
			card = "↗ " + c.Title
		}
		text = strings.TrimSpace(text + " " + termtext.Sanitize(card))
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
