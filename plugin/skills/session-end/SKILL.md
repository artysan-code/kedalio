---
name: session-end
description: Use when the user wraps up, says they are done or stopping, or the work in a folder reaches a natural end. Saves the project card, the tasks and the decisions of the session to Kedalio and notes what exists only on this machine.
---

# Session end

Run this when the work ends. Be short; save what a future session needs, not a diary.

1. Tasks: for each task finished, started, blocked or newly agreed, call `tasks_save` (no `id` adds; with `id` it
   changes; put the reason in `log`, a choice in `decision`). Do not duplicate a task that exists: check with
   `tasks_list` or `tasks_brief` first.
2. Decisions and facts worth keeping: call `memory_write`. Use `op` = `remember` for a single fact (with `about`),
   `append` for a note, `edit` or `write` to change a page (read it first with `memory_read` and pass `base_rev`).
3. Project card: read the project's page, then update its card (what the project is, where it stands, what is next)
   with `memory_write` `op` = `edit`. Keep it short and current; replace stale lines rather than adding below them.
4. Local only: say plainly what exists only on this machine and is not in Kedalio or a git remote: uncommitted or
   unpushed changes, local branches, files outside the repository, services running here.
5. Close with two or three lines: what was saved, what is next, what is local only.

Never write secrets, tokens or recovery codes to memory or tasks. Do not commit or push unless the user asked.
