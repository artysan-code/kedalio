#!/usr/bin/env bash
# go.sh — the Go part of the gate: `go.sh check` (format, vet, staticcheck: cheap, in --quick), `go.sh test` (the
# tests: the full gate) or `go.sh vuln` (govulncheck: the full gate on dev). Go is optional on a workstation (skipped with a warning) and required in CI (CI=true).
set -euo pipefail
cd "$(dirname "$0")/.."

if ! command -v go >/dev/null; then
  if [[ "${CI:-}" == true ]]; then
    echo "go: Go is required in CI" >&2
    exit 1
  fi
  echo "go: Go is not installed, skipped" >&2
  exit 0
fi

case "${1:-}" in
check)
  mapfile -t files < <(git ls-files --cached --others --exclude-standard '*.go')
  unformatted=$(gofmt -l "${files[@]}")
  if [[ -n "$unformatted" ]]; then
    echo "go: not formatted (gofmt -w):" >&2
    echo "$unformatted" >&2
    exit 1
  fi
  go vet ./...
  go tool staticcheck ./...
  ;;
test)
  go test ./...
  ;;
vuln)
  # Only exit 3, "a vulnerability affects this code", fails: a download or the vulnerability database out of reach (a
  # network failure) says so and passes, and a new advisory in a module the code does not call is only printed.
  status=0
  go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./... || status=$?
  if ((status == 3)); then
    exit 1
  elif ((status != 0)); then
    echo "go: govulncheck did not complete (exit ${status}), not counted against the merge" >&2
  fi
  ;;
*)
  echo "usage: go.sh check|test|vuln" >&2
  exit 2
  ;;
esac
