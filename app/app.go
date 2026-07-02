// Package app composes the full assistant: it wires a model provider, connector
// tools, agent skills, automatic routing, the identity profile (persona +
// delegation), and per-identity memory into a ready-to-run agent. Every surface
// — the CLI, chat, the scheduler, the HTTP server, the Telegram bot — builds its
// agent through app.Build, so they all behave identically. The cmd binary stays
// thin: it parses flags and calls in here.
package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/flakerimi/harness/agent"
	"github.com/flakerimi/harness/auth"
	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/connector"
	"github.com/flakerimi/harness/connector/google"
	"github.com/flakerimi/harness/connector/mcp"
	"github.com/flakerimi/harness/connector/plugin"
	"github.com/flakerimi/harness/memory"
	"github.com/flakerimi/harness/profile"
	"github.com/flakerimi/harness/provider"
	"github.com/flakerimi/harness/router"
	"github.com/flakerimi/harness/session"
	"github.com/flakerimi/harness/skill"
	"github.com/flakerimi/harness/subagent"
	"github.com/flakerimi/harness/task"
	"github.com/flakerimi/harness/tool"
)

// Spec is the resolved configuration for building an agent.
type Spec struct {
	Provider  string
	Model     string
	System    string
	MaxTokens int
	Root      string // filesystem root for tools; "" = auto (the profile's workspace, else ".")
	Profile   string
	Tier      string
	Route     bool
	Classify  bool
	Escalate  bool
	Bash      bool
	Compact   int  // summarizing-compaction token budget; 0 disables
	Critique  bool // run a critic→revise pass before returning a final answer

	// ConfirmWrite, when set, gates mutating tools (write_file, edit_file,
	// bash, …) behind a confirmation callback — the CLI wires a terminal
	// prompt here. Nil allows writes (remote surfaces are sandboxed in the
	// identity's workspace instead).
	ConfirmWrite func(toolName, detail string) bool

	// TaskDeliver is where a background task queued from this surface sends
	// its result (e.g. "telegram:<chatID>" — the chat that asked). Empty keeps
	// results in the store for `harness task show`.
	TaskDeliver string
}

// memDigestCap bounds how many memories are injected into the system prompt;
// the rest stay reachable through the recall tool, so a growing memory store
// doesn't bloat every turn.
const memDigestCap = 24

