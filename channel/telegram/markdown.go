package telegram

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Telegram's HTML parse mode is far easier to get right than MarkdownV2 (which
// requires escaping ~15 characters everywhere). So we convert the model's
// markdown to a small, safe subset of HTML that Telegram accepts:
//   **b**/__b__ → <b>, *i*/_i_ → <i>, `c` → <code>, ```block``` → <pre>,
//   [t](u) → <a>, # heading → bold line, - bullet → •, and pipe tables → an
//   aligned monospace <pre> block (Telegram has no <table>).
// Everything else is HTML-escaped. If Telegram still rejects the result, the
// caller falls back to plain text, so a reply is never lost.

var (
	reFence    = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\\n?(.*?)```")
	reCode     = regexp.MustCompile("`([^`\n]+)`")
	reLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reBold     = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reBoldU    = regexp.MustCompile(`__([^_\n]+)__`)
	reItalic   = regexp.MustCompile(`\*([^*\n]+)\*`)
	reHeading  = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+(.+?)\s*#*$`)
	reBullet   = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+(.+)$`)
	reTableSep = regexp.MustCompile(`^\s*\|?[\s:|]*-[-\s:|]*\|?\s*$`)
)

// MarkdownToHTML converts common markdown to the HTML subset Telegram renders.
func MarkdownToHTML(md string) string {
	var stash []string
	hold := func(s string) string {
		stash = append(stash, s)
		return fmt.Sprintf("\x00%d\x00", len(stash)-1)
	}

	// Protect fenced code first — its contents (which may contain pipes) must
	// not be treated as markdown or as a table, only HTML-escaped.
	md = reFence.ReplaceAllStringFunc(md, func(m string) string {
		body := reFence.FindStringSubmatch(m)[1]
		return hold("<pre>" + escapeHTML(strings.Trim(body, "\n")) + "</pre>")
	})
	// Tables → aligned monospace blocks (before inline code, so cell backticks
	// are stripped as markdown rather than protected).
	md = renderTables(md, hold)
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

	// Restore the protected code/table spans.
	for i, s := range stash {
		md = strings.ReplaceAll(md, fmt.Sprintf("\x00%d\x00", i), s)
	}
	return md
}

// renderTables replaces each GFM pipe table (a header row, a dash separator,
// then data rows) with an aligned monospace <pre> block, stashed so the rest of
// the pipeline leaves it intact.
func renderTables(md string, hold func(string) string) string {
	lines := strings.Split(md, "\n")
	var out []string
	for i := 0; i < len(lines); {
		if i+1 < len(lines) && strings.Contains(lines[i], "|") && reTableSep.MatchString(lines[i+1]) {
			j := i + 2
			for j < len(lines) && strings.Contains(lines[j], "|") && strings.TrimSpace(lines[j]) != "" {
				j++
			}
			out = append(out, hold(tableToPre(lines[i:j])))
			i = j
		} else {
			out = append(out, lines[i])
			i++
		}
	}
	return strings.Join(out, "\n")
}

// tableToPre renders a table block (header, separator at index 1, data rows) as
// a column-aligned <pre>, its cells stripped of inline markdown and escaped.
func tableToPre(block []string) string {
	var rows [][]string
	for idx, ln := range block {
		if idx == 1 {
			continue // the |---|---| separator
		}
		rows = append(rows, splitRow(ln))
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	width := make([]int, cols)
	for _, r := range rows {
		for c := range r {
			if w := utf8.RuneCountInString(r[c]); w > width[c] {
				width[c] = w
			}
		}
	}
	cell := func(r []string, c int) string {
		v := ""
		if c < len(r) {
			v = r[c]
		}
		return escapeHTML(v + strings.Repeat(" ", width[c]-utf8.RuneCountInString(v)))
	}
	var b strings.Builder
	b.WriteString("<pre>")
	for ri, r := range rows {
		parts := make([]string, cols)
		for c := range cols {
			parts[c] = cell(r, c)
		}
		b.WriteString(strings.TrimRight(strings.Join(parts, "  "), " "))
		b.WriteByte('\n')
		if ri == 0 { // underline the header
			seps := make([]string, cols)
			for c := range cols {
				seps[c] = strings.Repeat("-", width[c])
			}
			b.WriteString(strings.Join(seps, "  "))
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n") + "</pre>"
}

// splitRow splits a table row into trimmed cells with inline markdown removed
// (bold/italic/code markers, and links reduced to their text).
func splitRow(ln string) []string {
	ln = strings.TrimSpace(ln)
	ln = strings.TrimPrefix(ln, "|")
	ln = strings.TrimSuffix(ln, "|")
	parts := strings.Split(ln, "|")
	for i := range parts {
		parts[i] = stripInlineMarkdown(parts[i])
	}
	return parts
}

// stripInlineMarkdown reduces a table cell to plain text (inside <pre>, markdown
// wouldn't be rendered anyway, so raw markers would just look like noise).
func stripInlineMarkdown(s string) string {
	s = strings.TrimSpace(s)
	s = reLink.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, "*", "")
	return strings.TrimSpace(s)
}

// escapeHTML escapes the three characters Telegram's HTML mode is sensitive to.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
