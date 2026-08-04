# App resilience, hardening, Claude OAuth, sessions, TestFlight — design

**Date:** 2026-08-04
**Repos:** `~/Harnes` (engine, `flakerimi/harness`), `~/dev/harness-app` (Flutter iOS client, no remote), `~/dev/donna` (deployment, `flakerimi/donna`)
**Trigger:** `ClientException: Connection closed while receiving data, uri=https://donna.common.al/v1/chat` whenever the iOS app is backgrounded mid-turn.

## Problem

1. `server.go handleChat` runs the agent turn on `r.Context()`. iOS suspends the app → socket dies → context cancels → **the turn is killed server-side**. The Flutter client has no lifecycle handling and no reconnect; the stream error surfaces as red text.
2. The Claude OAuth login (`auth/login.go`) binds a callback on `127.0.0.1:53692` on the daemon host — impossible on deployed Basepod, so the app cannot connect Claude and sessions run on fireworks/deepseek.
3. General hardening is thin (plain `==` token compare, no rate limits, no body caps) and the app stores its bearer token in `shared_preferences`.
4. Session list is raw IDs/dates; no titles, no easy continuation.
5. No release pipeline to TestFlight.

## Decisions (user-confirmed)

- Turns **survive** client disconnects; app **re-attaches to the live stream** (approach A: SSE journal + `Last-Event-ID`-style resume). WebSockets rejected (same iOS problem, bigger rewrite); poll-only rejected (loses live streaming).
- Claude OAuth: **both** paths — paste-code flow in the app (primary) + `deploy.sh --seed-auth` from a Mac login (fallback).
- Hardening scope: server limits/timeouts, app resilience + Keychain, auth-file protection. (Brute-force lockout: out of scope.)
- Sessions: auto-titles, continue-from-list, newest-first sort. (Search, pin/archive: out of scope.)
- Full review of both codebases; findings fixed as part of this work.
- TestFlight: App Store Connect record exists; scripted upload via `release.sh`.

## 1. Detached turns + live resume

### Server (`server/turns.go`, new)

- `TurnManager` keyed by `profile/session`. One running turn per key; concurrent cap across all keys (default 4, config `server.max_turns`).
- `POST /v1/chat` validates as today, then delegates: manager runs `agent.ContinueWith` on `context.Background()` + turn timeout (default 10 min, config `server.turn_timeout`). The HTTP handler merely subscribes.
- Every SSE frame carries `id: <seq>` (monotonic per turn). Frames append to an in-memory ring journal (cap 512 events) and broadcast to subscribers. Subscriber disconnect ≠ turn cancel. Session save runs in the turn goroutine.
- Second `POST /v1/chat` for a busy session → `409 {"error":"turn in progress","seq":<latest>}`.
- `GET /v1/chat/stream?session=&profile=&after=<seq>` — replay journal `> after`, then follow live; if the turn finished, replay ends with the buffered `done`. Journal survives until the next turn starts.
- Keepalive comment frame (`: ping`) every 20 s on idle streams.

### App

- `HarnessClient.chat` parses `id:`; exposes `ChatEvent.seq`. New `Stream<ChatEvent> resume({session, profile, after})`.
- Chat screen: `WidgetsBindingObserver`. Background → record `(session, lastSeq, turnActive)`. Foreground with `turnActive` → resume from `lastSeq`, merge into the in-progress bubble. Mid-stream `ClientException`/`SocketException` → auto-resume, backoff 1 s/2 s/4 s, error UI only after all three fail. `409` on send → attach via resume instead.

### Tests (all offline)

- Go: mock-provider turn; drop subscriber mid-turn → turn completes → resume replays exact suffix incl. `done`; double-POST → 409; journal reset on next turn; concurrency cap enforced.
- Dart: SSE `id:` parsing; resume merge produces identical transcript to uninterrupted stream.

## 2. Review + hardening

