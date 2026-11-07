package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mdfranz/bskyban/internal/bsky"
	"github.com/mdfranz/bskyban/internal/termtext"
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

func DefaultStatePath() (string, error) {
	dir, err := stateDir()
	return filepath.Join(dir, "state.json"), err
}

func DefaultAuditPath() (string, error) {
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

// SnapshotOf describes p. detailed says whether p came from a full profile
// fetch; summaries (timeline authors) have no counts, so those are left out
// rather than logged as a misleading 0.
func SnapshotOf(p bsky.Profile, detailed bool) *Snapshot {
	bio := termtext.OneLine(p.Description)
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
	Action   string    `json:"action"` // block, unblock, follow, unfollow, post, delete-post, like, unlike, refresh (failures only), keep, skip, undo, baseline
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

// Path is the file the log is written to.
func (l *Audit) Path() string { return l.path }

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
