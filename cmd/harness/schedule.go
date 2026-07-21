package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/flakerimi/harness/app"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/schedule"
)

// runSchedule is the proactive surface: register, inspect, and fire scheduled
// tasks (each an identity + prompt + schedule). `run-due` is meant to be invoked
// by a system cron/launchd; `daemon` keeps a process alive checking on an
// interval; `run` fires one task immediately.
func runSchedule(args []string) {
	store := schedule.NewStore(profile.ScheduleDir())
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "add":
		scheduleAdd(store, args)
	case "list", "ls", "":
		scheduleList(store)
	case "remove", "rm":
		scheduleRemove(store, args)
	case "run":
		scheduleRun(store, args)
	case "run-due":
		scheduleRunDue(store)
	case "daemon":
		scheduleDaemon(store, args)
	default:
		fmt.Fprintf(os.Stderr, "unknown schedule subcommand %q\nusage: harness schedule <add|list|remove|run|run-due|daemon>\n", sub)
		os.Exit(2)
	}
}

func scheduleAdd(store *schedule.Store, args []string) {
	fs := flag.NewFlagSet("schedule add", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "identity profile to run as (default from config)")
	spec := fs.String("spec", "", "schedule: 'every 30m' | 'daily 08:00' | 'weekly mon 09:00' | 'once 09:00' | 'in 2h' (one-shot)")
	id := fs.String("id", "", "optional task id (auto-assigned if empty)")
	providerSlug := fs.String("provider", "mock", "model provider to run this task with")
	deliver := fs.String("deliver", "", "send output to a channel, e.g. telegram:<chatID> (default: stdout only)")
	_ = fs.Parse(args)

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if *spec == "" || prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: harness schedule add -spec <spec> [-profile p] [-provider claude] [-deliver telegram:<id>] [-id name] <prompt>")
		os.Exit(2)
	}
	prof := *profileFlag
	if prof == "" {
		prof = activeProfile()
	}
	t, err := store.Add(schedule.Task{
		ID:       *id,
		Profile:  prof,
		Provider: *providerSlug,
		Prompt:   prompt,
		Spec:     *spec,
		Deliver:  *deliver,
	}, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("added %s · profile=%s · %s · next %s\n", t.ID, t.Profile, t.Spec, t.NextRun.Format("Mon 2006-01-02 15:04"))
}

func scheduleList(store *schedule.Store) {
	tasks, err := store.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("scheduled tasks  [%s]\n", store.Dir())
	for _, t := range tasks {
		state := "on"
		if !t.Enabled {
			state = "off"
		}
		last := "never"
		if !t.LastRun.IsZero() {
			last = t.LastRun.Format("01-02 15:04")
		}
		fmt.Printf("- %-10s [%s] profile=%s spec=%q next=%s last=%s\n      %s\n",
			t.ID, state, t.Profile, t.Spec, t.NextRun.Format("01-02 15:04"), last, clip(t.Prompt, 80))
	}
	if len(tasks) == 0 {
		fmt.Println("(no tasks — add one with `harness schedule add`)")
	}
}

func scheduleRemove(store *schedule.Store, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: harness schedule remove <id>")
		os.Exit(2)
	}
	ok, err := store.Remove(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if !ok {
		fmt.Printf("no task %q\n", args[0])
		return
	}
	fmt.Printf("removed %s\n", args[0])
}

func scheduleRun(store *schedule.Store, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: harness schedule run <id>")
		os.Exit(2)
	}
	tasks, err := store.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var task *schedule.Task
	for i := range tasks {
		if tasks[i].ID == args[0] {
			task = &tasks[i]
		}
	}
	if task == nil {
		fmt.Fprintf(os.Stderr, "no task %q\n", args[0])
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	runScheduledTask(ctx, *task)
	if err := store.MarkRan(task.ID, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
}

func scheduleRunDue(store *schedule.Store) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if n := runDueOnce(ctx, store, time.Now()); n == 0 {
		fmt.Fprintln(os.Stderr, "(nothing due)")
	}
}

func scheduleDaemon(_ *schedule.Store, args []string) {
	fs := flag.NewFlagSet("schedule daemon", flag.ExitOnError)
	interval := fs.Duration("interval", time.Minute, "how often to check for due tasks")
	_ = fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	runScheduler(ctx, *interval)
	fmt.Fprintln(os.Stderr, "\nstopped")
}

