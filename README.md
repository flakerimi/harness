# harness

[![ci](https://github.com/flakerimi/harness/actions/workflows/ci.yml/badge.svg)](https://github.com/flakerimi/harness/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/flakerimi/harness.svg)](https://pkg.go.dev/github.com/flakerimi/harness)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A multi-provider **agent harness** in Go — a personal/company assistant engine
built from the standard library, no vendor SDK, one static binary. The agent
loop is provider-agnostic: Claude, OpenAI, DeepSeek, Gemini, Ollama, or any
OpenAI-compatible endpoint plug in behind one streaming interface, and the loop
above never knows which is in use.

The `Provider`/`Agent` shapes borrow from the Construct `brain`, rebuilt here as
a clean standalone library. The assistant model is shaped after personal-agent
frameworks like Hermes — an identity that **remembers**, has **skills**, runs on
a **schedule**, and improves itself.

## The pillars

| Pillar | What it is | Where |
|---|---|---|
| **Soul** | identities — persona + routing tier + delegation | `profile/`, `harness profiles` |
| **Memory** | per-identity second brain: a bounded digest is injected, `remember` saves (with tags), `recall` searches, `resurface` revisits aging notes | `memory/`, `harness memory` |
| **Skills** | SKILL.md workflows (agentskills.io format), progressive disclosure | `skill/`, `harness skills` |
| **Self-improvement** | writes its own skills (`learn_skill`); self-critiques answers (`-critique`); reflects on past chats into memory + skills (`harness reflect`) | `skill/`, `agent/`, `session/` |
| **Sessions** | multi-turn conversations that persist across runs | `session/`, `harness chat` |
| **Crons** | scheduled, proactive runs on a clock | `schedule/`, `harness schedule` |

## Why it's structured this way

- **One neutral seam.** `provider.Provider` streams neutral `Event`s over
  neutral `Message`/`Block` types. Each adapter translates to/from its vendor
  wire format; nothing above the provider is vendor-aware.
- **The loop is yours.** Tool-calling is the universal primitive every major
  provider supports, so the harness runs the loop itself rather than depending
  on a vendor's hosted agent runtime. That's what keeps it portable.
- **Capabilities, not branches.** `Request.CapFlags` ("caching", "tools", …)
  let a provider opt into features it supports; unknown ⇒ conservative default.
- **Automatic routing.** Tiers (fast/balanced/reasoning) map to per-provider
  models; a profile picks a base tier, cheap work delegates to a fast worker,
  and a fumbled turn escalates one tier up.
- **Events are pure data.** The same stream drives the CLI, the `chat` REPL, and
  the SSE web server — frontends are just consumers.
- **Identities are scoped.** Each profile has its own data dir (auth, memory,
  skills, sessions), like Construct profiles — different identities connect
  different accounts.

## Layout

```
provider/   Provider interface + neutral types; mock/anthropic/openai(+compat) adapters; registry
tool/       Tool interface, Registry, mediated Env; read/write/edit/list files, web_fetch, web_search, bash
agent/      the provider-agnostic loop (stream → accumulate → dispatch tools → repeat) + compaction
router/     tier → per-provider model table; classify + escalate
profile/    identities (persona, tier, delegation) + per-identity data dirs
skill/      SKILL.md loader, load_skill + self-improving learn_skill
memory/     per-identity durable facts + remember tool
session/    persisted multi-turn conversations
schedule/   proactive scheduled tasks (every / daily / weekly)
connector/  integration layer: native, MCP, Google (calendar) connectors
server/     HTTP+SSE transport (POST /v1/chat, GET /v1/sessions, /healthz)
auth/       OAuth (Anthropic PKCE, Google) + token store
config/     config.json (search, Google client, default profile)
cmd/harness/ thin reference CLI built on the library
```

## Install

```sh
go install github.com/flakerimi/harness/cmd/harness@latest
```

Or build from a checkout: `go build ./cmd/harness` (Go 1.26+, no dependencies).

## Run

No keys needed — the `mock` provider exercises the full loop offline:

```sh
go run ./cmd/harness -provider mock "hello there"
```

Real providers (bring your own key, or `harness login` for Claude OAuth):

```sh
ANTHROPIC_API_KEY=sk-... go run ./cmd/harness -provider claude "summarize go.mod"
OPENAI_API_KEY=sk-...    go run ./cmd/harness -provider openai "say hi"
go run ./cmd/harness -provider ollama -model llama3.1 "say hi"   # local, no key
```

## Onboarding — personalize for a business

Turn the generic harness into a specific assistant (an ISP's support desk, a
social-media agency, …) with a short interview — the model drafts a tailored
persona and writes the identity:

```sh
harness onboard
#  Business / org name: Acme ISP
#  What does it do: regional fiber internet
#  Your role: support lead
#  Tone: friendly, clear, no jargon
#  → drafting persona… ✓ wrote profiles/acme-isp.md (set as default)
#  Next: harness chat -profile acme-isp · harness connect google -profile acme-isp
```

It writes a `profiles/<slug>.md` identity and sets it as the default. Add domain
know-how afterwards with skills (`skills/<name>/SKILL.md`) and specialists
(`agents/<name>.md`). `-no-ai` uses a built-in template instead of the model.

## Commands

```sh
harness onboard               # guided setup for a business → a tailored identity
harness [flags] <prompt>      # one-shot run (default)
harness chat                  # multi-turn conversation, persisted per identity
harness serve                 # HTTP+SSE server (POST /v1/chat)
harness channel telegram ...  # reach the assistant from Telegram
harness schedule add ...      # proactive scheduled tasks
harness task add ...          # queue background work ("do X, report when done")
harness profiles              # list identities
harness skills                # list skills (incl. the identity's learned ones)
harness skills search <q>     # find skills in the git registry
harness skills add <name>     # install one from the registry
harness memory                # what the identity remembers
harness memory search <q>     # search that identity's memory
harness sessions              # list stored conversations
harness reflect               # review recent chats → learn (memory + skills)
harness login [-provider claude]   # model-provider OAuth
harness connect google             # integration OAuth (Calendar)
harness connectors                 # what's connected + the tools exposed
harness config                     # config + search settings
```

### Chat — multi-turn, persistent

```sh
harness chat -provider claude                 # default rolling conversation
harness chat -session work-trip               # a named side conversation
harness chat -new                             # start fresh
```

The conversation survives across invocations. Long chats are compacted: once the
history exceeds the `-compact` token budget, the oldest turns are summarized by a
fast-tier call into one synthetic exchange, keeping the prompt in-window while
the on-disk session keeps full fidelity.

### Schedule — proactive runs

```sh
harness schedule add -profile work -provider claude -spec "daily 08:00" "brief me on my day"
harness schedule add -spec "every 2h" "check my inbox and flag anything urgent"
harness schedule add -profile personal -provider claude -spec "daily 09:00" \
  -deliver telegram:<chatID> "resurface a memory worth revisiting, as a warm one-liner"
harness schedule add -profile work -provider claude -spec "daily 07:00" \
  -deliver telegram:<chatID> "run the chief-of-staff skill"   # morning brief → your phone
harness schedule list
harness schedule run-due        # fire what's due (wire to system cron/launchd)
harness schedule daemon         # or keep a process checking every minute
```

Specs: `every 30m` · `every 1d` · `daily 08:00` · `weekly mon 09:00`. A task's
output streams to stdout by default; `-deliver telegram:<chatID>` (with
`$TELEGRAM_BOT_TOKEN` set) also pushes it to your phone — the wire that turns a
scheduled run into a proactive message.

### Background tasks — work now, off your critical path

Where the schedule fires on a clock, the task queue fires immediately — in the
background. Queue from the CLI, or let the **agent queue work for itself**: the
`background_task` tool lets it say "I'll work on this and get back to you" in a
chat, and the daemon's worker runs the job as the same identity (its memory,
skills, workspace) and delivers the result back to the chat that asked.

```sh
harness task add -profile personal -deliver telegram:<chatID> \
  "research X in depth and write the brief to workspace:briefs/x.md"
harness task list
harness task show <id>       # status + stored result
harness task drain           # execute queued jobs now (no daemon needed)
```

Jobs persist as JSON; a daemon restart re-queues anything caught mid-run.
Failures are delivered too, so the asker is never left waiting on silence.

### Daemon — one background process

`harness daemon` runs the HTTP API + scheduler + task worker + (optionally) the
Telegram bot together under one graceful shutdown — the thing you deploy and
leave running.

```sh
harness daemon -provider claude                       # API + scheduler
harness daemon -provider claude -telegram -allow 123  # + the Telegram bot
harness daemon -print-launchd                         # macOS launch-agent plist
```

On a VPS it sits behind HTTPS (CapRover/nginx), with the API token as the org
secret; `launchctl`/systemd (or the proxy's process manager) keeps it alive.

### Serve — HTTP + SSE API (local or a shared VPS)

A token-gated JSON/SSE API so web, desktop, and mobile clients all talk to one
brain — run it locally, or deploy it to a VPS (e.g. behind HTTPS on CapRover)
that a whole org connects to for shared identities, memory, and sessions.

```sh
harness serve -provider claude -addr :8080      # auto-generates + prints an API token
harness serve -open                             # NO auth (localhost dev only)

TOKEN=…   # from startup, or -token / $HARNESS_API_TOKEN
curl -s  -H "Authorization: Bearer $TOKEN" localhost:8080/v1/profiles
curl -s  -H "Authorization: Bearer $TOKEN" localhost:8080/v1/models
curl -sN -H "Authorization: Bearer $TOKEN" -X POST localhost:8080/v1/chat \
     -d '{"profile":"business","session":"web1","message":"hi","provider":"kimi"}'
```

| Endpoint | What |
|---|---|
| `POST /v1/chat` | stream a turn (SSE); body `{profile, session, message, provider?, model?}` |
| `GET /v1/profiles` | the identities |
| `GET /v1/models` | providers + their models |
| `GET /v1/sessions?profile=` | a profile's conversations |
| `GET /v1/sessions/{id}?profile=` | one conversation's history |
| `GET /healthz` | open liveness check |

Auth: every `/v1` route needs the token (Bearer header, or `?token=` for
EventSource). CORS is permitted (token-gated), so browser/desktop clients on
other origins work. TLS is terminated by the reverse proxy in front.

### Web client

A decoupled Vue 3 SPA lives in [`web/`](web/) — connect with the API URL +
token, pick an identity and model, and chat with streaming replies. It's a pure
client over the same `/v1` API (local or a remote VPS); desktop/mobile can be
thin wrappers over it.

```sh
cd web && npm install && npm run dev    # http://localhost:5173
```

### Telegram — assistant on your phone

```sh
harness channel telegram -token <BotFather-token> -profile personal -provider claude
# or: TELEGRAM_BOT_TOKEN=... harness channel telegram -provider claude
```

Each Telegram chat resumes its own per-identity session, so the conversation
has memory across messages.

## Identities (personal vs business)

A profile is *who the assistant is for* — and each identity is fully scoped: its
own connected accounts, memory, skills, sessions, and tools, isolated from the
others. So "you personally" and "you at work" are different identities with
different email, calendar, and tools.

```sh
harness profiles                                  # personal, work, business, …
harness connect google -profile business          # connect the work email/calendar to that identity
harness chat -profile business                     # talk to the business identity
harness -profile personal "what's on my calendar"  # uses the personal account
```

Everything is per-identity, under `<config>/harness/profiles/<name>/`. On
remote surfaces (daemon, Telegram, schedules) the identity's filesystem tools
are rooted in its **workspace** — a persistent home for files that survives
across sessions; a CLI run works in cwd (`-root`) with the workspace alongside
(tools address it with a `workspace:` path prefix).

The agent has real hands — `write_file`, `edit_file`, `list_dir` next to
`read_file` — mediated by a **permission gate**: on the CLI (your own
directory) every mutating call (`write_file`, `edit_file`, `bash`) asks on the
terminal first (`-yes` waives it; non-interactive runs deny). Remote surfaces
skip the prompt — they're sandboxed inside the identity's workspace, and path
traversal out of the sandbox is rejected. The gate follows delegated and
dispatched workers too, so it can't be bypassed via a sub-agent.

| Scoped per identity | Where |
|---|---|
| Connected accounts (Google/email) | `auth.json` |
| Durable memory | `memory/` |
| Learned skills | `skills/` |
| Conversations | `sessions/` |
| Workspace (persistent files: drafts, notes, projects) | `workspace/` |
| Tools (MCP servers) | `mcp.json` (layered on the shared `mcp.json`) |

Add an identity by dropping a `profiles/<name>.md` (persona + tier + delegation).
Connecting Google needs a Desktop OAuth client once (`google.client_id/secret`
in config, from console.cloud.google.com).

## Extending it — everything is a dropped file

The harness has four pluggable layers, all file-based and per-identity scopable:

| Layer | What | File |
|---|---|---|
| **Identities** | who the assistant is | `profiles/<name>.md` |
| **Skills** | *procedures* the agent follows itself | `skills/<name>/SKILL.md` |
| **Specialists** | *sub-agents* it dispatches tasks to | `agents/<name>.md` |
| **Tools** | raw capabilities | connectors / `mcp.json` |

**Skills** (agentskills.io / Anthropic open format) reach the model by progressive
disclosure: names + descriptions go in the system prompt; the model calls
`load_skill` to pull full instructions only when a task matches. An identity can
also `learn_skill` — saving a workflow it worked out, reusable by name next time.
Skills can also come from a shared **registry** — a git repo of skill folders.
Point `skills.registry` (in config, or `$HARNESS_SKILLS_REGISTRY`) at the repo,
then `harness skills search <q>` browses it and `harness skills add <name>`
installs a skill into the shared skills dir (`-profile` for one identity's own).
The clone is cached and only refreshed with `-refresh`.

**Specialists** are separate agent runs with their own persona, model tier, and
tool subset. The orchestrator dispatches a focused task to one by name
(`dispatch(agent, task)`); it runs in isolation and returns its result — good for
focus, parallelism, and running grunt work at a cheaper tier. Define one:

```markdown
---
name: researcher
description: Deep web research; returns a sourced brief.
tier: fast
tools: web_search, web_fetch
---
You are a research specialist. Find concrete, sourced facts and return a brief.
```

`harness skills` / `harness agents` / `harness profiles` list each layer. A skill
is a *recipe*; a specialist is a *separate worker*.

## Status

The engine plus all six pillars: identities, memory, skills (+ self-learning
and a git-backed registry), sessions, compaction, scheduling, an HTTP/SSE
server, and a Telegram channel. Provider adapters for mock, Anthropic (API key +
OAuth), and OpenAI-compatible (OpenAI, DeepSeek, Gemini, Kimi, Mistral, Qwen,
OpenRouter, Fireworks, Together, xAI, Z.ai, MiMo, Apple, Ollama, LM Studio).
Connectors: native tools, MCP, and Google Calendar + Gmail (read + draft + send).

Next: more channels (Slack).

## Contributing

Small, dependency-free, offline-testable — see [CONTRIBUTING.md](CONTRIBUTING.md)
for the ground rules before opening a PR.

## License

[MIT](LICENSE)
