---
name: kedalio-merge
description: How a change gets to `dev` in Kedalio, how to read CI's verdict, and when it is done. Use when about to say "done"/"fatto", commit or push to dev or a branch, check or wait for CI, merge or fast-forward into dev, or find the main folder busy on another session's branch. Italian triggers - "push su dev", "mergia", "è verde?", "integra", "la CI è rossa?", "possiamo chiudere?".
---

# To `dev`

The rule is in AGENTS.md («Verify before saying done»); these are the steps. `dev` is what runs: a green push to it
builds the image and redeploys the instance (ci.yml: gate, then images, then deploy, no batching).

1. **Locally**: `scripts/ci.sh --quick` (the pre-commit hook runs it) and `go test ./...`; `scripts/test.sh` when
   `scripts/` changed. Read the **exit codes** (`echo $?` or `|| echo FAILED`), never the last line of output: a `tail`
   or a filter hides a failure. The full gate runs only in CI.
2. **A small change by a single session** (a fix, a docs change, a contained feature): commit on `dev` and push it.
   The `dev` run is the gate and the deploy. If the run goes red, fix forward at once, or revert: the instance keeps
   the last image that passed, nothing red is deployed.
3. **Parallel agents, or a risky change** (migrations, sign-in, the connector's protocol, the workflows): push a
   branch (`feat/…`, `fix/…`, `docs/…`, `chore/…`, `refactor/…`, `ci/…`; agents under Kedalio push only
   `kedalio/<run-id>`). CI runs the full gate on it; its verdict is the branch's own run, shown on the commit:
   `gh run list --branch <branch> --limit 1`, then `gh run watch <id> --exit-status` (in the background) and
   `gh run view <id> --log-failed` for a red one. Merge only a green branch, in a worktree of your own (never in a folder
   where another session has its branch checked out): merge `origin/dev` into your branch, push it, wait for its green
   run, then fast-forward `dev` to it (`--ff-only`) and push. The `dev` push runs the whole gate again: the branch's
   green run does not stand in for it. If `dev` moved after your run, merge it into the branch again and wait again.
4. **A push to `dev` is done** when its run is green through all three jobs: `gate`, `image` and `deploy`
   (`gh run view <id>` lists them). The gate can pass while the image fails, and then nothing is deployed. Watch the
   run with `gh run watch <id> --exit-status`, never a hand poll; do not declare it done before. A redeploy of the head
   without a push: `gh workflow run deploy.yml --ref dev`.
5. If something could not be verified, say so plainly: what was not run and why.

## Release

Only when the owner asks. `main` is the default branch and always the latest release; `dev` is development.

0. Run `deno run --allow-read --allow-write scripts/release.ts X.Y.Z` (Unreleased becomes the dated heading, the plugin
   versions follow), commit it to `dev` and wait for green CI; a tag without its CHANGELOG section fails the release.
1. Tag that green `dev` commit and push the tag: `git tag vX.Y.Z <sha> && git push origin vX.Y.Z`.
2. Wait for `release.yml` and the tag's image run to be green (`gh run watch <id> --exit-status`).
3. Fast-forward `main` to the tag: `git push origin vX.Y.Z^{commit}:refs/heads/main`. No workflow pushes `main`.

Asking the owner stays for force pushes, deleting remote branches, a release, a tag or a package, a deploy by hand,
deleting a deployed resource and changing the repository's visibility or settings (see AGENTS.md).
