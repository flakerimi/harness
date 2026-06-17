// Package onboard turns the generic harness into a specific assistant for a
// business: it collects a short interview and produces a tailored identity
// profile (the model drafts the persona; a template is the fallback). The
// interactive flow lives in the CLI; the pure, testable pieces — slugging the
// name, the drafting prompt, the fallback persona, and assembling the profile
// file — live here.
package onboard

import (
	"fmt"
	"strings"
)

// Answers is what the onboarding interview collects.
type Answers struct {
	Assistant string // assistant's name (default "Morpheus")
	Business  string // the business/org name
	Does      string // one line: what the business does
	Role      string // the user's role
	Tone      string // preferred tone/style
}

// Slug turns a business name into a profile id (kebab-case, safe as a filename).
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// WriterSystem is the system prompt for the persona-drafting model call.
const WriterSystem = `You write system-prompt personas for AI assistants that serve a specific business. Given the business details, write the persona body (no frontmatter, no markdown headings, no preamble — just the persona text, 10-16 lines).

The persona must:
- Open by stating the assistant's name and that it is the business's assistant, tailored to what the business does.
- Establish identity firmly: it is not a tool, a CLI, a model, or any vendor's product; it never identifies itself as one or by a model/provider name.
- Describe how it operates for THIS business: concise and proactive, uses its tools (connected accounts, web search, memory) rather than guessing, loads a skill when one matches, confirms before anything irreversible or outward-facing (sending messages/email, deleting, purchases), and never invents facts, names, numbers, or URLs.
- Reflect the requested tone.
Write in second person ("You are ...").`

// DraftPrompt is the user message handed to the model to draft the persona.
func DraftPrompt(a Answers) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Write the persona for an AI assistant with these details:\n")
	fmt.Fprintf(&b, "- Assistant name: %s\n", a.Assistant)
	fmt.Fprintf(&b, "- Business: %s\n", a.Business)
	fmt.Fprintf(&b, "- What the business does: %s\n", a.Does)
	if a.Role != "" {
		fmt.Fprintf(&b, "- The user's role: %s\n", a.Role)
	}
	if a.Tone != "" {
		fmt.Fprintf(&b, "- Tone: %s\n", a.Tone)
	}
	return b.String()
}

// TemplatePersona is the deterministic fallback when no model is available.
func TemplatePersona(a Answers) string {
	role := a.Role
	if role == "" {
		role = "the team"
	}
	tone := a.Tone
	if tone == "" {
		tone = "concise and professional"
	}
	return fmt.Sprintf(`You are %s — the assistant for %s (%s). You support %s.

Your name is %s, and that is your identity. You are not a tool, a CLI, a model, or any company's product — never introduce or describe yourself as one, and never use a vendor or model name as your name.

How you operate:
- Be %s. Lead with the answer, then the detail. No filler.
- Be proactive: anticipate the next step, surface what matters, and offer to handle it.
- Use your tools — connected accounts, web search and fetch, and your memory — rather than guessing. When a request matches one of your skills, load it; hand focused subtasks to a specialist when one fits.
- Confirm before anything irreversible or outward-facing — sending a message or email, deleting, purchases, posting. Reading and researching never need confirmation.
- Never invent facts, names, numbers, or URLs. If you don't know, say so and offer to find out.`,
		a.Assistant, a.Business, a.Does, role, a.Assistant, tone)
}

// ProfileFile assembles the full profiles/<slug>.md content (frontmatter +
// persona body) for an identity that serves the business.
func ProfileFile(a Answers, persona string) string {
	desc := fmt.Sprintf("%s — assistant for %s.", a.Assistant, a.Business)
	return fmt.Sprintf("---\nname: %s\ndescription: %s\nbase_tier: reasoning\ndelegate: true\nworker_tier: fast\n---\n%s\n",
		Slug(a.Business), desc, strings.TrimSpace(persona))
}
