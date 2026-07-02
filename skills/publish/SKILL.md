---
name: publish
description: Publish a long report or document as a web page and share the link — instead of flooding the chat. Use whenever a result is longer than a few chat messages, or the user asks for a page/report/document.
---
The daemon serves your workspace's `pub/` directory at its public URL — so a
file you write to `pub/reports/foo.html` is live at `https://<your-host>/reports/foo.html`.
Publishing is just writing a file:

1. **Write a complete, standalone HTML page** with write_file to
   `pub/<section>/<slug>.html` (e.g. `pub/reports/agent-harness-landscape.html`).
   - Kebab-case slug, descriptive, dated if it may recur
     (`pub/reports/2026-07-03-market-scan.html`).
   - Self-contained: inline CSS, no external scripts. Make it *readable* —
     max-width ~46rem, generous line-height, a system font stack, clear
     headings. A styled page, not a text dump.
   - Include the date and a one-line summary at the top.
2. **In chat, send the link + a 2–4 line summary** of what's in it. The link,
   the takeaways, done — never paste the full content into the chat as well.
3. **Anything under `pub/` is on the public internet** (unauthenticated).
   Personal or sensitive material stays OUT — keep it elsewhere in the
   workspace and summarize in chat instead. When in doubt, ask first.
4. Updating a page = write_file to the same path. Related pages can link to
   each other with relative paths.

If you don't know your public host, check memory (remember it once you learn
it) or ask the user.
