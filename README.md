# harness

A small, multi-provider **agent harness framework** in Go. The agent loop is
provider-agnostic: Claude, OpenAI, or any OpenAI-compatible endpoint plug in
behind one streaming interface, and the loop above never knows which is in use.

The design borrows the proven `Provider` / `Agent` shapes from the Construct
`brain`, rebuilt here as a clean, standalone library with no gateway coupling.

## Why it's structured this way

- **One neutral seam.** `provider.Provider` streams neutral `Event`s over
  neutral `Message`/`Block` types. Each adapter translates to/from its vendor
  wire format; nothing above the provider is vendor-aware.
- **The loop is yours.** Tool-calling is the universal primitive every major
  provider supports, so the harness runs the loop itself rather than depending
  on any vendor's hosted agent runtime. That's what keeps it portable.
- **Capabilities, not branches.** `Request.CapFlags` ("caching", "tools", …)
  let a provider opt into features it supports; unknown ⇒ conservative default.
- **Events are pure data.** The same stream drives a CLI today and an SSE/web
  transport later — frontends are just consumers.

## Layout

```
provider/   Provider interface + neutral types; mock, anthropic, openai adapters; registry
tool/       Tool interface, Registry, mediated Env, read_file built-in
agent/      the provider-agnostic loop (stream → accumulate → dispatch tools → repeat)
cmd/harness/ thin reference CLI built on the library
```

## Run

No keys needed — the `mock` provider exercises the full loop offline (it calls a
tool, then answers):

```sh
go run ./cmd/harness -provider mock "hello there"
```

With real providers (bring your own key):

```sh
ANTHROPIC_API_KEY=sk-... go run ./cmd/harness -provider claude  "summarize go.mod" -root .
OPENAI_API_KEY=sk-...    go run ./cmd/harness -provider openai  "say hi"
# OpenAI-compatible local runtimes (no key):
go run ./cmd/harness -provider ollama -model llama3.1 "say hi"
```

## Status

Milestone 0: the spine — neutral provider interface, mock + Anthropic + OpenAI
adapters, tool registry with a sandboxed `read_file`, and the agent loop.

Next: pluggable context strategy (compaction, file-state tracking), MCP client,
sub-agents, an `Agent` profile layer (persona + tools + run-mode), and server /
web transports.
