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
	"strings"
	"time"
)

var errUsage = errors.New("usage")

const usageText = `bskyban - review and block Bluesky followers

Usage:
  bskyban [review] [--since N] [--backlog]   interactive TUI (new followers only)
  bskyban list [--since N] [--backlog] [--all] [--json]
  bskyban baseline                      mark all current non-followed followers as old
  bskyban show <handle|did>
  bskyban block [--dry-run] <handle|did>... (use - to read from stdin)
  bskyban keep <handle|did>...
  bskyban unblock [--dry-run] <handle|did>...

Environment:
  BSKY_HANDLE, BSKY_APP_PASSWORD   required (use an app password)
  BSKY_PDS                         optional, default https://bsky.social
  BSKYBAN_STATE                    optional state file path
`

type app struct {
	c      *Client
	st     *State
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
		if f.Viewer.Following != "" {
			continue // you follow them back; not a stranger
		}
		e, done := a.st.Entries[f.DID]
		if !done || (backlog && e.Decision == decSkipped) {
			out = append(out, f)
		}
	}
	return out, nil
}

func (a *app) run(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "list":
		return a.cmdList(ctx, args)
	case "baseline":
		return a.cmdBaseline(ctx)
	case "show":
		return a.cmdShow(ctx, args)
	case "block", "keep", "unblock":
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

func (a *app) act(ctx context.Context, cmd, id string, dry bool) error {
	p, err := a.c.GetProfile(ctx, id)
	if err != nil {
		return err
	}
	prefix := ""
	if dry {
		prefix = "[dry-run] would "
	}
	switch cmd {
	case "block":
		if p.Viewer.Blocking != "" {
			fmt.Fprintf(a.stdout, "%salready blocked: @%s\n", prefix, p.Handle)
			if dry {
				return nil
			}
			return a.st.Set(p.DID, p.Handle, decBlocked)
		}
		fmt.Fprintf(a.stdout, "%sblock @%s (%s)\n", prefix, p.Handle, p.DID)
		if dry {
			return nil
		}
		if _, err := a.c.Block(ctx, p.DID); err != nil {
			return err
		}
		return a.st.Set(p.DID, p.Handle, decBlocked)
	case "keep":
		fmt.Fprintf(a.stdout, "keep @%s\n", p.Handle)
		return a.st.Set(p.DID, p.Handle, decKept)
	case "unblock":
		fmt.Fprintf(a.stdout, "%sunblock @%s (%s)\n", prefix, p.Handle, p.DID)
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
	text := strings.Join(strings.Fields(p.Text), " ")
	if r := []rune(text); max > 0 && len(r) > max {
		text = string(r[:max-1]) + "…"
	}
	if len(p.Attach) > 0 {
		text = strings.TrimSpace(text + " " + strings.Join(p.Attach, " "))
	}
	if text == "" {
		text = "(no text)"
	}
	return fmt.Sprintf("%-5s %s%s", age, tag, text)
}

func normalizeActor(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "@") }

func humanAge(d time.Duration) string {
	switch {
	case d <= 0:
		return "?"
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 90*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
}
