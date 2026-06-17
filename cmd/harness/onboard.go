package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/app"
	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/onboard"
	"github.com/flakerimi/harness/profile"
)

// runOnboard interviews the user about their business and writes a tailored
// identity profile (the model drafts the persona; a template is the fallback),
// then offers to make it the default.
func runOnboard(args []string) {
	fs := flag.NewFlagSet("onboard", flag.ExitOnError)
	providerSlug := fs.String("provider", "", "model provider to draft with (default: config default, else claude)")
	noAI := fs.Bool("no-ai", false, "skip the model and use the built-in template persona")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	in := bufio.NewScanner(os.Stdin)
	ask := func(q, def string) string {
		if def != "" {
			fmt.Printf("%s [%s]: ", q, def)
		} else {
			fmt.Printf("%s: ", q)
		}
		if !in.Scan() {
			return def
		}
		v := strings.TrimSpace(in.Text())
		if v == "" {
			return def
		}
		return v
	}

	fmt.Println("Let's set up an assistant for your business.")
	a := onboard.Answers{
		Business:  ask("Business / org name", ""),
		Does:      ask("What does it do (one line)", ""),
		Role:      ask("Your role", ""),
		Tone:      ask("Preferred tone", "concise and professional"),
		Assistant: ask("Assistant's name", "Morpheus"),
	}
	if a.Business == "" || a.Does == "" {
		fmt.Fprintln(os.Stderr, "a business name and what it does are required")
		os.Exit(2)
	}
	slug := onboard.Slug(a.Business)

	// Draft the persona with the model, unless disabled or it fails.
	persona := onboard.TemplatePersona(a)
	if !*noAI {
		prov := *providerSlug
		if prov == "" {
			prov = "claude"
		}
		fmt.Fprintf(os.Stderr, "\n› drafting persona with %s…\n", prov)
		if drafted, err := draftPersona(ctx, prov, a); err != nil {
			fmt.Fprintf(os.Stderr, "  (couldn't draft with the model: %v — using the template instead)\n", err)
		} else if strings.TrimSpace(drafted) != "" {
			persona = drafted
		}
	}

	// Write profiles/<slug>.md to the writable profiles dir.
	dir := profile.UserProfilesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	path := filepath.Join(dir, slug+".md")
	if _, err := os.Stat(path); err == nil {
		if ask(fmt.Sprintf("profile %q already exists — overwrite? (y/N)", slug), "N") != "y" {
			fmt.Fprintln(os.Stderr, "aborted")
			os.Exit(1)
		}
	}
	if err := os.WriteFile(path, []byte(onboard.ProfileFile(a, persona)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("\n✓ wrote %s\n", path)

	if ask("Make this the default identity? (Y/n)", "Y") != "n" {
		if err := config.SetDefaultProfile(slug); err != nil {
			fmt.Fprintln(os.Stderr, "warning: couldn't set default:", err)
		} else {
			fmt.Printf("✓ default identity is now %q\n", slug)
		}
	}

	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  harness chat -profile %s              # talk to it\n", slug)
	fmt.Printf("  harness connect google -profile %s    # connect its email/calendar\n", slug)
	fmt.Printf("  add skills/<name>/SKILL.md and agents/<name>.md to give it domain know-how\n")
}

// draftPersona runs a one-shot model call to write the persona.
func draftPersona(ctx context.Context, providerSlug string, a onboard.Answers) (string, error) {
	ag, err := app.Build(ctx, app.Spec{
		Provider:  providerSlug,
		System:    onboard.WriterSystem,
		MaxTokens: 1500,
		Root:      ".",
		Tier:      "reasoning",
		Route:     true,
	})
	if err != nil {
		return "", err
	}
	var c agent.Collector
	if err := ag.Run(ctx, onboard.DraftPrompt(a), &c); err != nil {
		return "", err
	}
	return c.Text(), nil
}
