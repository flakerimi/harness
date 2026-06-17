// Package profile defines named agent presets — persona + routing tiers +
// whether to delegate grunt work to a fast worker. A profile turns the generic
// engine into a specific assistant (meeting-prep, etc.) without touching the
// core. The same harness ships many profiles over one loop.
package profile

import (
	"sort"

	"github.com/flakerimi/harness/router"
)

// Profile is a declarative agent preset.
type Profile struct {
	Name     string
	Persona  string      // orchestrator system prompt
	BaseTier router.Tier // tier for the main (orchestrator) loop

	// Delegation: when true, the orchestrator gets a `delegate` tool that runs
	// subtasks on a fast worker, so research is cheap and only synthesis is
	// done at the expensive tier.
	Delegate      bool
	WorkerPersona string
	WorkerTier    router.Tier
}

var registry = map[string]Profile{
	meetingPrep.Name: meetingPrep,
}

// Get returns a profile by name.
func Get(name string) (Profile, bool) {
	p, ok := registry[name]
	return p, ok
}

// Names lists the available profile names.
func Names() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
