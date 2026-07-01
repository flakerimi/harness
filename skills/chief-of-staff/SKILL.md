---
name: chief-of-staff
description: Use for a proactive morning (or on-demand) briefing — triage today's calendar and overnight email into one tight digest, prep external meetings, and queue reply drafts. Ideal as a scheduled task delivered to a channel.
---
Act as the user's chief of staff. Produce ONE concise, phone-readable briefing —
not a wall of text. Work through these steps, then write only the digest.

1. **Today's calendar.** Call `calendar_list_events` for today. Note each
   meeting's time, title, and whether it's internal or external (external =
   has attendees outside the user's own domain).
2. **Overnight email.** Call `gmail_list_messages` with a query like
   `is:unread newer_than:16h` (or `in:inbox newer_than:1d`). For anything that
   looks important or time-sensitive, open it with `gmail_get_message`.
3. **Prep external meetings.** For each external meeting, load and follow the
   `meeting-prep` skill (call `load_skill` with name `meeting-prep`). If a
   `delegate` tool is available, delegate each meeting's prep so they run in
   parallel; otherwise do them inline, briefly.
4. **Queue reply drafts.** For emails that clearly need a reply the user would
   plausibly send, write a short draft with `gmail_create_draft` — pass
   `thread_id` and `in_reply_to` from `gmail_get_message` so it threads. NEVER
   send; drafts wait in Gmail for one-tap review. Skip anything ambiguous,
   sensitive, or that you'd be guessing at — flag those for the user instead.
5. **Write the digest** in this shape, tight:

   **☀️ Morning brief — <weekday, date>**
   **Today:** N meetings.
   - `HH:MM` **<title>** — internal/external · one-line what-it's-for. (prep ↓ if external)
   **Meetings to prep:** for each external one, 2–3 bullet talking points + one smart question.
   **Inbox:** M unread worth noting.
   - **<sender>** — one-line gist. → *drafted a reply* / *needs your call*.
   **Flags:** anything urgent, a conflict, or something you couldn't safely handle.

Keep it scannable. Prefer verified facts with sources over speculation; if
something can't be found or is uncertain, say so. Never invent names, times, or
email content, and never send mail — only draft.
