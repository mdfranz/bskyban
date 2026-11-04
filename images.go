package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// maxImageBytes caps one downloaded image; Bluesky's full-size images are well under it.
const maxImageBytes = 20 << 20

type (
	// imagesMsg carries downloaded images, ready to show with chafa.
	imagesMsg struct {
		dir   string // temporary directory holding the files; removed afterwards
		files []string
		err   error
	}
	// imagesDoneMsg reports that the image viewer has exited.
	imagesDoneMsg struct{ err error }
)

// haveChafa reports whether chafa is installed. Tests replace it.
var haveChafa = func() bool {
	_, err := exec.LookPath("chafa")
	return err == nil
}

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
// colon-separated commands; "%s" marks where the URL goes), otherwise xdg-open.
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
	return exec.Command("xdg-open", u)
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

// chafaKey (I, --fancy only) shows the selected post's images in the terminal with chafa.
func (m model) chafaKey() (model, tea.Cmd) {
	urls, ok := m.postImages()
	if !ok {
		m.status = "this post has no images"
		return m, nil
	}
	if !haveChafa() {
		m.status = "install chafa to view images here (sudo apt install chafa); i opens them in your browser"
		return m, nil
	}
	m.busy, m.err, m.status = true, nil, ""
	return m, func() tea.Msg { return downloadImages(urls) }
}

// showImages hands the terminal to chafa, one image per screen, and takes it back
// when you are done. The downloaded files are removed afterwards.
func (m model) showImages(msg imagesMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	if msg.err != nil || m.quitting {
		os.RemoveAll(msg.dir)
		if m.quitting {
			return m, tea.Quit
		}
		m.err = msg.err
		return m, nil
	}
	// $0 is a label; the files follow. chafa fits each image to the terminal.
	script := `n=$#; i=0
for f; do
	i=$((i+1)); clear
	chafa --animate=off "$f" || exit 1
	if [ "$i" -lt "$n" ]; then printf '\nimage %d of %d · Enter: next · q Enter: back ' "$i" "$n"
	else printf '\nimage %d of %d · Enter: back ' "$i" "$n"; fi
	read -r k; [ "$k" = q ] && break
done`
	cmd := exec.Command("sh", append([]string{"-c", script, "bskyban-images"}, msg.files...)...)
	dir := msg.dir
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		os.RemoveAll(dir)
		return imagesDoneMsg{err: err}
	})
}

// imageClient fetches images; tests replace it to reach a local TLS server.
var imageClient = &http.Client{Timeout: 30 * time.Second}

// downloadImages fetches the images into a new temporary directory.
func downloadImages(urls []string) imagesMsg {
	dir, err := os.MkdirTemp("", "bskyban-images-")
	if err != nil {
		return imagesMsg{err: err}
	}
	client := imageClient
	var files []string
	for i, u := range urls {
		if !strings.HasPrefix(u, "https://") {
			continue // only fetch over https
		}
		f := filepath.Join(dir, fmt.Sprintf("%02d", i+1))
		if err := download(client, u, f); err != nil {
			return imagesMsg{dir: dir, err: fmt.Errorf("download image %d: %w", i+1, err)}
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return imagesMsg{dir: dir, err: fmt.Errorf("no images to show")}
	}
	return imagesMsg{dir: dir, files: files}
}

func download(client *http.Client, url, path string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, maxImageBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxImageBytes {
		err = fmt.Errorf("larger than %d MB", maxImageBytes>>20)
	}
	return err
}
