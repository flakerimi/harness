# Integrations

How the harness talks to the outside world, and — more importantly — **which
route** each integration should take. The harness deliberately has four
integration seams instead of a pile of vendor SDKs:

| Route | What it is | When it's right |
|---|---|---|
| **Native connector** | Go code in `connector/` behind the `Connector` interface | Only when the integration needs OAuth we manage, streaming, or is core to the assistant loop |
| **MCP server** | any Model Context Protocol server in `mcp.json` | The vendor/community already ships one; sessions and state; rich toolsets |
| **Exec plugin** | one executable in `plugins/` (`spec` / `run` / `deliver`) | Personal glue, simple APIs, anything a bash/python script can do |
| **Deliver target** | `-deliver kind:dest` on schedules, wake-ups, background tasks | Notify-only: push text somewhere, no round-trip |

**The decision rule:** start at the bottom of that table and move up only when
forced. A weather API is a plugin, not a connector. Slack notifications are a
webhook, not a channel — until you need two-way chat, and then it's a channel.

## Today

| Integration | Route | Status |
|---|---|---|
| Telegram (two-way chat) | native channel (`channel/telegram`) | ✅ shipped — markdown, typing, feedback buttons, per-chat sessions |
| Telegram (notify) | deliver `telegram:<chatID>` | ✅ shipped |
| Slack / Discord / Mattermost / Teams (notify) | deliver `webhook:<url>` | ✅ shipped — one kind covers all incoming webhooks |
| Google Calendar | native connector (OAuth) | ✅ shipped (read) |
| Gmail | native connector (OAuth) | ✅ shipped (read + draft + send) |
| Web search | native tool → self-hosted SearXNG | ✅ shipped |
| Web fetch | native tool | ✅ shipped |
| Filesystem | native tools, sandboxed + permission-gated | ✅ shipped |
| Shell | native `bash` tool (opt-in `-bash`) | ✅ shipped |
| Anything MCP | `mcp.json` (shared + per-identity) | ✅ shipped — namespaced tools |
| Anything exec | `plugins/` (shared + per-identity + project) | ✅ shipped — tools + deliver kinds |

## Planned / candidates

### Channels (two-way chat surfaces)
| Integration | Route | Notes |
|---|---|---|
| Slack | native channel | Events API webhook on the daemon's HTTP server (it's already public when deployed) + `chat.postMessage`; Socket Mode needs websockets, Events API doesn't |
| Discord | native channel | interactions webhook, same daemon-endpoint pattern as Slack |
| Email as a channel | native channel | IMAP poll + `net/smtp` reply — stdlib has SMTP; "email the assistant, it emails back" |
| WhatsApp | native channel | via the Business Cloud API (webhook + HTTPS send) |
| iMessage | exec plugin | macOS-only AppleScript/shortcuts glue — exactly what plugins are for |
| Matrix | MCP or plugin | simple HTTP API; a plugin can send, a channel needs sync polling |

### Deliver targets (notify-only)
| Integration | Route | Notes |
|---|---|---|
| Email | native deliver `email:<addr>` | `net/smtp` + config for the relay; no dependency needed |
| SMS (Twilio et al.) | exec plugin advertising `sms` | one curl in a script |
| Desktop push (ntfy.sh / Pushover) | deliver `webhook:` already works | ntfy accepts plain POST |

### Google / Microsoft suites
| Integration | Route | Notes |
|---|---|---|
| Google Drive / Docs / Sheets | native connector | same OAuth client + token store as Calendar/Gmail; add scopes + tools |
| Google Contacts / Tasks | native connector | small API surface, same pattern |
| Outlook / Microsoft 365 | native connector | second OAuth family (MS identity platform); mirror the Google connector shape |

### Dev / work
| Integration | Route | Notes |
|---|---|---|
| GitHub / GitLab | exec plugin wrapping `gh` / `glab`, or MCP | the CLIs already handle auth; a plugin is 20 lines |
| Linear / Jira / Notion | MCP | official/community MCP servers exist |
| Postgres / SQLite | MCP | community MCP servers exist; keeps SQL out of the core |
| Obsidian / notes vault | none needed | it's files — point the identity's workspace (or `-root`) at the vault |

### Media / voice
| Integration | Route | Notes |
|---|---|---|
| ElevenLabs TTS | exec plugin (`say` tool / `voice` deliver kind) | one HTTPS call |
| Whisper STT | exec plugin | local `whisper.cpp` or API |
| Image generation (Flux, …) | exec plugin | returns a file path into the workspace |

### Infra / money
| Integration | Route | Notes |
|---|---|---|
| Porkbun / Hetzner / Basepod | exec plugins | thin API/CLI wrappers; per-identity so only the ops profile has them |
| Stripe / Polar | MCP (official) or plugin | read-only reporting first; anything that moves money must be `writes: true` so the gate covers it |
| Home Assistant | MCP or plugin | HA has an MCP server; lights/sensors as tools |

## Ground rules for new integrations

1. **Prefer the dumbest route that works.** Plugin over MCP over native. Native
   is a last resort, not a default — every native connector is code this repo
   maintains forever.
2. **Mutating actions declare `writes: true`** (plugin manifest or `tool.Spec`)
   so the permission gate covers them uniformly. Anything that sends, posts,
   deletes, or spends is a write.
3. **Per-identity scoping is the security model.** A connector/plugin/MCP
   server wired into the `business` profile does not exist for `personal`.
   Credentials live in the profile's data dir, never in the repo.
4. **Channels own identity mapping.** A channel decides which profile a
   conversation binds to (like Telegram's `/profile`); tools never do.
5. **Everything testable offline.** A native integration lands with fakes and
   unit tests; plugins get fixture-script tests. No integration may make
   `go test ./...` need the network.
