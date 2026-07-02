# harness-web

A small Vue 3 + Vite single-page client for the harness HTTP API. It's fully
decoupled from the Go daemon — it talks to the token-gated `/v1` endpoints over
HTTP (CORS is enabled server-side), so it runs anywhere and points at a local or
remote harness (e.g. `https://harness.construct.space`).

## Run

```sh
# 1. start the API (prints a token)
harness serve -provider claude          # or: harness daemon -provider claude

# 2. start the web client
cd web
npm install
npm run dev                              # http://localhost:5173
```

In the app: enter the **API URL** (e.g. `http://localhost:8080`) and the
**token**, Connect, then pick an identity + model and chat. Replies stream over
SSE. The conversation is the same one the CLI/Telegram see (shared server-side
state). The **tasks** button opens the background-task queue — queue work for
the current identity, watch statuses update live, expand a job to read its
result (execution needs `harness daemon` or `harness task drain`).

## Build

```sh
npm run build      # → dist/  (static files; serve behind any web server)
```

The chat endpoint is a POST that streams SSE, so the client uses `fetch` + a
`ReadableStream` (not `EventSource`, which can't POST or set an auth header) and
parses the SSE frames in `src/api.js`.
