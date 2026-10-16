# Token economy of a long agent session

A measured session (one agent, one working day on LisaOS, 2026-10-16):

| | tokens |
|---|---|
| Input | 1,338,984,660 |
| Cached | 1,327,893,021 |
| Output | 1,848,581 |
| Total | 1,340,833,241 |

The model reads ~725× more than it writes. 99.2% of the input was served from
the prompt cache, so the money is not in fresh tokens but in the sheer size of
the context that is re-sent on every turn: a long session carries its whole
history, tool output included, into each request.

## What to research

1. **Context as a working set, not a transcript.** Keep the live context to
   what the current task needs; move everything else to files the agent reads
   back on demand (a room file, a tracker, per-topic notes). The session above
   did this partly (append-only coordination file, memory files) and still
   grew to ~1.3 GB of input.
2. **Tool output budgets.** Most of the bytes are tool results: logs, diffs,
   listings. Truncate at the tool boundary (`tail`, `cut`, counts) and keep the
   full text in a file the agent can open by path. The agent should ask for
   the lines it needs, not the file.
3. **Watchers instead of polling.** A background watcher that wakes the agent
   only on a condition (a marker line, a log pattern) replaces dozens of
   "check again" turns, each of which re-sends the whole context.
4. **Compaction on a schedule, with a written summary.** When the context
   grows past a budget, summarise the state to a file and start a fresh
   context from it; the summary should carry facts and open items, not prose.
5. **Cache-aware prompt shape.** Keep the stable prefix (system, skills,
   instructions) byte-identical across turns so the cache hits; put volatile
   material (timestamps, counters) at the end.
6. **Measure per turn.** Log input/cached/output per request with the turn's
   purpose, so the expensive patterns (a long log read, a re-read of a big
   file) show up as lines in a table instead of a total at the end of the day.
7. **Write more, read less.** Prefer producing a file or a diff over reading
   the whole target again; prefer `grep -n` over `cat`; prefer the record
   (a marker, a receipt) over the log that produced it.

The question for the harness: which of these it can enforce by shape
(budgets on tool results, scheduled compaction, cache-stable prefixes,
per-turn metering) so that an agent does not have to remember to be frugal.
