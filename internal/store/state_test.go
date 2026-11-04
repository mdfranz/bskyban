package store

import (
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
