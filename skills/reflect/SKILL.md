---
name: reflect
description: Use to review recent conversations and improve — turn what happened into durable skills and memory. Ideal as a nightly scheduled run or when asked to "reflect", "review our chats", or "learn from today".
---
You are reviewing your own recent work to get better — not continuing any
conversation. Be honest and specific; vague self-praise is useless.

1. **Gather.** Call `review_sessions` to read your recent conversation
   transcripts (raise `limit` if asked to look back further).
2. **Mine each conversation for lessons:**
   - **Corrections / pushback** — where the user redirected you, and what they
     actually wanted. These matter most.
   - **Durable preferences** — tone, format, tools, or workflow they favor.
   - **Repeatable procedures** — a multi-step task you worked out that could
     recur.
3. **Encode what's durable:**
   - `remember` each fact or preference, with a clear name and a `lesson` tag
     when it's a correction to apply next time.
   - `learn_skill` when a reusable procedure emerged. If you're refining an
     existing skill, `load_skill` it first and improve on it rather than
     clobbering it.
   - Skip one-offs, transient task details, and anything trivial.
4. **Report** a short **What I learned** summary: 2–4 bullets naming exactly what
   you saved (skill or memory name + why). If nothing was worth keeping, say so
   plainly — don't invent lessons to look busy.

Never fabricate corrections or preferences the transcripts don't support.
