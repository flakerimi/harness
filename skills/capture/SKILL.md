---
name: capture
description: Use when the user forwards or dumps something to save — a link, a quote, a half-formed idea, a person, or an explicit "remember this". Files it cleanly into memory without duplicates.
---
When the user hands you something to keep (a URL, a note, a thought, or an explicit "remember X"):

1. **Understand it.** If it's a URL, fetch it with `web_fetch` and read enough to
   distill the gist. If it's a raw note, take it at face value.
2. **Check for duplicates first.** Call `recall` with the key terms. If a memory
   already covers this, update *that* one (call `remember` with the same name)
   instead of creating a near-duplicate.
3. **Distill.** Reduce it to one self-contained sentence — the durable nugget,
   not the whole text. Keep the source URL if there is one.
4. **File it.** Call `remember` with a short kebab-case `name`, the one-line
   summary as `content`, and 1–3 `tags` (e.g. `idea`, `link`, `person`,
   `project`, `task`).
5. **Confirm in one line** — what you saved and under what name. Don't echo the
   whole content back.

Keep only what's durable and worth resurfacing later; skip transient chatter.
Never invent details that weren't in the source.