// runScheduler fires due tasks every interval until ctx is cancelled. It is the
// reusable core shared by `schedule daemon` and the daemon supervisor.
func runScheduler(ctx context.Context, interval time.Duration) {
	store := schedule.NewStore(profile.ScheduleDir())
	fmt.Fprintf(os.Stderr, "scheduler: checking every %s\n", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		runDueOnce(ctx, store, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runDueOnce fires every task due at now and records each run. It returns how
// many tasks ran.
func runDueOnce(ctx context.Context, store *schedule.Store, now time.Time) int {
	due, err := store.Due(now)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 0
	}
	for _, t := range due {
		if ctx.Err() != nil {
			break
		}
		runScheduledTask(ctx, t)
		if err := store.MarkRan(t.ID, time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "warning: mark ran:", err)
		}
	}
	return len(due)
}

// runScheduledTask builds the task's identity and runs its prompt once,
// streaming the result to stdout (so a system cron captures it). When the task
// has a Deliver target, the assistant's text is also sent to that channel — the
// wire that turns a scheduled run into a proactive message on your phone.
func runScheduledTask(ctx context.Context, t schedule.Task) {
	provSlug := t.Provider
	if provSlug == "" {
		provSlug = "mock"
	}
	fmt.Printf("\n──[ %s · profile=%s · %s ]──\n", t.ID, t.Profile, time.Now().Format("2006-01-02 15:04"))
	ag, err := app.Build(ctx, app.Spec{
		Provider:  provSlug,
		System:    "You are a helpful assistant.",
		MaxTokens: 8192,
		Root:      "", // auto: the profile's workspace
		Profile:   t.Profile,
		Tier:      "reasoning",
		Route:     true,
		Classify:  false,
		Escalate:  true,
		MaxTurns:  40, // scheduled runs (pulse, reflect) reach for several tools
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return
	}
	h := &captureHandler{}
	err = ag.Run(ctx, t.Prompt, h)
	// Provider failover: a scheduled run (the morning brief, the inbox watch)
	// shouldn't skip a beat because one vendor blinked — retry once on the
	// first fallback, whose model routing re-resolves.
	if fb := fallbackProviders(); err != nil && isTransientErr(err) && len(fb) > 0 && fb[0] != provSlug {
		fmt.Fprintf(os.Stderr, "\nschedule: %v — failing over to %s\n", err, fb[0])
		if ag2, berr := app.Build(ctx, app.Spec{
			Provider: fb[0], System: "You are a helpful assistant.", MaxTokens: 8192,
			Root: "", Profile: t.Profile, Tier: "reasoning", Route: true,
			Classify: false, Escalate: true, MaxTurns: 40,
		}); berr == nil {
			h = &captureHandler{}
			err = ag2.Run(ctx, t.Prompt, h)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
	}
	fmt.Println()
	if t.Deliver != "" {
		text := strings.TrimSpace(h.text.String())
		if isSilence(text) {
			// The run decided nothing deserves the user. Models are bad at
			// emitting literally nothing, so a sentinel ("NOTHING") counts as
			// silence too — no delivery, no 10-minutely "all quiet" spam.
			fmt.Fprintf(os.Stderr, "deliver: silent run (%s), nothing sent\n", t.ID)
			return
		}
		if err := deliver(ctx, t.Deliver, text); err != nil {
			fmt.Fprintln(os.Stderr, "warning: deliver:", err)
		} else {
			// Proof of delivery in the log — a sent-but-vanished message
			// should never be a mystery.
			fmt.Fprintf(os.Stderr, "deliver: sent %d chars to %s\n", len(text), t.Deliver)
		}
	}
}

// isSilence reports whether a scheduled run's output means "say nothing":
// empty, or a bare silence sentinel (optionally wrapped in markdown/punctuation).
func isSilence(text string) bool {
	norm := strings.ToLower(strings.Trim(strings.TrimSpace(text), "*_`.!()[]\" \n"))
	switch norm {
	case "", "nothing", "silence", "nothing to report", "nothing that needs you":
		return true
	}
	return false
}

// captureHandler renders the run to the terminal like cliHandler but also
// accumulates the assistant's text, so a scheduled task can forward it to a
// delivery channel.
type captureHandler struct {
	cliHandler
	text strings.Builder
}

func (h *captureHandler) OnText(delta string) {
	h.cliHandler.OnText(delta)
	h.text.WriteString(delta)
}

// deliverers is the shared channel registry — telegram, webhook, push, app,
// with exec plugins as the fallback. One instance serves every cmd surface
// (scheduler, task worker, reflect).
var deliverers = app.Deliverers()

// deliver sends a scheduled/background task's output to one or more channel
// targets — "kind:dest" forms like "telegram:<chatID>", "push:<profile>",
// "webhook:<url>", or any kind a plugin advertises — separated by "|".
func deliver(ctx context.Context, target, text string) error {
	return deliverers.Deliver(ctx, target, text)
}
