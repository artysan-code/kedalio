---
name: kedalio-gate
description: What Kedalio's gate (scripts/ci.sh) runs, how to set up the git hooks, how to add or speed up a check, and how to read a failure. Use when CI or a hook is red or fails (pre-commit, commit-msg, pre-push), the gate is slow, a check must be added, or the owner asks "la CI è rossa", "perché fallisce il gate", "è verde?" about the gate, "aggiungi un check".
---

# The gate

`scripts/ci.sh` is the whole gate; CI runs it on GitHub Actions (`.github/workflows/ci.yml`, job `gate`) on every push
and pull request. The verdict is the run's, shown on the commit: `gh run list --branch <branch>`, `gh run watch <id>
--exit-status`, `gh run view <id> --log-failed`. On `development`, a green gate goes on to the `image` and `deploy` jobs (docs/ci.md).

- **Full**: `deno fmt --check`, `deno lint`, `deno check`, shell scripts (`bash -n`, shellcheck), Conventional Commit
  subjects (`scripts/commits.ts`), nothing personal (`scripts/personal.ts`), the repository's skills
  (`scripts/skills.ts`), the tooling's tests (`scripts/test.sh`), Go's format, vet and staticcheck (`scripts/go.sh check`)
  and tests (`go.sh test`; `go.sh vuln` on `development`), and the secret scan (gitleaks).
- `--fast`: the same without the secret scan. `--quick`: only the cheap steps (check, Go's format and vet, subjects,
  personal, skills).
- Steps run at once, each into its own log (on a working branch in CI, steps whose inputs did not change are skipped:
  `test` and `skills` run only when scripts or `.agents/` changed, so a docs-only branch runs the cheap steps); a failed
  one prints its whole log. `CI_VERBOSE=1` prints every log.
- CI runs the whole gate on every run, with no lookup of an earlier run of the same tree and no note: a green run is
  cheap enough to repeat. A newer push to the same ref cancels the older run.
- Tools optional on a workstation (shellcheck, gitleaks) are **required in CI** (`CI=true`).
- **A new check goes into `ci.sh`**, never only into a workflow file, and gets a test next to its script.

## Rules for a new step

CI runners can be slow and shared, so every step has a deadline and competes with the others.

- **Declare its inputs** and add it with `step_if <name> '<paths ERE>' …` in `ci.sh`: on a working branch in CI it is
  skipped, with one line saying why, when nothing under its inputs changed since the merge-base with `origin/development`. Be
  conservative: follow its imports out of its folder (`deno.json`, `go.mod`); when unsure, run it. `development`
  always runs the full gate; `check` and the whole-tree steps are never skipped.
- **No work done twice**: type-check once, in `check` (the tests run with `--no-check` in CI); build once.
- **Measure** the step's time in a CI run before and after, and say it in the commit body.
- **Heavy jobs** (image builds, browsers) are their own workflow or job (`image.yml`), not in the gate.
- **Secrets**: only `deploy.yml` reads any, and it does nothing without them; the gate needs none.

## Hooks

`git config core.hooksPath .githooks`, once per clone (they are engine-neutral: plain git).

- `commit-msg`: the subject is a Conventional Commit and the message has no attribution trailer.
- `pre-commit`: refuses credential files, secret patterns, personal data (home paths, e-mail addresses), and runs
  `ci.sh --quick` when code changed.
- `pre-push`: the subjects about to leave, and gitleaks on them.
- Deliberate bypass: `--no-verify`; CI still checks what it can.
