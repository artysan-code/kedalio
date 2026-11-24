#!/usr/bin/env bash
# The nightly upkeep: one `claude -p` run on the owner's subscription that does one job of `memory_upkeep`.
# It runs in the `upkeep` container (Dockerfile here, service in deploy/coolify/core.yaml), started by a scheduled
# task. It needs KEDALIO_URL (the core's base URL), KEDALIO_UPKEEP_TOKEN and a Claude sign-in (CLAUDE_CODE_OAUTH_TOKEN,
# or a signed-in Claude Code) in the environment, never on the command line; jq and timeout on the PATH. Claude Code
# gets none of its own tools (`--tools ""`): it can only call the core.
#
# The subscription's pace is only visible inside a run (the stream-json `rate_limit_event`), so a one-turn probe reads
# it first and the job is skipped when the five-hour or the weekly window is already busy.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
max_5h=${UPKEEP_MAX_5H:-0.5}
max_7d=${UPKEEP_MAX_7D:-0.8}
budget=${UPKEEP_BUDGET_USD:-1}
model=${UPKEEP_MODEL:-haiku}
runs=${UPKEEP_RUNS:-5}
# the scheduled task's time limit in seconds, and the longest a run may take (the `timeout` below)
window=${UPKEEP_WINDOW:-2400}
run_max=700
margin=60

: "${KEDALIO_URL:?KEDALIO_URL is not set}"
: "${KEDALIO_UPKEEP_TOKEN:?KEDALIO_UPKEEP_TOKEN is not set}"

# One window's utilization (0–1) from a stream-json run; empty when the event or the window is missing.
utilization() {
  jq -rR --arg w "$1" 'fromjson? | select(.type == "rate_limit_event")
    | .rate_limit_info.unifiedWindows[$w].utilization // empty' | tail -n 1
}

probe=$(timeout 120 claude -p "Answer: ok" --model "$model" --output-format stream-json --verbose \
  --tools "" --strict-mcp-config --permission-mode dontAsk 2>&1 || true)
if ! jq -eR 'fromjson? | select(.type == "result") | .is_error == false' <<<"$probe" >/dev/null; then
  echo "upkeep: the probe run failed (signed in to Claude? CLAUDE_CODE_OAUTH_TOKEN set?)" >&2
  exit 1
fi
u5=$(utilization five_hour <<<"$probe")
u7=$(utilization seven_day <<<"$probe")
busy() { [ -n "$1" ] && awk -v u="$1" -v m="$2" 'BEGIN { exit !(u >= m) }'; }
if busy "$u5" "$max_5h" || busy "$u7" "$max_7d"; then
  echo "upkeep: skipped, subscription busy (5h ${u5:-?}, 7d ${u7:-?})"
  exit 0
fi

# Each run does one job, the one run longest ago with something due (the core picks it), so a few runs a night reach
# every job instead of the inbox alone. A failed run stops the night. Five runs a night: the hubs and logs queues are long, and a run costs about 0.012 USD.
# A run is capped at 700 s (and by the budget above); none starts unless it could end, at its longest, inside the
# scheduled task's window (SECONDS counts from the script's start).
for _ in $(seq "$runs"); do
  if [ $((SECONDS + run_max + margin)) -gt "$window" ]; then
    echo "upkeep: stopped for time, ${SECONDS} s used of ${window} s"
    break
  fi
  line=$(timeout "$run_max" claude -p "$(cat "$here/nightly.md")" --model "$model" --tools "" \
    --strict-mcp-config --mcp-config "$here/mcp.json" \
    --allowedTools "mcp__kedalio" --permission-mode dontAsk \
    --max-budget-usd "$budget" --output-format json |
    jq -r 'if .is_error then "upkeep: failed: \(.result // .terminal_reason // "?")"
      else "upkeep: \(.subtype // "done"), turns \(.num_turns // "?"), $\(.total_cost_usd // "?")" end') || line="upkeep: failed: the run did not finish"
  echo "$line"
  case $line in "upkeep: failed"*) exit 1 ;; esac
done
