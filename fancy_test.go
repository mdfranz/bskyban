package main

import (
	"strings"
	"testing"
	"time"
)

// fancyModel is a refresh model at the top of the list with a clock the test controls.
func fancyModel(t *testing.T, s *script, fancy bool, shown ...FeedItem) (model, *time.Time) {
	m := refreshModel(t, s, shown...)
	m.fancy = fancy
	clock := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	return m, &clock
}

// view is the screen without colours, so the test can look for the plain marks.
func view(m model) string { return ansiRe.ReplaceAllString(m.View(), "") }

func TestFancyOffAddsNoHighlight(t *testing.T) {
	A := post("A", "a", "")
	s := &script{pages: [][]FeedItem{{post("N1", "n1", ""), A}}}
	m, _ := fancyModel(t, s, false, A)
	m = check(t, m, false)
	if uris(m.items) != "N1,A" || len(m.fresh) != 0 || m.animating {
		t.Fatalf("items=%s fresh=%d animating=%v", uris(m.items), len(m.fresh), m.animating)
	}
	if _, cmd := m.startAnim(); cmd != nil {
		t.Error("nothing to animate without --fancy")
	}
	if strings.Contains(view(m), "┃") {
		t.Errorf("no highlight bar without --fancy:\n%s", view(m))
	}
}

func TestFancyHighlightsNewPostsThenFades(t *testing.T) {
	A := post("A", "a", "")
	s := &script{pages: [][]FeedItem{{post("N1", "n1", ""), post("N2", "n2", ""), A}}}
	m, clock := fancyModel(t, s, true, A)
	m, cmd := m.startCheck(false)
	next, anim := m.Update(cmd())
	m = next.(model)
	if len(m.fresh) != 2 || !m.animating || anim == nil {
		t.Fatalf("fresh=%d animating=%v anim=%v", len(m.fresh), m.animating, anim != nil)
	}
	v := view(m)
	if n := strings.Count(v, ".bsky.social) new"); n != 2 {
		t.Errorf("want a new tag on the 2 inserted posts, got %d:\n%s", n, v)
	}
	if !strings.Contains(v, "┃") {
		t.Errorf("the unselected new post should have the bar:\n%s", v)
	}

	*clock = clock.Add(time.Second) // half way: still highlighted, still ticking
	next, anim = m.Update(animTickMsg{})
	m = next.(model)
	if len(m.fresh) != 2 || anim == nil {
		t.Fatalf("mid-fade: fresh=%d anim=%v", len(m.fresh), anim != nil)
	}

	*clock = clock.Add(1100 * time.Millisecond) // past freshFor
	next, anim = m.Update(animTickMsg{})
	m = next.(model)
	if len(m.fresh) != 0 || m.animating || anim != nil {
		t.Fatalf("after fading: fresh=%d animating=%v anim=%v", len(m.fresh), m.animating, anim != nil)
	}
	if v := view(m); strings.Contains(v, ".bsky.social) new") || strings.Contains(v, "┃") {
		t.Errorf("highlight should be gone:\n%s", v)
	}
}

func TestFadeColourMovesAlongTheRamp(t *testing.T) {
	m, clock := fancyModel(t, &script{}, true)
	m.fresh["k"] = *clock
	first, _ := m.freshStyle("k")
	*clock = clock.Add(freshFor - time.Millisecond)
	last, ok := m.freshStyle("k")
	if first.GetForeground() != theme.Fresh[0] || last.GetForeground() != theme.Fresh[len(theme.Fresh)-1] {
		t.Errorf("fade should use the active theme's endpoints")
	}
	if !ok || first.GetForeground() == last.GetForeground() {
		t.Errorf("colour should change as it fades: %v -> %v", first.GetForeground(), last.GetForeground())
	}
	*clock = clock.Add(time.Millisecond)
	if _, ok := m.freshStyle("k"); ok {
		t.Error("highlight should end at freshFor")
	}
}

func TestEnvOn(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, "YES": true, " yes ": true, "": false, "0": false, "no": false} {
		t.Setenv("BSKYBAN_FANCY", v)
		if got := envOn("BSKYBAN_FANCY"); got != want {
			t.Errorf("%q: got %v", v, got)
		}
	}
}
