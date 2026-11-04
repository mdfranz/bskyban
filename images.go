package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// openURL opens u in the browser without waiting for it. Tests replace it.
var openURL = func(u string) error {
	cmd := browserCommand(os.Getenv("BROWSER"), u)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap it; the browser lives on after the launcher exits
	return nil
}

// browserCommand opens u with $BROWSER when it is set (the first of its
// colon-separated commands; "%s" marks where the URL goes), otherwise the
// platform's default launcher (open on macOS, xdg-open on Linux).
// On ChromeOS Linux, $BROWSER is garcon-url-handler, which opens ChromeOS's Chrome;
// xdg-open would pick a browser installed inside the Linux container instead.
func browserCommand(browser, u string) *exec.Cmd {
	for _, b := range strings.Split(browser, ":") {
		fields := strings.Fields(b)
		if len(fields) == 0 {
			continue
		}
		args, placed := fields[1:], false
		for i, a := range args {
			if strings.Contains(a, "%s") {
				args[i], placed = strings.ReplaceAll(a, "%s", u), true
			}
		}
		if !placed {
			args = append(args, u)
		}
		return exec.Command(fields[0], args...)
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u)
	default:
		return exec.Command("xdg-open", u)
	}
}

// postImages is the selected post's image URLs, or a status saying it has none.
func (m model) postImages() ([]string, bool) {
	it, ok := m.selected()
	if !ok || len(it.Post.Images) == 0 {
		return nil, false
	}
	return it.Post.Images, true
}

// openImagesKey (i) opens the selected post's images in the browser.
func (m model) openImagesKey() (model, tea.Cmd) {
	urls, ok := m.postImages()
	if !ok {
		m.status = "this post has no images"
		return m, nil
	}
	for _, u := range urls {
		if err := openURL(u); err != nil {
			m.err = fmt.Errorf("open image: %w", err)
			return m, nil
		}
	}
	m.status = fmt.Sprintf("opened %d image(s) in your browser", len(urls))
	return m, nil
}
