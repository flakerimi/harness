package telegram

import (
	"fmt"
	"regexp"
	"strings"
)

// Telegram's HTML parse mode is far easier to get right than MarkdownV2 (which
// requires escaping ~15 characters everywhere). So we convert the model's
// markdown to a small, safe subset of HTML that Telegram accepts:
//   **b**/__b__ → <b>, *i*/_i_ → <i>, `c` → <code>, ```block``` → <pre>,
//   [t](u) → <a>, # heading → bold line, - bullet → •.
// Everything else is HTML-escaped. If Telegram still rejects the result, the
// caller falls back to plain text, so a reply is never lost.

var (
	reFence   = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\\n?(.*?)```")
	reCode    = regexp.MustCompile("`([^`\n]+)`")
	reLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reBold    = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reBoldU   = regexp.MustCompile(`__([^_\n]+)__`)
	reItalic  = regexp.MustCompile(`\*([^*\n]+)\*`)
	reHeading = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+(.+?)\s*#*$`)
	reBullet  = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+(.+)$`)
)

// MarkdownToHTML converts common markdown to the HTML subset Telegram renders.
func MarkdownToHTML(md string) string {
	var stash []string
	hold := func(s string) string {
		stash = append(stash, s)
		return fmt.Sprintf("\x00%d\x00", len(stash)-1)
	}

	// Protect code first — its contents must not be treated as markdown, only
	// HTML-escaped. Fenced blocks before inline spans.
	md = reFence.ReplaceAllStringFunc(md, func(m string) string {
		body := reFence.FindStringSubmatch(m)[1]
		return hold("<pre>" + escapeHTML(strings.Trim(body, "\n")) + "</pre>")
	})
	md = reCode.ReplaceAllStringFunc(md, func(m string) string {
		return hold("<code>" + escapeHTML(reCode.FindStringSubmatch(m)[1]) + "</code>")
	})

	// Escape the remaining text, then layer on the inline/line formatting.
	md = escapeHTML(md)
	md = reHeading.ReplaceAllString(md, "<b>$1</b>")
	md = reBullet.ReplaceAllString(md, "$1• $2")
	md = reLink.ReplaceAllString(md, `<a href="$2">$1</a>`)
	md = reBold.ReplaceAllString(md, "<b>$1</b>")
	md = reBoldU.ReplaceAllString(md, "<b>$1</b>")
	md = reItalic.ReplaceAllString(md, "<i>$1</i>")

	// Restore the protected code spans.
	for i, s := range stash {
		md = strings.ReplaceAll(md, fmt.Sprintf("\x00%d\x00", i), s)
	}
	return md
}

// escapeHTML escapes the three characters Telegram's HTML mode is sensitive to.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
