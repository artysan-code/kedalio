## What and why

<!-- What this changes and the problem it solves. Link the issue if there is one. -->

## Checks

- [ ] `go test ./...` and `scripts/ci.sh --quick` pass (exit codes read, not the last line)
- [ ] A bug fix has the test that would have caught it; a new pure function has a `_test.go` beside it
- [ ] A user-visible change adds a line under `## Unreleased` in `CHANGELOG.md`
- [ ] A change to `protocol/` tool names or arguments updates `plugin/skills/` in the same commit
- [ ] No migration drops, renames or retypes anything
- [ ] Nothing personal (names, hostnames, e-mails, home paths) and no secrets in the diff

See [CONTRIBUTING.md](../CONTRIBUTING.md).
