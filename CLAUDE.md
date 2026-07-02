# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A multi-provider **agent harness** in Go — a personal/company assistant engine built from the standard library only (`go.mod` has no `require` block; no vendor SDK; one static binary). The agent loop is provider-agnostic: Claude, OpenAI, DeepSeek, Gemini, Ollama, Kimi, or any OpenAI-compatible endpoint plug in behind one streaming interface, and the loop above never knows which is in use.

`cmd/harness/` is a thin reference CLI that dogfoods the library — the real product is the packages it imports. Every surface (CLI, chat REPL, scheduler, HTTP server, Telegram bot) builds its agent through `app.Build`, so they all behave identically.

## Commands

```sh
go build ./...                                   # build everything (must stay green)
go test ./...                                    # full test suite (no network needed)
go test ./agent/ -run TestRouting -v             # one package / one test
go vet ./...                                     # vet

go run ./cmd/harness -provider mock "hello"      # exercise the full loop offline, no keys
ANTHROPIC_API_KEY=… go run ./cmd/harness -provider claude "summarize go.mod"
go run ./cmd/harness -provider ollama -model llama3.1 "say hi"   # local, no key
```

Tests are pure unit tests against the `mock` provider and fakes — **no network, no API keys, no build tags**. There is no Makefile or build script; use the `go` toolchain directly. Go 1.26.

## Architecture — the layers, bottom up

The design is a stack of narrow seams. Read these packages in order to understand the whole:

1. **`provider/`** — the one neutral seam. `Provider.Stream(ctx, Request, emit func(Event))` streams a single model turn as neutral `Event`s (`text_delta`, `tool_use_start/delta`, `usage`, `stop`) built from neutral `Message`/`Block` types (`text`, `tool_use`, `tool_result`). Adapters: `mock.go`, `anthropic.go` (API key + OAuth via `WithOAuth`), `openai.go` (OpenAI **and** all OpenAI-compatible vendors). `registry.go` `BuildWith(slug, opts)` is the single switch resolving a provider slug → concrete `Provider`, env vars overriding config. **Nothing above this layer is vendor-aware.**

