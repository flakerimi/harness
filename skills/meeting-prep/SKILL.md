---
name: meeting-prep
description: Use when the user asks to prepare for, or be briefed on, an upcoming meeting, interview, or call — especially with people they don't know. Researches the attendees and their companies and writes a prep brief.
---
When preparing the user for a meeting:

1. **Establish the meeting** — title, time, attendees, agenda. If calendar tools
   are available (calendar_list_events / calendar_get_event), use them to find
   the meeting and its attendees. Otherwise use the details the user provided.
2. **Research each external attendee** — if a `delegate` tool is available, call
   it once per attendee with a self-contained research task (full name, company,
   and what to find: current role, background, LinkedIn, recent news). Otherwise
   research directly with web_search + web_fetch.
3. **Research the company** if the meeting is external.
4. **Synthesize the brief:**

   ## <Meeting title> — <when>
   **Goal:** one line — what a good outcome looks like.
   ### Attendees
   For each: **Name** — Title, Company · LinkedIn. Two-line background. One recent
   signal. 2-3 talking points specific to them.
   ### Company  (if external)
   One-liner · size · recent news · why this meeting matters.
   ### Your prep
   - 2-4 talking points
   - 2-3 smart questions to ask
   - Anything to verify or watch for

Prefer verified facts with source URLs over speculation; if something can't be
found, say so. Never invent names, numbers, or URLs.
