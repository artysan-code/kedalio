#!/usr/bin/env bash
# ci.sh — the whole gate, run by CI on every push of dev and of a working branch. A
# workstation runs only --quick (the pre-commit hook); the full gate is CI's.
#
#   scripts/ci.sh          check, commit subjects, tests, secret scan
#   scripts/ci.sh --fast   the same without the secret scan
#   scripts/ci.sh --quick  only the cheap steps: check (fmt, lint, type-check, shellcheck), Go's format and vet, and
#                          commit subjects
# The Go code (go.mod at the root) has its own steps: go_check (gofmt, vet, staticcheck) and, in the full gate, go_test
# (tests) and, on dev, go_vuln (govulncheck), all in scripts/go.sh.
#
# The steps are independent: they run at once, each into its own log. Each prints one line with its time as soon as
# it ends, and a failed one its whole log (CI_VERBOSE=1: every log); the gate fails when any fails, and names it.
# A step that runs past CI_STEP_TIMEOUT seconds (default 600) is stopped and fails with what it logged so far: a hung
# test then names itself, before the runner's own deadline kills the job with no verdict and no log.
# The secret scan reads the history of the commit under test, not every branch fetched: a finding on another branch
# fails that branch's own run, not this one.
# gitleaks is optional on a workstation (skipped with a warning) and required in CI (CI=true).
# The runner is CPU-starved and the steps share it with production, so on a working branch in CI a step whose inputs
# did not change since the merge-base with origin/dev is skipped, and says so. `dev` runs the full gate, and
# so does everything else (a workstation, no merge-base to compute).
# check, commits, personal, placeholders and secrets are never skipped: their inputs are the whole tree (or the history).
set -euo pipefail
cd "$(dirname "$0")/.."

fast=0 quick=0
[[ "${1:-}" == --fast ]] && fast=1
[[ "${1:-}" == --quick ]] && quick=1

secrets() {
  if command -v gitleaks >/dev/null; then
    gitleaks git --no-banner --redact --config .gitleaks.toml --log-opts=HEAD .
  elif [[ "${CI:-}" == true ]]; then
    echo "ci: gitleaks is required in CI" >&2
    return 1
  else
    echo "ci: gitleaks is not installed, secret scan skipped" >&2
  fi
}

export -f secrets
step_timeout=${CI_STEP_TIMEOUT:-600}
names=() pids=()
logs=$(mktemp -d)
trap 'rm -rf "$logs"' EXIT
step() {
  local name=$1
  shift
  names+=("$name")
  local n=${#names[@]}
  (
    start=$SECONDS
    rc=0
    # through bash, so a step can be a function of this script (secrets, exported below)
    timeout -k 10 "$step_timeout" bash -c '"$@"' _ "$@" || rc=$?
    echo $((SECONDS - start)) >"$logs/$n.time"
    ((rc == 124 || rc == 137)) && echo "ci: ${name} stopped after ${step_timeout}s"
    exit "$rc"
  ) >"$logs/$n" 2>&1 &
  pids+=($!)
}

# Files changed since the merge-base with origin/dev, only on a CI run of a working branch; empty (so nothing is
# skipped) in every other case or when the merge-base cannot be computed.
changed=""
skipping=0
if [[ "${CI:-}" == true ]]; then
  ref=${GITHUB_REF:-}
  if [[ -n "$ref" && "$ref" != refs/heads/dev ]] &&
    base=$(git merge-base HEAD origin/dev 2>/dev/null) && changed=$(git diff --name-only "$base" HEAD 2>/dev/null); then
    skipping=1
  fi
fi

# step_if <name> <inputs: an ERE on the changed files' paths> <command>...: the step, unless it is skipping and none
# of its inputs changed. Root files every step reads (the lockfiles, the formatter's config) and the gate itself count
# as inputs of all of them.
step_if() {
  local name=$1 inputs=$2
  shift 2
  if ((skipping)) && ! grep -qE "^(${inputs}|deno\.(json|lock)|scripts/ci\.sh|\.github/)" <<<"$changed"; then
    echo "ci: ${name} skipped, no change under ${inputs//|/, } since the merge-base with origin/dev"
    return
  fi
  step "$name" "$@"
}

step check bash scripts/check.sh
step_if go_check '.*\.go|go\.(mod|sum)|scripts/go\.sh' bash scripts/go.sh check
step commits deno run --quiet --allow-read --allow-run=git scripts/commits.ts
step personal deno run --quiet --allow-read --allow-run=git scripts/personal.ts --tree
step placeholders deno run --quiet --allow-read --allow-run=git scripts/placeholders.ts
# skills reads .agents/ and .claude/ (the symlinks); scripts/ holds its code
step_if skills '\.agents/|\.claude/|plugin/|scripts/' deno run --quiet --allow-read scripts/skills.ts
if ((!quick)); then
  # inputs: what each step reads. The tests read the tooling (scripts/) and deno.json; never docs/, *.md
  # at the root or the deploy files, so a docs-only branch skips them.
  step_if test 'scripts/|get\.sh' bash scripts/test.sh
  step_if go_test '.*\.go|go\.(mod|sum)|scripts/go\.sh' bash scripts/go.sh test
  # govulncheck reads the vulnerability database over the network and a new advisory changes its answer with no change of
  # ours, so it runs on dev only, and only a vulnerability that affects the code fails it (scripts/go.sh)
  [[ "${CI:-}" == true && "${GITHUB_REF:-}" == refs/heads/dev ]] &&
    step go_vuln bash scripts/go.sh vuln
  ((fast)) || step secrets secrets
fi

failed=()
for _ in "${pids[@]}"; do
  rc=0
  wait -n -p pid || rc=$?
  for i in "${!pids[@]}"; do [[ "${pids[$i]}" == "$pid" ]] && break; done
  n=$((i + 1))
  if ((rc == 0)); then
    printf '\033[32m✓\033[0m %-10s %3ss\n' "${names[$i]}" "$(cat "$logs/$n.time" 2>/dev/null)"
    [[ "${CI_VERBOSE:-}" == 1 ]] && cat "$logs/$n"
  else
    printf '\033[31m✗\033[0m %-10s %3ss\n' "${names[$i]}" "$(cat "$logs/$n.time" 2>/dev/null)"
    sed 's/^/    /' "$logs/$n"
    failed+=("${names[$i]}")
  fi
done

if ((${#failed[@]})); then
  printf '\033[31mci failed: %s\033[0m\n' "${failed[*]}" >&2
  exit 1
fi
printf '\033[32mci ok\033[0m\n'
