package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// withChafa pretends chafa is (or isn't) installed.
func withChafa(t *testing.T, installed bool) {
	old := haveChafa
	haveChafa = func() bool { return installed }
	t.Cleanup(func() { haveChafa = old })
}

// withBrowser records what xdg-open would open, failing if err is set.
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

func TestShiftIDownloadsThenHandsTheTerminalToChafa(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.jpg" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("fake image bytes " + r.URL.Path))
	}))
	defer srv.Close()
	old := imageClient
	imageClient = srv.Client()
	t.Cleanup(func() { imageClient = old })
	withChafa(t, true)

	m, _ := feedWithTimeline(t)
	m.fancy = true
	m.items[0].Post.Images = []string{srv.URL + "/a.jpg", srv.URL + "/b.jpg", "http://plain.example/c.jpg"}
	if h := m.hint(); !strings.Contains(h, "i open images") || !strings.Contains(h, "I view here") {
		t.Errorf("hint should offer i and I on a post with images: %s", h)
	}
	next, cmd := m.Update(key("I"))
	m = next.(model)
	if !m.busy || cmd == nil {
		t.Fatal("I should start downloading")
	}
	msg := cmd().(imagesMsg)
	if msg.err != nil || len(msg.files) != 2 {
		t.Fatalf("want the 2 https images: files=%v err=%v", msg.files, msg.err)
	}
	if b, _ := os.ReadFile(msg.files[1]); string(b) != "fake image bytes /b.jpg" {
		t.Errorf("file 2 = %q", b)
	}
	next, cmd = m.Update(msg)
	m = next.(model)
	if m.busy || cmd == nil {
		t.Fatalf("after downloading, the viewer should run: busy=%v cmd=%v", m.busy, cmd != nil)
	}
	os.RemoveAll(msg.dir) // the viewer's exit callback would do this

	// a failed download is reported and leaves nothing behind
	m.items[0].Post.Images = []string{srv.URL + "/missing.jpg"}
	_, cmd = m.Update(key("I"))
	msg = cmd().(imagesMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "404") {
		t.Fatalf("err = %v", msg.err)
	}
	next, _ = m.Update(msg)
	if next.(model).err == nil {
		t.Error("the download error should be shown")
	}
	if _, err := os.Stat(msg.dir); !os.IsNotExist(err) {
		t.Errorf("temporary directory should be removed: %v", err)
	}
}

func TestShiftIWithoutChafaOrImages(t *testing.T) {
	withChafa(t, false)
	m, _ := feedWithTimeline(t)
	m.fancy = true
	m = press(m, "I")
	if !strings.Contains(m.status, "no images") {
		t.Errorf("status = %q", m.status)
	}
	m.items[0].Post.Images = []string{"https://cdn.bsky.app/img/1.jpg"}
	m = press(m, "I")
	if !strings.Contains(m.status, "install chafa") || !strings.Contains(m.status, "i opens them in your browser") || m.busy {
		t.Errorf("without chafa it should say how to get it and point to i: %q", m.status)
	}
}

func TestShiftINeedsFancyMode(t *testing.T) {
	withChafa(t, true)
	m, _ := feedWithTimeline(t)
	m.items[0].Post.Images = []string{"https://cdn.bsky.app/img/1.jpg"}
	if strings.Contains(m.hint(), "I view here") {
		t.Error("no I hint without --fancy")
	}
	next, cmd := m.Update(key("I"))
	if m2 := next.(model); m2.busy || cmd != nil || m2.status != "" {
		t.Errorf("I should do nothing without --fancy: busy=%v cmd=%v status=%q", m2.busy, cmd != nil, m2.status)
	}
}

func TestLowerIOpensImagesInTheBrowser(t *testing.T) {
	opened := withBrowser(t, nil)
	m, _ := feedWithTimeline(t) // works without --fancy
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

	withBrowser(t, errors.New("xdg-open: not found"))
	m = press(m, "i")
	if m.err == nil || !strings.Contains(m.err.Error(), "not found") {
		t.Errorf("a failing xdg-open should be reported: %v", m.err)
	}
}

func TestBrowserCommandPrefersBROWSER(t *testing.T) {
	u := "https://cdn.bsky.app/img/1.jpg"
	for browser, want := range map[string]string{
		"":                              "xdg-open " + u,
		"/usr/bin/garcon-url-handler":   "/usr/bin/garcon-url-handler " + u,
		"firefox --new-tab %s:chromium": "firefox --new-tab " + u,
		"  :lynx":                       "lynx " + u,
	} {
		if got := strings.Join(browserCommand(browser, u).Args, " "); got != want {
			t.Errorf("BROWSER=%q: got %q, want %q", browser, got, want)
		}
	}
}
