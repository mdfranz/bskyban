package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUntrustedTerminalControlsAreRemoved(t *testing.T) {
	payload := "hello\x1b]52;c;YWJj\x07 https://example.com"
	var p Profile
	data, _ := json.Marshal(map[string]string{"displayName": payload, "description": payload})
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(p.DisplayName+p.Description, "\x1b\x07") {
		t.Fatalf("decoded profile retained controls: %+v", p)
	}
	if got := formatPost(Post{Text: payload, Attach: []string{payload}}, 0); strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("CLI post retained controls: %q", got)
	}
}

func TestExpandLinks(t *testing.T) {
	text := "See www.news.com/ted-cruz-say... now"
	f := facet{}
	f.Index.ByteStart, f.Index.ByteEnd = 4, 32
	f.Features = []struct {
		Type string `json:"$type"`
		URI  string `json:"uri"`
	}{{Type: "app.bsky.richtext.facet#link", URI: "https://www.news.com/ted-cruz-says-all"}}
	got := expandLinks(text, []facet{f})
	if got != "See https://www.news.com/ted-cruz-says-all now" {
		t.Errorf("got %q", got)
	}
}
