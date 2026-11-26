package store

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFailedStateMutationPreservesPreviousEntry(t *testing.T) {
	old := Entry{Handle: "old", Decision: Kept, At: time.Now().UTC()}
	for _, had := range []bool{false, true} {
		for _, op := range []string{"set", "put", "delete"} {
			s := &State{path: t.TempDir(), Entries: map[string]Entry{}}
			if had {
				s.Entries["did:a"] = old
			}
			var err error
			switch op {
			case "set":
				err = s.Set("did:a", "new", Blocked)
			case "put":
				err = s.Put("did:a", Entry{Decision: Skipped})
			case "delete":
				err = s.Delete("did:a")
			}
			got, ok := s.Entries["did:a"]
			if err == nil || ok != had || (had && got != old) {
				t.Errorf("%s had=%v: entry=%+v exists=%v err=%v", op, had, got, ok, err)
			}
		}
	}
}

func TestStateMergesInterleavedChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	a := NewState(path)
	b := NewState(path)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	check(a.Set("did:a", "a", Kept))
	check(b.Set("did:b", "b", Blocked))
	check(a.Put("did:b", Entry{Decision: Followed}))
	check(b.Delete("did:a"))
	check(a.Set("did:c", "c", Kept))
	count, err := b.PutMissing(map[string]Entry{
		"did:b": {Decision: Skipped},
		"did:d": {Decision: Skipped},
	})
	check(err)
	got, err := LoadState(path)
	check(err)
	if count != 1 || len(got.Entries) != 3 || got.Entries["did:b"].Decision != Followed ||
		got.Entries["did:c"].Decision != Kept || got.Entries["did:d"].Decision != Skipped {
		t.Fatalf("inserted %d; entries = %+v", count, got.Entries)
	}
}

func TestStateConcurrentProcesses(t *testing.T) {
	const workerEnv = "BSKYBAN_STATE_TEST_WORKER"
	if did := os.Getenv(workerEnv); did != "" {
		s, err := LoadState(os.Getenv("BSKYBAN_STATE_TEST_PATH"))
		if err != nil {
			t.Fatal(err)
		}
		for range 20 {
			if err := s.Set(did, did, Kept); err != nil {
				t.Fatal(err)
			}
		}
		return
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	dids := []string{"did:a", "did:b"}
	results := make(chan error, len(dids))
	for _, did := range dids {
		go func() {
			cmd := exec.Command(os.Args[0], "-test.run=^TestStateConcurrentProcesses$")
			cmd.Env = append(os.Environ(), workerEnv+"="+did, "BSKYBAN_STATE_TEST_PATH="+path)
			out, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("%s: %w\n%s", did, err, out)
			}
			results <- err
		}()
	}
	for range dids {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, did := range dids {
		if got.Entries[did].Decision != Kept {
			t.Errorf("missing successful decision for %s", did)
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Errorf("expected only the state and lock files, got %v (%v)", files, err)
	}
}
