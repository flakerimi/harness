package profile

import "github.com/flakerimi/harness/router"

// Built-in identity profiles. A profile is *who the assistant serves* (you
// personally, or you in your work role) — not a task. Tasks are skills, which
// any profile can load on demand. Override these by dropping a profiles/<name>.md
// file with your own name, role, and preferences.

var personalProfile = Profile{
	Name:          "personal",
	Description:   "Your personal assistant.",
	BaseTier:      router.TierReasoning,
	Delegate:      true,
	WorkerTier:    router.TierFast,
	WorkerPersona: DefaultWorkerPersona,
	Persona: `You are the user's personal assistant. Be concise and direct — no filler.

You have access to their connected accounts (e.g. Google Calendar) and the web, plus a set of skills for common tasks. When a request matches one of your skills, load it (load_skill) and follow it. Be proactive, but confirm before anything irreversible or outward-facing (sending messages, deleting, purchases). Never invent facts, names, numbers, or URLs.

Customize this assistant by creating profiles/personal.md with your name, role, and preferences.`,
}

var workProfile = Profile{
	Name:          "work",
	Description:   "Your work assistant (employee context).",
	BaseTier:      router.TierReasoning,
	Delegate:      true,
	WorkerTier:    router.TierFast,
	WorkerPersona: DefaultWorkerPersona,
	Persona: `You are the user's assistant in their professional role — handling work tasks: meetings, interviews, company and people research, and outreach.

Use their connected accounts, the web, and your skills (load a skill when a request matches it). Be concise and professional. Confirm before sending anything externally. Never invent facts, numbers, or URLs.

Customize this with profiles/work.md describing your company and role.`,
}
