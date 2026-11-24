# Contributing

Issues and pull requests are welcome. There is no SLA, and a change may be declined if it does not fit the project
([docs/adr/0006-public-release.md](docs/adr/0006-public-release.md)). For a larger change, open an issue first.

## Build and test

Requirements: Go 1.27, Deno 2.9 and git (shellcheck and gitleaks for the full gate in CI).

```sh
go build ./core/cmd/kedalio-core
go test ./...
scripts/ci.sh --quick   # format, lint, types, shell, commit subjects, Go vet and staticcheck, personal data
```

Run both checks and read their exit codes before you open a pull request. A new pure function gets a `_test.go` beside it,
and a bug fix gets the test that would have caught it.

## Rules

- Commit subjects are `<type>(<scope>)!: <description>`, at most 100 characters, for example
  `fix(memory): the recall cache sees writes from another connection`. A change to `protocol/` is `feat(protocol): ...`.
- Tool names and arguments in `protocol/` are a contract with the clients' skills: a change to them updates
  `plugin/skills/` in the same commit, and `protocol/skills_test.go` checks that the skills name only real tools.
- No migration drops or alters a table or column, and none is renumbered or removed: a database must start under every
  version.
- Nothing personal in the tree: no names, hostnames, e-mails or home paths; examples use `example.com` and `alice`.
- Secrets never go in a file, an argument, a fixture or a log.
- Comments say why. Standard library first; a new dependency needs its reason in the commit body.
- Enable the hooks with `git config core.hooksPath .githooks`.

[AGENTS.md](AGENTS.md) describes how the maintainer's agents work on the repository.
