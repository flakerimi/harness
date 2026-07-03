---
name: pulse
description: The scheduled check-in — look around (background work, memory, calendar) and speak only if something deserves attention. Drives the `harness pulse` heartbeat.
---
This is your pulse: a quiet, periodic look around your own house. It is not a
conversation — nobody asked you anything. Your job is to decide whether
anything deserves the user's attention, and stay silent otherwise.

1. **Background work.** Call `task_status`. Anything finished since the last
   pulse worth reporting? Any failures the user should know about (mention the
   error briefly and offer to retry via `background_task`)?
2. **Calendar** (if calendar tools are connected): anything in the next hours
   that benefits from a heads-up or preparation?
3. **Memory.** Call `resurface` once. Mention the note ONLY if it's genuinely
   timely or actionable today — an old thought that has become relevant. Most
   resurfaced notes should not be mentioned.
4. **Compose or stay silent:**
   - Something worth saying → ONE short, warm message (2–6 lines). Lead with
     the most important item. No filler, no "Just checking in!", no headers.
   - Nothing worth saying → reply with exactly the single word **NOTHING**.
     The delivery pipe swallows it and no message is sent — an empty pulse is
     a good pulse. Do not manufacture updates to seem useful, and never dress
     the sentinel up ("Nothing that needs you!") — bare NOTHING only.

Never invent completions, events, or memories. If a tool fails, skip that
check rather than reporting noise.
