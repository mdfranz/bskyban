// Package store holds what bskyban writes locally: the decisions file (state.json)
// and the append-only audit log (audit.jsonl).
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Decisions recorded for an account.
const (
	Blocked  = "blocked"
	Kept     = "kept"
	Followed = "followed"
	Skipped  = "skipped" // deferred or baselined; hidden unless --backlog
)

// Entry is the decision recorded for one account.
type Entry struct {
	Handle   string    `json:"handle"`
	Decision string    `json:"decision"`
	At       time.Time `json:"at"`
}

// State maps accounts (by DID) to the decision made about them.
type State struct {
	path    string
	Entries map[string]Entry `json:"entries"` // keyed by DID
}

// NewState returns an empty state that saves to path. Nothing is read or written yet.
func NewState(path string) *State { return &State{path: path, Entries: map[string]Entry{}} }

func LoadState(path string) (*State, error) {
	s := NewState(path)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Entries == nil {
		s.Entries = map[string]Entry{}
	}
	return s, nil
}

// Path is the file the state is saved to.
func (s *State) Path() string { return s.path }

// update serializes changes with other processes using the same state path.
func (s *State) update(change func(map[string]Entry) bool) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Keep the lock file in place so every writer locks the same inode, even
	// when the state file is replaced.
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open state lock: %w", err)
	}
	defer lock.Close()

	if err := lockStateFile(lock); err != nil {
		return fmt.Errorf("lock state: %w", err)
	}

	current, err := LoadState(s.path)
	if err != nil {
		return err
	}
	if change(current.Entries) {
		if err := current.write(); err != nil {
			return err
		}
	}
	s.Entries = current.Entries
	return nil
}

func (s *State) write() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "."+filepath.Base(s.path)+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // fails harmlessly once renamed
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func (s *State) Set(did, handle, decision string) error {
	return s.Put(did, Entry{Handle: handle, Decision: decision, At: time.Now().UTC()})
}

func (s *State) Delete(did string) error {
	return s.update(func(entries map[string]Entry) bool {
		_, existed := entries[did]
		delete(entries, did)
		return existed
	})
}

// Put stores e as the entry for did, as-is (used to restore a previous decision).
func (s *State) Put(did string, e Entry) error {
	return s.update(func(entries map[string]Entry) bool {
		entries[did] = e
		return true
	})
}

// PutMissing adds decisions only for accounts without a saved decision.
func (s *State) PutMissing(batch map[string]Entry) (int, error) {
	count := 0
	err := s.update(func(entries map[string]Entry) bool {
		for did, e := range batch {
			if _, exists := entries[did]; !exists {
				entries[did] = e
				count++
			}
		}
		return count > 0
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
