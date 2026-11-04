// Package store holds what bskyban writes locally: the decisions file (state.json)
// and the append-only audit log (audit.jsonl).
package store

import (
	"encoding/json"
	"errors"
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

func (s *State) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *State) Set(did, handle, decision string) error {
	return s.Put(did, Entry{Handle: handle, Decision: decision, At: time.Now().UTC()})
}

func (s *State) Delete(did string) error {
	prev, had := s.Entries[did]
	delete(s.Entries, did)
	if err := s.Save(); err != nil {
		if had {
			s.Entries[did] = prev
		}
		return err
	}
	return nil
}

// Put stores e as the entry for did, as-is (used to restore a previous decision).
func (s *State) Put(did string, e Entry) error {
	prev, had := s.Entries[did]
	s.Entries[did] = e
	if err := s.Save(); err != nil {
		if had {
			s.Entries[did] = prev
		} else {
			delete(s.Entries, did)
		}
		return err
	}
	return nil
}
