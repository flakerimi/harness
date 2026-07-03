package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/app"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/task"
)

// runTask dispatches the background-task subcommands: add/list/show enqueue and
// inspect jobs; drain executes the queue in-process (for setups not running the
// daemon, whose worker normally does this).
func runTask(args []string) {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	store := task.NewStore(profile.TasksDir())
	switch sub {
	case "add":
		fs := flag.NewFlagSet("task add", flag.ExitOnError)
		profileFlag := fs.String("profile", "", "identity the job runs as (default from config)")
		providerFlag := fs.String("provider", "", "model provider slug (default: the runner's)")
		deliver := fs.String("deliver", "", "where the result goes, e.g. telegram:<chatID> (default: stored only)")
		_ = fs.Parse(args)
		prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if prompt == "" {
			fmt.Fprintln(os.Stderr, "usage: harness task add [-profile p] [-deliver telegram:id] <prompt>")
			os.Exit(2)
		}
		name := *profileFlag
		if name == "" {
			name = activeProfile()
		}
		t, err := store.Enqueue(task.Task{Profile: name, Provider: *providerFlag, Prompt: prompt, Deliver: *deliver})
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("queued %s (profile %q) — run `harness daemon` or `harness task drain` to execute\n", t.ID, name)

	case "list":
		all, err := store.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if len(all) == 0 {
			fmt.Println("(no background tasks — queue one with `harness task add \"...\"`)")
			return
		}
		for _, t := range all {
			fmt.Printf("%-8s %-28s %-10s %s\n", t.Status, t.ID, t.Profile, clip(t.Prompt, 60))
		}

	case "show":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "usage: harness task show <id>")
			os.Exit(2)
		}
		t, err := store.Get(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("id:       %s\nstatus:   %s\nprofile:  %s\ncreated:  %s\n", t.ID, t.Status, t.Profile, t.Created.Format(time.RFC3339))
		if t.Deliver != "" {
			fmt.Printf("deliver:  %s\n", t.Deliver)
		}
		fmt.Printf("prompt:   %s\n", t.Prompt)
		if t.Error != "" {
			fmt.Printf("error:    %s\n", t.Error)
		}
		if t.Result != "" {
			fmt.Printf("\n%s\n", t.Result)
		}

	case "drain":
		fs := flag.NewFlagSet("task drain", flag.ExitOnError)
		providerFlag := fs.String("provider", "claude", "default model provider for jobs that didn't pin one")
		compact := fs.Int("compact", 120000, "compaction token budget (0 disables)")
		_ = fs.Parse(args)
		ctx := context.Background()
		n := 0
		for {
			t, err := store.NextQueued()
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			if t == nil {
				break
			}
			executeTask(ctx, store, t, *providerFlag, *compact)
			n++
		}
		fmt.Fprintf(os.Stderr, "drained %d task(s)\n", n)

	default:
		fmt.Fprintf(os.Stderr, "usage: harness task add|list|show|drain\n")
		os.Exit(2)
	}
}

// executeTask runs one claimed job to completion: build the identity's agent,
// run the prompt, persist the outcome, deliver the result. Errors are recorded
// on the task (and delivered, so the asker isn't left waiting on silence).
func executeTask(ctx context.Context, store *task.Store, t *task.Task, defaultProvider string, compact int) {
	prov := t.Provider
	if prov == "" {
		prov = defaultProvider
	}
	fmt.Fprintf(os.Stderr, "task: running %s (profile %q): %s\n", t.ID, t.Profile, clip(t.Prompt, 80))

	var result string
	ag, err := app.Build(ctx, app.Spec{
		Provider:  prov,
		System:    "You are executing a queued background task. Work it to completion; your final message is the deliverable.",
		MaxTokens: 8192,
		Profile:   t.Profile,
		Tier:      "reasoning",
		Route:     true,
		Escalate:  true,
		Compact:   compact,
		MaxTurns:  48, // background work is exactly the place for a deep tool budget
	})
	if err == nil {
		c := &agent.Collector{}
		err = ag.Run(ctx, t.Prompt, c)
		result = strings.TrimSpace(c.Text())
	}
	if serr := store.Complete(t, result, err); serr != nil {
		fmt.Fprintln(os.Stderr, "task: save:", serr)
	}

	// Deliver the outcome — failures too, so the asker hears back either way.
	if t.Deliver != "" {
		text := result
		if err != nil {
			text = "background task failed: " + err.Error()
		}
		if derr := deliver(ctx, t.Deliver, text); derr != nil {
			fmt.Fprintln(os.Stderr, "task: deliver:", derr)
		} else if strings.TrimSpace(text) != "" {
			fmt.Fprintf(os.Stderr, "task: delivered %d chars to %s\n", len(text), t.Deliver)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "task: %s failed: %v\n", t.ID, err)
	} else {
		fmt.Fprintf(os.Stderr, "task: %s done\n", t.ID)
	}
}

// runTaskWorker is the daemon's queue drainer: recover interrupted jobs, then
// poll for queued ones and execute them sequentially until ctx ends.
func runTaskWorker(ctx context.Context, interval time.Duration, defaultProvider string, compact int) {
	store := task.NewStore(profile.TasksDir())
	if n, err := store.RecoverRunning(); err != nil {
		fmt.Fprintln(os.Stderr, "task: recover:", err)
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "task: re-queued %d interrupted job(s)\n", n)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		// Drain everything currently queued, then wait for the next tick.
		for {
			t, err := store.NextQueued()
			if err != nil {
				fmt.Fprintln(os.Stderr, "task: queue:", err)
				break
			}
			if t == nil {
				break
			}
			executeTask(ctx, store, t, defaultProvider, compact)
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
