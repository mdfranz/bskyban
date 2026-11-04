package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mdfranz/bskyban/internal/store"
	"github.com/mdfranz/bskyban/internal/termtext"
)

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

// RunLog implements `bskyban log`. It needs no login.
func RunLog(args []string, out io.Writer) error {
	fs := NewFlagSet("log")
	since := fs.String("since", "", "")
	action := fs.String("action", "", "")
	who := fs.String("who", "", "")
	asJSON := fs.Bool("json", false, "")
	n := fs.Int("n", 50, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	path := os.Getenv("BSKYBAN_AUDIT")
	if path == "" {
		var err error
		if path, err = store.DefaultAuditPath(); err != nil {
			return err
		}
	}
	events, err := store.ReadAudit(path)
	if err != nil {
		return err
	}
	var cutoff time.Time
	if *since != "" {
		d, err := parseSince(*since)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUsage, err)
		}
		cutoff = time.Now().Add(-d)
	}
	var shown []store.AuditEvent
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
		fmt.Fprintln(out, termtext.Sanitize(line))
	}
	return nil
}
