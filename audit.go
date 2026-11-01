package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// stateDir is where bskyban keeps everything it writes: decisions and the audit
// log. $XDG_STATE_HOME/bskyban, or ~/.local/state/bskyban.
func stateDir() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, "bskyban"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "bskyban"), nil
}

func defaultStatePath() (string, error) {
	dir, err := stateDir()
	return filepath.Join(dir, "state.json"), err
}

func defaultAuditPath() (string, error) {
	dir, err := stateDir()
	return filepath.Join(dir, "audit.jsonl"), err
}

// Snapshot is what the account looked like when it was acted on.
type Snapshot struct {
	CreatedAt string `json:"createdAt,omitempty"`
	Posts     *int   `json:"posts,omitempty"` // counts only when the full profile was loaded
	Followers *int   `json:"followers,omitempty"`
	Following *int   `json:"following,omitempty"`
	Avatar    bool   `json:"avatar"`
	Bio       string `json:"bio,omitempty"` // first 200 characters
}

// snapshotOf describes p. detailed says whether p came from a full profile
// fetch; summaries (timeline authors) have no counts, so those are left out
// rather than logged as a misleading 0.
func snapshotOf(p Profile, detailed bool) *Snapshot {
	bio := strings.Join(strings.Fields(p.Description), " ")
	if r := []rune(bio); len(r) > 200 {
		bio = string(r[:200])
	}
	s := &Snapshot{CreatedAt: p.CreatedAt, Avatar: p.Avatar != "", Bio: bio}
	if detailed {
		s.Posts, s.Followers, s.Following = &p.PostsCount, &p.FollowersCount, &p.FollowsCount
	}
	return s
}

// AuditEvent is one line of the audit log.
type AuditEvent struct {
	TS       string    `json:"ts"`
	Action   string    `json:"action"` // block, unblock, keep, skip, undo, baseline
	DID      string    `json:"did,omitempty"`
	Handle   string    `json:"handle,omitempty"`
	Source   string    `json:"source"`           // cli, tui:review, tui:following, tui:reposts
	Via      string    `json:"via,omitempty"`    // who reposted them into your timeline
	Record   string    `json:"record,omitempty"` // block record created or removed
	Prev     string    `json:"prev,omitempty"`   // decision recorded before this one
	Detail   string    `json:"detail,omitempty"`
	DryRun   bool      `json:"dryRun,omitempty"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Snapshot *Snapshot `json:"snapshot,omitempty"`
}

// Audit is an append-only JSON Lines log. A nil *Audit discards events.
type Audit struct {
	path string
	mu   sync.Mutex
}

func NewAudit(path string) *Audit { return &Audit{path: path} }

// Log appends e. It never rewrites or truncates the file, and holds no secrets.
func (l *Audit) Log(e AuditEvent) error {
	if l == nil {
		return nil
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// ReadAudit returns all events in the log, oldest first. A missing file is empty.
func ReadAudit(path string) ([]AuditEvent, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []AuditEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var e AuditEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, n, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// parseSince understands Go durations plus a "d" suffix for days (7d, 2d12h is not supported).
func parseSince(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil {
			return 0, fmt.Errorf("bad --since %q", s)
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad --since %q (try 48h or 7d)", s)
	}
	return d, nil
}

// runLog implements `bskyban log`. It needs no login.
func runLog(args []string, out io.Writer) error {
	fs := newFlagSet("log")
	since := fs.String("since", "", "")
	action := fs.String("action", "", "")
	who := fs.String("who", "", "")
	asJSON := fs.Bool("json", false, "")
	n := fs.Int("n", 50, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	path := os.Getenv("BSKYBAN_AUDIT")
	if path == "" {
		var err error
		if path, err = defaultAuditPath(); err != nil {
			return err
		}
	}
	events, err := ReadAudit(path)
	if err != nil {
		return err
	}
	var cutoff time.Time
	if *since != "" {
		d, err := parseSince(*since)
		if err != nil {
			return fmt.Errorf("%w: %v", errUsage, err)
		}
		cutoff = time.Now().Add(-d)
	}
	var shown []AuditEvent
	for _, e := range events {
		if t, err := time.Parse(time.RFC3339, e.TS); err == nil && t.Before(cutoff) {
			continue
		}
		if *action != "" && e.Action != *action {
			continue
		}
		if w := strings.TrimPrefix(*who, "@"); w != "" && !strings.Contains(e.Handle, w) && e.DID != w {
			continue
		}
		shown = append(shown, e)
	}
	if *n > 0 && len(shown) > *n {
		shown = shown[len(shown)-*n:]
	}
	enc := json.NewEncoder(out)
	for _, e := range shown {
		if *asJSON {
			_ = enc.Encode(e)
			continue
		}
		ts := e.TS
		if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
			ts = t.Local().Format("2006-01-02 15:04:05")
		}
		line := fmt.Sprintf("%s  %-8s %-34s %s", ts, e.Action, "@"+e.Handle, e.Source)
		if e.Via != "" {
			line += "  via @" + e.Via
		}
		if e.DryRun {
			line += "  [dry-run]"
		}
		if !e.OK {
			line += "  FAILED: " + e.Error
		}
		if e.Detail != "" {
			line += "  (" + e.Detail + ")"
		}
		fmt.Fprintln(out, terminalText(line))
	}
	return nil
}