// Build assembles a ready-to-run agent from a Spec.
func Build(ctx context.Context, spec Spec) (*agent.Agent, error) {
	// Resolve provider credentials/endpoint from config (env still overrides),
	// so stored keys work without exporting env vars.
	cfg, _ := config.Load()
	pc := cfg.ProviderConf(spec.Provider)
	prov, err := provider.BuildWith(spec.Provider, provider.BuildOptions{APIKey: pc.APIKey, BaseURL: pc.BaseURL})
	if err != nil {
		return nil, err
	}

	reg, err := Connectors(spec.Bash, spec.Profile).Tools(ctx)
	if err != nil {
		return nil, err
	}

	// Permission: gate mutating tools behind the surface's confirmation
	// callback (the CLI's terminal prompt). Built over the connector registry
	// and handed to the orchestrator AND every worker path (delegate,
	// dispatch), so delegation can't bypass the write policy.
	var gate agent.PermissionGate
	if spec.ConfirmWrite != nil {
		gate = agent.ConfirmWrites(reg, spec.ConfirmWrite)
	}

	// Model precedence: explicit model, else a config-pinned model for this
	// provider (needed for OpenAI-compatible providers with no built-in default).
	model := spec.Model
	if model == "" {
		model = pc.Model
	}

	// The identity's workspace — its persistent home for files. An explicit
	// Root wins (a CLI run working in cwd); with none, a profile's surface is
	// rooted in its workspace, so files persist across sessions and surfaces.
	workspace := ""
	if spec.Profile != "" {
		workspace = profile.WorkspaceDir(spec.Profile)
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "warning: workspace:", err)
			workspace = ""
		}
	}
	root := spec.Root
	if root == "" {
		if workspace != "" {
			root = workspace
		} else {
			root = "."
		}
	}

	caps := []string{provider.CapTools, provider.CapCaching}
	opts := agent.Options{
		Model:         model,
		System:        spec.System,
		MaxTokens:     spec.MaxTokens,
		Caps:          caps,
		Env:           &tool.Env{Root: root, Workspace: workspace},
		CompactTokens: spec.Compact,
		Critique:      spec.Critique,
		Permission:    gate,
	}
	toolReg := reg // tools the orchestrator uses

	// Agent Skills: register the load_skill tool into reg so both the
	// orchestrator and any delegated workers get it, and build the discovery
	// text that advertises skills in the system prompt. For an identity profile,
	// its own learned-skills dir is scanned first (and wins on name conflicts).
	var skillDirs []string
	if spec.Profile != "" {
		skillDirs = append(skillDirs, profile.SkillsDir(spec.Profile))
	}
	skills, skErrs := skill.Load(skillDirs...)
	for _, e := range skErrs {
		fmt.Fprintln(os.Stderr, "warning: skill:", e)
	}
	skillDiscovery := ""
	if len(skills) > 0 {
		reg.Register(skill.NewLoadTool(skills))
		skillDiscovery = skill.DiscoveryText(skills)
	}

	// Routing is on when requested, or implied by a profile.
	var rt *router.Table
	if (spec.Model == "" && spec.Route) || spec.Profile != "" {
		t, rerr := router.LoadTable(ModelsConfigPath())
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "warning: models config:", rerr)
			t = router.DefaultTable()
		}
		rt = t
		opts.Router = rt
		opts.BaseTier = router.ParseTier(spec.Tier, router.TierReasoning)
		opts.Classify = spec.Classify
		opts.Escalate = spec.Escalate
	}

	if spec.Profile != "" {
		prof, ok := profile.Get(spec.Profile)
		if !ok {
			return nil, fmt.Errorf("unknown profile %q (available: %s)", spec.Profile, strings.Join(profile.Names(), ", "))
		}
		opts.System = prof.Persona
		opts.BaseTier = prof.BaseTier
		opts.Classify = false // the profile sets the orchestrator's tier
		if prof.Delegate {
			workerSystem := prof.WorkerPersona
			if skillDiscovery != "" {
				workerSystem += "\n\n" + skillDiscovery
			}
			orch := tool.NewRegistry()
			for _, t := range reg.All() { // reg already includes load_skill
				orch.Register(t)
			}
			orch.Register(agent.Delegate{
				Provider:   prov,
				Tools:      reg, // worker gets the connector tools + load_skill (no delegate → no recursion)
				Router:     rt,
				Tier:       prof.WorkerTier,
				System:     workerSystem,
				MaxTokens:  spec.MaxTokens,
				Caps:       caps,
				Permission: gate,
			})
			toolReg = orch
		}
		fmt.Fprintf(os.Stderr, "› provider=%s profile=%s (base=%s, delegate=%v)\n", prov.Name(), prof.Name, prof.BaseTier, prof.Delegate)
	} else if opts.Router != nil {
		fmt.Fprintf(os.Stderr, "› provider=%s routing=on (classify=%v escalate=%v)\n", prov.Name(), spec.Classify, spec.Escalate)
	} else {
		shown := spec.Model
		if shown == "" {
			shown = provider.DefaultModel(spec.Provider)
		}
		fmt.Fprintf(os.Stderr, "› provider=%s model=%s\n", prov.Name(), shown)
	}

	// Append skill discovery to the orchestrator's system prompt (after a
	// profile may have set it). The load_skill tool is already in toolReg.
	if skillDiscovery != "" {
		if opts.System != "" {
			opts.System += "\n\n"
		}
		opts.System += skillDiscovery
	}

	// Pluggable specialists: load agents/<name>.md (shared + this identity's own)
	// and give the orchestrator a dispatch tool plus a roster in its prompt. Each
	// specialist runs at its own tier over its own tool subset (empty = all).
	var agentDirs []string
	if spec.Profile != "" {
		agentDirs = append(agentDirs, profile.AgentsDir(spec.Profile))
	}
	specs, aerrs := subagent.Load(agentDirs...)
	for _, e := range aerrs {
		fmt.Fprintln(os.Stderr, "warning: subagent:", e)
	}
	if len(specs) > 0 {
		workers := make(map[string]agent.WorkerConfig, len(specs))
		for _, s := range specs {
			wtools := reg // reg has the connector tools + load_skill, no dispatch (no recursion)
			if len(s.Tools) > 0 {
				sub := tool.NewRegistry()
				for _, n := range s.Tools {
					if t, ok := reg.Get(n); ok {
						sub.Register(t)
					}
				}
				wtools = sub
			}
			sys := s.Persona
			if skillDiscovery != "" {
				sys += "\n\n" + skillDiscovery
			}
			workers[s.Name] = agent.WorkerConfig{
				Description: s.Description,
				Tier:        router.ParseTier(s.Tier, router.TierFast),
				System:      sys,
				Tools:       wtools,
			}
		}
		disp := agent.NewDispatch(prov, rt, spec.MaxTokens, caps, workers)
		disp.Permission = gate
		toolReg.Register(disp)
		if d := subagent.DiscoveryText(specs); d != "" {
			if opts.System != "" {
				opts.System += "\n\n"
			}
			opts.System += d
		}
	}

	// Tell the identity where home is: files written under the workspace
	// persist across sessions and surfaces.
	if workspace != "" {
		note := "Workspace: you have a persistent workspace directory for files (drafts, notes, projects); it survives across conversations."
		if root == workspace {
			note += " Filesystem tools operate inside it."
		} else {
			note += fmt.Sprintf(" Filesystem tools operate in the current working root; your workspace is at %s.", workspace)
		}
		if opts.System != "" {
			opts.System += "\n\n"
		}
		opts.System += note
	}

	// Memory: inject the identity's durable facts + the remember tool. Only for
	// an identity profile — a generic stateless run keeps no memory.
	if spec.Profile != "" {
		memStore := memory.NewStore(profile.MemoryDir(spec.Profile))
		mems, merr := memStore.Load()
		if merr != nil {
			fmt.Fprintln(os.Stderr, "warning: memory:", merr)
		}
		if mc := memory.Digest(mems, memDigestCap); mc != "" {
			if opts.System != "" {
				opts.System += "\n\n"
			}
			opts.System += mc
		}
		toolReg.Register(memory.NewRememberTool(memStore))
		// recall lets the agent search the rest of memory on demand, so the
		// injected digest can stay small as the store grows; resurface picks an
		// aging note to revisit, for proactive scheduled check-ins.
		toolReg.Register(memory.NewRecallTool(memStore))
		toolReg.Register(memory.NewResurfaceTool(memStore, 0))
		// Feedback-to-lesson: a correction the user gives now becomes a durable
		// lesson (a tagged memory) that shapes future behavior.
		toolReg.Register(memory.NewFeedbackTool(memStore))

		// Self-improvement: let the identity write new skills it works out into
		// its own skills dir, so the procedure is reusable by name next time.
		toolReg.Register(skill.NewLearnTool(profile.SkillsDir(spec.Profile)))

		// Reflection: let the identity read its own past conversations, so it can
		// review and learn from them (the reflect skill drives this).
		toolReg.Register(session.NewReviewTool(session.NewStore(profile.SessionsDir(spec.Profile))))

		// Background work: let the identity queue long jobs for the daemon's
		// task worker instead of blocking the conversation (the result is
		// delivered to this surface's deliver target when done), and see its
		// own queue so it can report progress and notice failures.
		taskStore := task.NewStore(profile.TasksDir())
		toolReg.Register(task.NewEnqueueTool(taskStore, spec.Profile, spec.Provider, spec.TaskDeliver))
		toolReg.Register(task.NewStatusTool(taskStore, spec.Profile))
	}

	return agent.New(prov, toolReg, opts), nil
}

