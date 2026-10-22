package main

import (
	"html"
	"regexp"
	"strconv"
	"unicode/utf8"
)

// Posts are written in a small Markdown subset: **bold**, *italic*,
// `code`, [text](url). Telegram and MAX get HTML, VK — plain text.

var (
	reCode   = regexp.MustCompile("`([^`\n]+)`")
	reLink   = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	reBold   = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reItalic = regexp.MustCompile(`\*([^*\n]+)\*`)
	reSlot   = regexp.MustCompile("\x00(\\d+)\x00")
)

// toHTML protects code spans and links in slots first, so markup inside
// them (a * in a URL, ** in code) is not rewritten.
func toHTML(md string) string {
	var slots []string
	slot := func(s string) string {
		slots = append(slots, s)
		return "\x00" + strconv.Itoa(len(slots)-1) + "\x00"
	}
	s := reCode.ReplaceAllStringFunc(md, func(m string) string {
		return slot("<code>" + html.EscapeString(reCode.FindStringSubmatch(m)[1]) + "</code>")
	})
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		g := reLink.FindStringSubmatch(m)
		return slot(`<a href="` + html.EscapeString(g[2]) + `">` + html.EscapeString(g[1]) + "</a>")
	})
	s = html.EscapeString(s)
	s = reBold.ReplaceAllString(s, "<b>$1</b>")
	s = reItalic.ReplaceAllString(s, "<i>$1</i>")
	return reSlot.ReplaceAllStringFunc(s, func(m string) string {
		i, err := strconv.Atoi(reSlot.FindStringSubmatch(m)[1])
		if err != nil {
			return m
		}
		return slots[i]
	})
}

func toPlain(md string) string {
	s := reCode.ReplaceAllString(md, "$1")
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		g := reLink.FindStringSubmatch(m)
		if g[1] == g[2] {
			return g[2]
		}
		return g[1] + " (" + g[2] + ")"
	})
	s = reBold.ReplaceAllString(s, "$1")
	return reItalic.ReplaceAllString(s, "$1")
}

// textLen is the length platforms count against their limits: visible
// characters, markup excluded.
func textLen(md string) int {
	return utf8.RuneCountInString(toPlainNoURL(md))
}

func toPlainNoURL(md string) string {
	return toPlain(reLink.ReplaceAllString(md, "$1"))
}
