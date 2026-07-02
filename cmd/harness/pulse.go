package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/schedule"
)

// pulsePrompt is what the scheduled pulse run executes. It works even without
// the pulse skill installed, but loads it when present. An empty reply means
// deliver() sends nothing — silence is the default, not a failure.
const pulsePrompt = "This is your scheduled pulse — a quiet check-in, not a conversation. " +
	"If you have a 'pulse' skill, load it (load_skill) and follow it. Otherwise: check task_status " +
	"for finished or failed background work worth mentioning; if calendar tools are connected, look at " +
	"the next few hours; call resurface once and mention the note only if it is genuinely timely. " +
	"If something deserves the user's attention, reply with ONE short, warm message (2–6 lines). " +
	"If nothing does, output nothing at all — an empty pulse is a good pulse. Never invent updates."

// runPulse installs, shows, or removes an identity's scheduled check-in — the
// heartbeat that makes the assistant present rather than merely available.
// Sugar over the schedule store: one task with the stable id "pulse-<profile>".
func runPulse(args []string) {
	fs := flag.NewFlagSet("pulse", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "identity that pulses (default from config)")
	providerFlag := fs.String("provider", "", "model provider for pulse runs (default: the runner's)")
	spec := fs.String("spec", "daily 08:30", "when to pulse: 'daily 08:30' | 'every 4h' | any schedule spec")
	deliver := fs.String("deliver", "", "where check-ins go, e.g. telegram:<chatID> (required to enable)")
	off := fs.Bool("off", false, "remove this identity's pulse")
	_ = fs.Parse(args)

	name := *profileFlag
	if name == "" {
		name = activeProfile()
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "pulse needs an identity: set -profile (or a default profile) — the pulse is per-identity")
		os.Exit(2)
	}
	store := schedule.NewStore(profile.ScheduleDir())
	id := "pulse-" + name

	switch {
	case *off:
		ok, err := store.Remove(id)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if !ok {
			fmt.Printf("no pulse installed for %q\n", name)
			return
		}
		fmt.Printf("pulse removed for %q\n", name)

	case *deliver == "":
		// Show the current pulse, if any.
		tasks, err := store.Load()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		for _, t := range tasks {
			if t.ID == id {
				fmt.Printf("pulse for %q: %s → %s (next %s)\n", name, t.Spec, t.Deliver, t.NextRun.Format("Mon 15:04"))
				return
			}
		}
		fmt.Printf("no pulse for %q — enable one:\n  harness pulse -deliver telegram:<chatID> [-spec \"daily 08:30\"]\n", name)

	default:
		t, err := installPulse(store, id, name, *providerFlag, *spec, *deliver)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("pulse on for %q: %s → %s (next %s)\n", name, t.Spec, t.Deliver, t.NextRun.Format("Mon 2006-01-02 15:04"))
		fmt.Fprintln(os.Stderr, "runs via `harness daemon` or `harness schedule run-due` — a quiet pulse sends nothing")
	}
}

// installPulse idempotently (re)installs the pulse task under its stable id.
func installPulse(store *schedule.Store, id, profileName, provider, spec, deliver string) (schedule.Task, error) {
	if _, err := store.Remove(id); err != nil {
		return schedule.Task{}, err
	}
	return store.Add(schedule.Task{
		ID:       id,
		Profile:  profileName,
		Provider: provider,
		Prompt:   pulsePrompt,
		Spec:     spec,
		Deliver:  deliver,
		Enabled:  true,
	}, time.Now())
}