// Connectors wires the integrations available to the harness: native built-ins
// (always present), a Google connector when an OAuth client is configured
// (scoped to the profile's own auth file), and any MCP servers from mcp.json.
func Connectors(allowShell bool, profileName string) *connector.Registry {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: config:", err)
	}
	tools := []tool.Tool{
		tool.ReadFile{},
		tool.WriteFile{},
		tool.EditFile{},
		tool.ListDir{},
		tool.WebFetch{},
		tool.WebSearch{SearxngURL: cfg.Search.SearxngURL, SearxngToken: cfg.Search.SearxngToken},
	}
	if allowShell {
		tools = append(tools, tool.Bash{})
	}
	r := connector.NewRegistry()
	r.Add(connector.NewNative("builtin", tools...))

	if id, secret := cfg.GoogleClient(); id != "" && secret != "" {
		r.Add(google.New(auth.NewStore(profile.AuthFile(profileName)), id, secret))
	}

	// Exec plugins: dropped executables (project-local, this identity's own,
	// then shared) join as connectors — their tools arrive namespaced like any
	// external integration, and a manifest's writes flag feeds the gate.
	plugs, perrs := plugin.Discover(context.Background(), PluginDirs(profileName)...)
	for _, e := range perrs {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}
	for _, p := range plugs {
		r.Add(plugin.New(p))
	}

	// Shared MCP servers (mcp.json), then this identity's own servers
	// (profiles/<name>/mcp.json) layered on top — so a business profile can have
	// tools a personal one doesn't.
	cfgs, err := mcp.LoadConfig(MCPConfigPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: mcp config:", err)
	}
	if profileName != "" {
		pcfgs, perr := mcp.LoadConfig(profile.MCPFile(profileName))
		if perr != nil {
			fmt.Fprintln(os.Stderr, "warning: profile mcp config:", perr)
		}
		cfgs = append(cfgs, pcfgs...)
	}
	for _, c := range cfgs {
		r.Add(mcp.New(c))
	}
	return r
}

// PluginDirs are the exec-plugin locations in priority order: project-local
// ./plugins, the identity's own plugins, then the shared user-config dir.
// First plugin claiming a name wins.
func PluginDirs(profileName string) []string {
	dirs := []string{"plugins"}
	if profileName != "" {
		dirs = append(dirs, profile.PluginsDir(profileName))
	}
	return append(dirs, profile.PluginsDir(""))
}

// ModelsConfigPath is the models routing table file ($HARNESS_MODELS_FILE or models.json).
func ModelsConfigPath() string {
	if v := os.Getenv("HARNESS_MODELS_FILE"); v != "" {
		return v
	}
	return "models.json"
}

// MCPConfigPath is the MCP servers file ($HARNESS_MCP_FILE or mcp.json).
func MCPConfigPath() string {
	if v := os.Getenv("HARNESS_MCP_FILE"); v != "" {
		return v
	}
	return "mcp.json"
}