2. **`tool/`** — `Tool` is `Spec() + Run(ctx, input, *Env)`. Tools are pure values; all I/O goes through the mediated `Env` (`Root` = sandbox root, `Workspace` = the identity's persistent file home; a `workspace:` path prefix addresses it — resolver in `tool/fs.go`, escape attempts are rejected). `Spec.Writes` marks mutating tools; `agent.ConfirmWrites(reg, confirm)` is the shipped gate policy — CLI surfaces wire a TTY prompt via `app.Spec.ConfirmWrite` (`-yes` waives, non-interactive denies), and the gate propagates through `Delegate`/`Dispatch` so workers can't bypass it. `Registry` preserves registration order; re-registering a name replaces it in place. Built-ins: `read_file`, `write_file`, `edit_file`, `list_dir`, `web_fetch`, `web_search` (SearXNG), `bash` (opt-in via `-bash`).

3. **`agent/`** — the provider-agnostic loop. `Agent.Continue(ctx, history, userInput, Handler)` drives stream → accumulate events into blocks → dispatch tool calls → loop until a non-`tool_use` stop (cap `MaxTurns`, default 16). `Handler` (`OnText/OnToolStart/OnToolResult/OnUsage/OnStop`, optional `OnRoute`) is how every frontend consumes the same stream. Also here: `compaction.go` (summarize oldest turns when over `CompactTokens`; cached by summarized-prefix length), `dispatch.go`/`subagent.go` (specialist workers), `Delegate` (a worker exposed as a tool for profile delegation).

4. **`router/`** — `Tier` (`fast`/`balanced`/`reasoning`) is the *role* of a call, decoupled from model names. A config-backed `Table` resolves `(provider, tier) → model`. The loop classifies a task to pick a base tier, and **escalates one tier up when a turn produces nothing usable** (`fumbled`). `DefaultTable()` is the built-in policy; `models.json` overrides it.

5. **`app/app.go`** — **the composition root**. `app.Build(ctx, Spec)` wires provider + connectors/tools + skills + routing + profile (persona/delegation) + specialists + memory into a ready agent. If you add a capability that should reach every surface, wire it here. Surfaces stay thin.

6. **Surfaces** — `cmd/harness/` (CLI subcommand dispatch in `main.go`), `server/` (HTTP+SSE: `POST /v1/chat`, `GET /v1/sessions`, `/healthz`), `channel/telegram/`, `schedule/` (proactive cron-like runs; a task's `Deliver` target like `telegram:<chatID>` pushes its output to a channel — see `deliver` in `cmd/harness/schedule.go`), `task/` (background one-shot queue: `harness task add|list|show|drain`, the `background_task` tool lets the agent queue work for itself — registered per-profile in `app.Build` with the surface's `Spec.TaskDeliver` as the reply target; the daemon's worker in `cmd/harness/task.go` drains it and re-queues jobs interrupted by a restart).

**Self-improvement loops.** Three, composable: (1) *in-task critique* — `agent.Options.Critique`/`-critique` runs a reasoning-tier critic→revise pass before returning (fails open); (2) *reflection* — `harness reflect` + the `reflect` skill + `session.ReviewTool` (`review_sessions`) let an identity read its own past conversations and distill lessons into memory/skills; schedulable nightly with `-deliver`; (3) *feedback-to-lesson* (see the feedback tool + Telegram 👍/👎). Lessons land in memory (tagged) and skills, so the next run is better, `cmd/harness/daemon.go` (API + scheduler + Telegram under one shutdown).

### The pillars (assistant model)

`profile/` identities (persona + base tier + delegation; `WorkspaceDir(name)` is the identity's persistent file home — with no explicit `Spec.Root`, `app.Build` roots `tool.Env` there, so remote surfaces (daemon/telegram/schedule/serve) live in the workspace while `main`/`chat` keep cwd via `-root .`; `Env.Workspace` carries the path either way), `memory/` a per-identity second brain (a bounded `Digest` is injected into the system prompt; `remember` saves — optional tags ride in the body so they're searchable; `recall` keyword-searches the rest — `memory/search.go`; `resurface` picks an aging note via mtime-LRU rotation for proactive scheduled check-ins — `memory/resurface.go`), `skill/` SKILL.md workflows surfaced by progressive disclosure (`load_skill`; an identity can `learn_skill` to write its own), `subagent/` specialists dispatched via `dispatch`, `session/` persisted multi-turn conversations, `connector/` integration layer (native, MCP, Google Calendar/Gmail — Gmail supports read + `gmail_create_draft` + `gmail_send`; drafting needs the `gmail.compose` scope, so accounts connected before it was added must re-run `harness connect google`).

## Conventions that matter here

- **Everything pluggable is a dropped file, per-identity scopable.** Identities = `profiles/<name>.md`; skills = `skills/<name>/SKILL.md`; specialists = `agents/<name>.md`; MCP tools = `mcp.json`; exec plugins = executables in `plugins/` (`connector/plugin`: `spec`/`run <tool>`/`deliver <kind> <dest>` over stdio — tools namespaced `<plugin>__<tool>`, manifest `writes` feeds the permission gate, `delivers` kinds extend `-deliver` targets via the fallback in `cmd/harness/schedule.go`; example in `plugins.example/`). Project-local dirs are scanned first, then the user-config dir.
- **Per-identity data dirs** (`profile/profile.go`): a profile's auth, memory, skills, sessions, and `mcp.json` live under `<user-config>/harness/profiles/<name>/`. File profiles override built-ins of the same name; an identity's own skills win on name conflicts. Memory + `learn_skill` are wired **only when a profile is set** — a generic stateless run keeps no memory.
- **Capabilities, not branches.** Providers opt into features via `Request.CapFlags` (`caching`, `tools`, `structured`); unknown ⇒ conservative default. Don't add per-vendor `if` branches above the provider layer.
- **MCP/external connector tools are namespaced** (`filesystem__read_file`) so they can't shadow built-ins; native built-ins keep plain names.
- **Secrets are gitignored** (`auth.json`, `mcp.json`, `harness.json`, `models.json`, `config.json`, `.env*`). The committed `harness` binary and `auth.json` in the repo root are local artifacts — do not commit credentials.
- Package docs live in a doc comment on each package's primary file — read it first; they explain *why* the seam exists, not just what it does.

## Configuration & env vars

Config lives in `config.json` (`config/`): `search` (SearXNG), Google OAuth client, `default_profile`, `skills.registry` (git URL for the skills registry), and `providers.<slug>.{api_key,base_url,model}` (so stored keys work without env). Env always overrides config. Notable env vars: `HARNESS_PROFILE`, `HARNESS_PROFILES_DIR`, `HARNESS_AUTH_FILE`, `HARNESS_MODELS_FILE`, `HARNESS_MCP_FILE`, `HARNESS_SKILLS_DIR`, `HARNESS_SKILLS_REGISTRY`, `HARNESS_API_TOKEN`, plus per-provider `*_API_KEY` / `*_BASE_URL`.

**Skills registry** (`skill/registry.go`): `Source` is the install-catalog seam; `GitSource` clones a skills repo into `<user-config>/harness/registry/<hash>` and serves skills from the checked-out tree (git-independent scan/copy lives in `tree`, so it unit-tests without git). Install = copy a skill folder into a skills dir → the normal `Load` machinery discovers it. Wired only in the `harness skills search`/`add` CLI (install-time); the agent loop is unchanged.
