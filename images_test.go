package main

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

// withBrowser records what the browser launcher would open, failing if err is set.
func withBrowser(t *testing.T, err error) *[]string {
	var opened []string
	old := openURL
	openURL = func(u string) error {
		if err != nil {
			return err
		}
		opened = append(opened, u)
		return nil
	}
	t.Cleanup(func() { openURL = old })
	return &opened
}

func TestLowerIOpensImagesInTheBrowser(t *testing.T) {
	opened := withBrowser(t, nil)
	m, _ := feedWithTimeline(t)
	m = press(m, "i")
	if !strings.Contains(m.status, "no images") || len(*opened) != 0 {
		t.Errorf("status = %q opened=%v", m.status, *opened)
	}
	urls := []string{"https://cdn.bsky.app/img/1.jpg", "https://cdn.bsky.app/img/2.jpg"}
	m.items[0].Post.Images = urls
	if !strings.Contains(m.hint(), "i open images") {
		t.Errorf("hint should offer i: %s", m.hint())
	}
	m = press(m, "i")
	if strings.Join(*opened, ",") != strings.Join(urls, ",") || !strings.Contains(m.status, "opened 2 image(s)") || m.busy {
		t.Errorf("opened=%v status=%q", *opened, m.status)
	}

	withBrowser(t, errors.New("open: not found"))
	m = press(m, "i")
	if m.err == nil || !strings.Contains(m.err.Error(), "not found") {
		t.Errorf("a failing browser launcher should be reported: %v", m.err)
	}
}

func TestBrowserCommandPrefersBROWSER(t *testing.T) {
	u := "https://cdn.bsky.app/img/1.jpg"
	wantDefault := "xdg-open " + u
	switch runtime.GOOS {
	case "darwin":
		wantDefault = "open " + u
	case "windows":
		wantDefault = "rundll32 url.dll,FileProtocolHandler " + u
	}
	for browser, want := range map[string]string{
		"":                              wantDefault,
		"/usr/bin/garcon-url-handler":   "/usr/bin/garcon-url-handler " + u,
		"firefox --new-tab %s:chromium": "firefox --new-tab " + u,
		"  :lynx":                       "lynx " + u,
	} {
		if got := strings.Join(browserCommand(browser, u).Args, " "); got != want {
			t.Errorf("BROWSER=%q: got %q, want %q", browser, got, want)
		}
	}
}
