package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestViewTwoPanels(t *testing.T) {
	m := newModel(&app{}, 0, false)
	m.loading = false
	m.width, m.height = 120, 40
	p := Profile{DID: "did:x", Handle: "junoview.bsky.social", DisplayName: "Junoview",
		Description: "A modern presentation editor. junoview.com", PostsCount: 43, FollowersCount: 40, FollowsCount: 580}
	m.list = []Profile{p}
	m.profiles[p.DID] = &p
	posts := []Post{{Text: "Code boxes in 13 colour themes. Free, in your browser: junoview.com #Python", CreatedAt: "2026-11-01T06:00:00Z"},
		{Text: "reply text", Reply: true, CreatedAt: "2026-11-01T06:00:00Z"}}
	m.posts[p.DID] = &posts

	v := m.View()
	if !strings.Contains(v, "\x1b]8;;https://bsky.app/profile/junoview.bsky.social") {
		t.Error("profile link missing")
	}
	if strings.Count(v, "bsky.app/profile/junoview.bsky.social") != 1 {
		t.Error("profile URL appears more than once (nested link?)")
	}
	v = regexp.MustCompile(`\x1b\]8;;[^\x1b]*\x1b\\`).ReplaceAllString(v, "") // drop link escapes
	t.Log("\n" + v)
	for _, w := range []string{"Junoview", "Recent posts", "Code boxes", "Followers", "b block"} {
		if !strings.Contains(v, w) {
			t.Errorf("view missing %q", w)
		}
	}
	for i, line := range strings.Split(v, "\n") {
		if lipgloss.Width(line) > 120 {
			t.Errorf("line %d is %d wide (>120)", i, lipgloss.Width(line))
		}
	}
	// side by side: a line containing both the profile and the posts panels
	found := false
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "Followers") && strings.Contains(line, "Code boxes") || strings.Contains(line, "Followers") && strings.Contains(line, "reply text") || strings.Contains(line, "Followers") && strings.Contains(line, "ago") {
			found = true
		}
	}
	if !found {
		t.Error("panels are not side by side")
	}
}
