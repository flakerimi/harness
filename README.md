# harness

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
| **Memory** | per-identity durable facts, injected + a `remember` tool | `memory/`, `harness memory` |
| **Skills** | SKILL.md workflows (agentskills.io format), progressive disclosure | `skill/`, `harness skills` |
| **Self-improvement** | the agent writes its own skills via `learn_skill` | `skill/` |
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
tool/       Tool interface, Registry, mediated Env; read_file, web_fetch, web_search, bash
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

## Commands

```sh
harness [flags] <prompt>      # one-shot run (default)
harness chat                  # multi-turn conversation, persisted per identity
harness serve                 # HTTP+SSE server (POST /v1/chat)
harness channel telegram ...  # reach the assistant from Telegram
harness schedule add ...      # proactive scheduled tasks
harness profiles              # list identities
harness skills                # list skills (incl. the identity's learned ones)
harness memory                # what the identity remembers
harness sessions              # list stored conversations
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
harness schedule list
harness schedule run-due        # fire what's due (wire to system cron/launchd)
harness schedule daemon         # or keep a process checking every minute
```

Specs: `every 30m` · `every 1d` · `daily 08:00` · `weekly mon 09:00`.

### Serve — HTTP + SSE

```sh
harness serve -provider claude -addr :8080
curl -N -X POST localhost:8080/v1/chat -d '{"profile":"personal","message":"hi"}'
```

### Telegram — assistant on your phone

```sh
harness channel telegram -token <BotFather-token> -profile personal -provider claude
# or: TELEGRAM_BOT_TOKEN=... harness channel telegram -provider claude
```

Each Telegram chat resumes its own per-identity session, so the conversation
has memory across messages.

## Skills

Skills are folders with a `SKILL.md` (frontmatter `name` + `description`, then
instructions) in the agentskills.io / Anthropic open format. They reach the model
by progressive disclosure: names + descriptions go in the system prompt, and the
model calls `load_skill` to pull full instructions only when a task matches. An
identity can also `learn_skill` — saving a workflow it worked out into its own
skills dir, reusable by name next time.

## Status

The engine plus all six pillars: identities, memory, skills (+ self-learning),
sessions, compaction, scheduling, an HTTP/SSE server, and a Telegram channel.
Provider adapters for mock, Anthropic (API key + OAuth), and OpenAI-compatible
(OpenAI, DeepSeek, Gemini, Ollama, LM Studio). Connectors: native tools, MCP,
and Google Calendar + Gmail (read).

Next: more channels (Slack), Gmail send/draft, and pulling skills from a registry.
