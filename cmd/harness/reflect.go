package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/flakerimi/harness/app"
)

// runReflect fires the self-improvement reflection loop: the identity reviews
// its recent conversations (via the review_sessions tool + the reflect skill)
// and distills durable lessons into memory and skills. It's a thin entry point
// — the same run happens when a scheduled task's prompt says "reflect", since
// the tool and skill are available in every profile run.
//
//	harness reflect -profile work -provider claude
//	harness reflect -profile work -deliver telegram:<chatID>   # nightly digest
func runReflect(args []string) {
	fs := flag.NewFlagSet("reflect", flag.ExitOnError)
	profileFlag := fs.String("profile", "", "identity to reflect as (default from config)")
	providerSlug := fs.String("provider", "claude", "model provider")
	deliverTo := fs.String("deliver", "", "send the summary to a channel, e.g. telegram:<chatID>")
	limit := fs.Int("limit", 5, "how many recent sessions to review")
	_ = fs.Parse(args)

	prof := *profileFlag
	if prof == "" {
		prof = activeProfile()
	}
	if prof == "" {
		fmt.Fprintln(os.Stderr, "reflect needs an identity (its memory/skills are where lessons are saved): pass -profile, or set a default")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ag, err := app.Build(ctx, app.Spec{
		Provider:  *providerSlug,
		MaxTokens: 4096,
		Root:      ".",
		Profile:   prof,
		Tier:      "reasoning",
		Route:     true,
		Escalate:  true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	prompt := fmt.Sprintf("Load and follow the reflect skill: review your %d most recent conversations and turn what you find into durable memory and skills, then give a short 'What I learned' summary.", *limit)

	h := &captureHandler{}
	if err := ag.Run(ctx, prompt, h); err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		os.Exit(1)
	}
	fmt.Println()
	if *deliverTo != "" {
		if err := deliver(ctx, *deliverTo, strings.TrimSpace(h.text.String())); err != nil {
			fmt.Fprintln(os.Stderr, "warning: deliver:", err)
		}
	}
}
