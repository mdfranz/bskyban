package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const (
	decBlocked  = "blocked"
	decKept     = "kept"
	decFollowed = "followed"
	decSkipped  = "skipped" // deferred or baselined; hidden unless --backlog
)

type Entry struct {
	Handle   string    `json:"handle"`
	Decision string    `json:"decision"`
	At       time.Time `json:"at"`
}

type State struct {
	path    string
	Entries map[string]Entry `json:"entries"` // keyed by DID
}

func LoadState(path string) (*State, error) {
	s := &State{path: path, Entries: map[string]Entry{}}
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
