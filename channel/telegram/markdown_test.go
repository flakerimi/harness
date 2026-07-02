package telegram

import (
	"strings"
	"testing"
)

func TestMarkdownToHTML(t *testing.T) {
	cases := map[string]string{
		"**bold**":                    "<b>bold</b>",
		"*italic*":                    "<i>italic</i>",
		"__bold__":                    "<b>bold</b>",
		"`code`":                      "<code>code</code>",
		"[Anthropic](https://x.test)": `<a href="https://x.test">Anthropic</a>`,
		"# Heading":                   "<b>Heading</b>",
		"- item":                      "• item",
	}
	for in, want := range cases {
		if got := MarkdownToHTML(in); !strings.Contains(got, want) {
			t.Errorf("MarkdownToHTML(%q) = %q, want to contain %q", in, got, want)
		}
	}
}

func TestMarkdownTable(t *testing.T) {
	md := "| Style | Syntax |\n|---|---|\n| **Bold** | `*b*` |\n| Italic | _i_ |"
	got := MarkdownToHTML(md)
	if !strings.Contains(got, "<pre>") || !strings.Contains(got, "</pre>") {
		t.Fatalf("table should become a <pre> block:\n%s", got)
	}
	// Header cells present, inline markdown stripped, columns aligned (padded).
	if !strings.Contains(got, "Style") || !strings.Contains(got, "Syntax") {
		t.Errorf("header cells missing:\n%s", got)
	}
	if strings.Contains(got, "**Bold**") || strings.Contains(got, "`*b*`") {
		t.Errorf("cell markdown should be stripped:\n%s", got)
	}
	if !strings.Contains(got, "Bold  ") { // padded to the column width of "Italic"/"Style"
		t.Errorf("columns not aligned/padded:\n%s", got)
	}
}

func TestMarkdownEscapesAndProtectsCode(t *testing.T) {
	// Raw < > & must be escaped so Telegram's HTML parser doesn't choke.
	got := MarkdownToHTML("use a < b && c > d")
	if strings.Contains(got, "<b") || !strings.Contains(got, "&lt;") || !strings.Contains(got, "&amp;") {
		t.Errorf("unescaped special chars: %q", got)
	}

	// Inside a code span, markdown is inert and angle brackets are escaped.
	code := MarkdownToHTML("`x < *y*`")
	if !strings.Contains(code, "<code>x &lt; *y*</code>") {
		t.Errorf("code span not protected/escaped: %q", code)
	}

	// A fenced block becomes <pre> with its body escaped, formatting untouched.
	pre := MarkdownToHTML("```\nif a < b { **x** }\n```")
	if !strings.Contains(pre, "<pre>") || !strings.Contains(pre, "&lt;") || strings.Contains(pre, "<b>") {
		t.Errorf("fenced block not handled: %q", pre)
	}
}
