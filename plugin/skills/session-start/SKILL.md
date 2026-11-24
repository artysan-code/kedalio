---
name: session-start
description: Use when a session opens in a working folder, at your first reply, before any other work. Loads what Kedalio knows about the folder's project (last time, next steps, due tasks) and settles which project the folder belongs to.
---

# Session start

Run this once, at the first reply of a session in a working folder. Skip it when the user only asks a quick question
unrelated to a folder.

1. Get the folder's absolute path and its git remote (`git remote get-url origin`; none is fine).
2. Call `session_start` with `dir` and `remote`.
3. If the answer says the folder is unknown (`folder: {ask}`), ask the user once, in one message:
   - link it to an existing project (list the candidates the answer gives),
   - create a new project (propose a lower-case path `a/b/c` from the folder name),
   - or ignore this folder.

   Then call `projects_link` with `op` = `link`, `create` or `ignore`, the `dir`, and for `link` the `project`, for
   `create` the `name`. Pass `repo` (the remote) and `branch` (the default branch) when the folder has them. Do not ask
   again for a folder the user ignored.
4. Report in at most five lines: when the project was last worked on, what is next, the tasks due or overdue. Nothing
   else; no tool output pasted.
5. Continue with what the user asked. A task or decision the user states later is saved at once (`tasks_save`,
   `memory_write`), not left to the end.

Never write secrets, tokens or recovery codes to memory or tasks.
