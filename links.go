package main

import (
	"os"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Matches http(s) URLs and bare domains with a common TLD. Stops at whitespace
// and ESC so it can run over already-styled (ANSI) text.
var linkRe = regexp.MustCompile(`(?i)(?:https?://[^\s\x1b]+|\b[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.(?:com|org|net|io|app|dev|ai|co|me|xyz|social|blog|info|tv|ly|edu|gov|uk|de|fr|ca|au)\b(?:/[^\s\x1b]*)?)`)

// ansiRe matches SGR/CSI styling sequences; links must never be detected inside them.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func urlTarget(url string) string {
	if strings.HasPrefix(strings.ToLower(url), "http") {
		return url
	}
	return "https://" + url
}

// linkify wraps URLs in OSC 8 hyperlinks so supporting terminals make them clickable.
// Styling escape sequences are left untouched.
func linkify(s string) string {
	var b strings.Builder
	pos := 0
	for _, loc := range ansiRe.FindAllStringIndex(s, -1) {
		b.WriteString(linkifyText(s[pos:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])
		pos = loc[1]
	}
	b.WriteString(linkifyText(s[pos:]))
	return b.String()
}

func linkifyText(s string) string {
	return linkRe.ReplaceAllStringFunc(s, func(m string) string {
		url := strings.TrimRight(m, ".,;:!?)]}\"'")
		tail := m[len(url):]
		return osc8(urlTarget(url), url) + tail
	})
}

// linkifyIfTTY only adds escape codes when stdout is a terminal.
func linkifyIfTTY(s string) string {
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return linkify(s)
	}
	return s
}

func osc8(target, text string) string {
	return "\x1b]8;;" + target + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

const urlTrailing = ".,;:!?)]}\"'"

var oscRe = regexp.MustCompile(`\x1b\][^\x1b]*\x1b\\`)

// visibleWidth is the on-screen width of s, ignoring hyperlink escapes.
func visibleWidth(s string) int { return lipgloss.Width(oscRe.ReplaceAllString(s, "")) }

type piece struct{ text, target string }

// splitWord splits a word into plain and URL pieces (URLs carry their target).
func splitWord(word string) []piece {
	var out []piece
	pos := 0
	for _, loc := range linkRe.FindAllStringIndex(word, -1) {
		start := loc[0]
		url := strings.TrimRight(word[start:loc[1]], urlTrailing)
		target := urlTarget(url)
		text := url
		if start > 0 && word[start-1] == '@' && !strings.Contains(url, "/") {
			// an @mention: link to the profile and keep the @ in the link text
			start--
			text = "@" + url
			target = "https://bsky.app/profile/" + url
		}
		if start > pos {
			out = append(out, piece{text: word[pos:start]})
		}
		out = append(out, piece{text: text, target: target})
		if tail := word[loc[0]+len(url) : loc[1]]; tail != "" {
			out = append(out, piece{text: tail})
		}
		pos = loc[1]
	}
	if pos < len(word) {
		out = append(out, piece{text: word[pos:]})
	}
	return out
}

// wrapText word-wraps text to width columns. URLs are never shortened: one too
// long for a line is split across lines, and every fragment is a hyperlink to
// the full URL. Newlines in text are kept.
func wrapText(text string, width int) []string {
	width = max(width, 8)
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		var cur strings.Builder
		curW := 0
		flush := func() { lines = append(lines, cur.String()); cur.Reset(); curW = 0 }
		for _, word := range words {
			pieces := splitWord(word)
			wordW := 0
			for _, p := range pieces {
				wordW += lipgloss.Width(p.text)
			}
			if curW > 0 && curW+1+wordW > width {
				flush()
			}
			if curW > 0 {
				cur.WriteByte(' ')
				curW++
			}
			for _, p := range pieces {
				rs := []rune(p.text)
				for i := 0; i < len(rs); {
					if curW >= width {
						flush()
					}
					j, w := i, 0
					for j < len(rs) {
						rw := lipgloss.Width(string(rs[j]))
						if curW+w+rw > width {
							break
						}
						w += rw
						j++
					}
					if j == i { // next rune doesn't fit in what is left
						if curW > 0 {
							flush()
							continue
						}
						j, w = i+1, lipgloss.Width(string(rs[i]))
					}
					frag := string(rs[i:j])
					if p.target != "" {
						frag = osc8(p.target, frag)
					}
					cur.WriteString(frag)
					curW += w
					i = j
				}
			}
		}
		flush()
	}
	return lines
}
