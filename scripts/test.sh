#!/usr/bin/env bash
# test.sh — the tests of the repository's own tooling (`deno task test`): every *_test.ts (the commit-subject check, the
# personal-data check, the skills check, get.sh against a release served on 127.0.0.1). The Go code
# has its own tests, in scripts/go.sh. They reach no network but localhost and run no program but git, sh and tar; the
# few that write do it in the temp folder, and the skills check makes symlinks, which Deno allows only with unscoped
# write access.
# Extra arguments go to `deno test` (e.g. --filter personal).
set -euo pipefail
cd "$(dirname "$0")/.."

mapfile -t files < <(git ls-files --cached --others --exclude-standard '*_test.ts')
check=()
[[ "${CI:-}" == true ]] && check=(--no-check) # the gate's check step already type-checked every file
exec deno test --quiet --parallel --allow-read --allow-write --allow-env --allow-run=git,deno,sh,tar \
  --allow-net=127.0.0.1 \
  "${check[@]}" "$@" "${files[@]}"
