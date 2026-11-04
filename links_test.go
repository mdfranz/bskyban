package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mdfranz/bskyban/internal/bsky"
)

func TestUntrustedTerminalControlsAreRemoved(t *testing.T) {
	payload := "hello\x1b]52;c;YWJj\x07 https://example.com"
	var p bsky.Profile
	data, _ := json.Marshal(map[string]string{"displayName": payload, "description": payload})
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(p.DisplayName+p.Description, "\x1b\x07") {
		t.Fatalf("decoded profile retained controls: %+v", p)
	}
	if got := formatPost(bsky.Post{Text: payload, Attach: []string{payload}}, 0); strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("CLI post retained controls: %q", got)
	}
}
