# Contributing

Thanks for your interest! harness is deliberately small and dependency-free —
please keep it that way.

## Ground rules

- **Standard library only.** `go.mod` has no `require` block and that's a
  feature, not an accident. No vendor SDKs, no frameworks. If a feature needs a
  third-party package, it probably belongs behind a connector (MCP) instead.
- **Nothing above the provider layer is vendor-aware.** New providers plug in
  behind `provider.Provider` (most are a few lines in `registry.go` on top of
  the OpenAI-compatible adapter). Don't add per-vendor branches to the agent
  loop — providers opt into features via `Request.CapFlags`.
- **Tests are offline.** Pure unit tests against the `mock` provider and fakes:
  no network, no API keys, no build tags. `go test ./...` must pass on a plane.
- **Everything pluggable is a dropped file.** Identities are `profiles/<name>.md`,
  skills are `skills/<name>/SKILL.md`, specialists are `agents/<name>.md`. Prefer
  extending those seams over adding flags.

## Workflow

```sh
go build ./...   # must stay green
go vet ./...
go test ./...
gofmt -l .       # must print nothing
```

Exercise the full loop offline with no keys:

```sh
go run ./cmd/harness -provider mock "hello"
```

Every package has a doc comment on its primary file explaining *why* the seam
exists — read it before changing the package.

## Pull requests

- One focused change per PR, with tests.
- Keep commit messages in the imperative with a short body explaining *why*.
- Never commit credentials or personal data (`auth.json`, `config.json`,
  `mcp.json`, `.env*` are gitignored for a reason).
