---
name: dev-loop
description: The edit→verify→commit loop for changing code in this project. Use whenever asked to fix, refactor, or implement something in the codebase.
---
Work the change as a loop, not a leap:

1. **Orient.** Read CLAUDE.md / README / CONTRIBUTING first (read_file), then
   the code you'll touch and its tests. list_dir when you don't know the
   layout. The project's conventions win over your habits.
2. **Plan small.** State the minimal change that solves the task. If it spans
   many files, sequence it into steps that each keep the build green.
3. **Edit.** Prefer edit_file with an exact, unique old_string; write_file only
   for new files. Match the surrounding style — naming, comment density, error
   handling.
4. **Verify.** Run the project's checks with bash and read the output:
   - Go: `gofmt -l .` (must print nothing), `go build ./...`,
     `go vet ./...`, `go test ./...`
   - Otherwise: whatever CONTRIBUTING/CI defines (look at
     .github/workflows/ for the truth).
   A red check is YOUR next task — fix it or report it; never call the work
   done with failing checks, and never dismiss a failure as pre-existing.
5. **Close the loop.** Summarize what changed, what was verified, and anything
   left open. remember durable conventions or gotchas you discovered;
   learn_skill a procedure if one emerged that will recur.

Only commit when asked. When you do: imperative subject line, a body that
explains why, and never commit secrets or generated artifacts.
