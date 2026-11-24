#!/usr/bin/env bash
# check.sh — the static gate: formatting, lint and type-check of the repository's TypeScript (its own tooling in
# scripts/), and the shell scripts (bash -n, shellcheck). Run by `deno task check`, the pre-commit hook (through ci.sh --fast) and
# scripts/ci.sh.
# The shellcheck step is optional on a workstation (skipped with a warning) and required in CI (CI=true).
set -euo pipefail
cd "$(dirname "$0")/.."

# a merge left half done: a conflict's markers at the start of a line in any tracked file (once committed to docs/go.md,
# where nothing else would have read them)
if git grep -n -I -E '^(<{7} |>{7} |={7}$)' -- . ':!:*.lock'; then
  echo "check: conflict markers above" >&2
  exit 1
fi

deno fmt --check --quiet
deno lint --quiet
mapfile -t ts < <(git ls-files --cached --others --exclude-standard '*.ts')
deno check --quiet "${ts[@]}"

# Every bash script, found by its shebang: a script added below is checked unedited.
shell=()
for f in get.sh scripts/*.sh .githooks/* deploy/*.sh; do
  [[ -f "$f" ]] || continue
  head -n1 "$f" | grep -Eq '^#!.*(bash|sh)$' || continue
  bash -n "$f"
  shell+=("$f")
done
if command -v shellcheck >/dev/null; then
  shellcheck -S warning "${shell[@]}"
elif [[ "${CI:-}" == true ]]; then
  echo "check: shellcheck is required in CI" >&2
  exit 1
else
  echo "check: shellcheck is not installed, skipped" >&2
fi