- **Review pass** over both codebases (Go: races, context misuse, error swallowing, leaks; Dart: unawaited futures, setState-after-dispose, stream leaks). Findings fixed in this cycle and reported.
- **Pending OpenAI provider fix** (uncommitted in `provider/openai.go`): tool parameter schemas gain an empty `properties` map when absent, so strict validators (LM Studio) accept them. Validate, add a regression test, commit — first task, clears the dirty tree.
- `guard`: `subtle.ConstantTimeCompare` for the bearer token.
- Rate limit `/v1`: token bucket per token+IP, 60 req/min (config), 429 on excess; established streams exempt.
- `http.MaxBytesReader`: 12 MB on `/v1/chat`, 1 MB elsewhere. `ReadHeaderTimeout` 10 s, `IdleTimeout` 120 s, no `WriteTimeout` (streams).
- `auth.json`: written 0600; present in `.gitignore` + `.dockerignore` of all three repos; lives only on `/data`.
- App: token → iOS Keychain via `flutter_secure_storage` (one-time migration from `shared_preferences`, then delete old key); offline banner from `health()`; list screens get retry.

## 3. Claude OAuth

### Server

- `POST /v1/auth/claude/start` → `{url, state}`; PKCE verifier held server-side keyed by `state`, 10-min TTL, single use. URL uses the manual-code variant (claude.ai displays the code) — no localhost callback.
- `POST /v1/auth/claude/complete {code}` → accepts `code` or `code#state`, exchanges via existing `exchangeCode`, saves to auth store (0600). Idempotent re-connect overwrites.
- `/v1/connectors` lists claude with connected/expired status (from stored credential expiry + refresh-ability).
- Registry unchanged (already falls back to OAuth when `ANTHROPIC_API_KEY` unset); error message on missing auth improved to point at the app flow.

### App

- Settings → Connect Claude: opens minted URL via `url_launcher`, paste field, complete → connected badge; provider picker then offers claude models (curated list already in `registry.go`).

### Fallback

- `donna/deploy.sh --seed-auth`: copy local `~/Harnes` auth.json (from `harness login claude` on the Mac) to the Basepod `/data` volume.

## 4. Sessions

- Server: after a session's first completed turn, async one-shot title (≤ 6 words) via the session's provider; stored in session meta; failures leave title empty (no retry loop). `GET /v1/sessions` → adds `title`, `updated_at`; sorted newest-first server-side.
- App: sessions list shows title (fallback: first-user-message snippet), relative date, newest on top; tap → full scrollback via existing `sessionHistory`, composer bound to that session id.

## 5. TestFlight

- `harness-app/release.sh`: bump build number (`pubspec.yaml` `version: 1.0.0+N`), `flutter build ipa --release --export-method app-store`, upload via `xcrun altool --upload-app --type ios` supporting **either** credential: ASC API key (`ASC_KEY_ID`/`ASC_ISSUER_ID`/`ASC_KEY_PATH` env) **or** Apple ID + app-specific password (`-u dev@basecode.al -p @keychain:AC_PASSWORD`). Fails loudly if neither is configured.
- **User-provided prerequisite (one of):** ASC API key (App Manager role), or an app-specific password for dev@basecode.al stored in keychain as `AC_PASSWORD`. Existing keys on disk can't upload: `AuthKey_STGRH25HZB.p8` is APNs-only, `SubscriptionKey_P54FYCYGTC.p8` is IAP-only. Prior installs were ad-hoc via `devicectl` to the phone, not TestFlight.
- Order of shipping: (1) engine changes land in `~/Harnes`, tagged release; (2) donna image rebuilt + `deploy.sh` (server live with resume + OAuth); (3) app released to TestFlight pointing at `donna.common.al`.
- Post-upload manual steps (user): export compliance answer (uses HTTPS only → exempt), add internal tester, install via TestFlight app.

## Error handling summary

- Turn timeout → `error` frame + `done` in journal; session history keeps partial progress.
- Resume with `after` beyond journal (evicted) → full-replay flag so the client refetches history first.
- OAuth exchange failure → 4xx with Anthropic's error text passed through; state TTL expiry → clear "start over" error.
- Rate-limited client → app backs off and surfaces a quiet toast, not red error text.

## Out of scope

Brute-force lockout, session search/pin/archive, WebSockets, Android release, web-UI parity for resume (it reconnects but without journal replay — acceptable, desktop doesn't background-kill).
