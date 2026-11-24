# AGENTS.md — Kedalio

The owner's memory, tasks, projects and sign-in behind one MCP connector: one Go program and one encrypted SQLite
database. Open source ([0006](docs/adr/0006-public-release.md)), run by its owner for one person; the pieces fit as in
[docs/architecture.md](docs/architecture.md). Stack: Go (core),
SQLite; Deno + TypeScript only for `scripts/`. English in code, commits and docs.

## Checks

- `scripts/ci.sh --quick` and `go test ./...`: run locally, read the exit codes, not the last line; the full gate runs
  only in CI. They are the gate before "done".
- A new pure function gets a `*_test.go` beside it; a bug fix gets the test that would have caught it. Server changes are
  checked with a real request; migration or database-open changes by starting the new code on a database the old code made.
- Done = the `dev` run on GitHub Actions green through the gate, image and deploy jobs (`gh run watch <id> --exit-status`,
  in the background); no git notes. Steps: skill `kedalio-merge`. Not verified: say what.

## Without asking

- Commit to `dev` and push once the checks are green (the owner, 2026-10-20 and 2026-11-08): `dev` is what runs, the
  image `kedalio-core:dev` is built from it on every green push. A parallel agent or a risky change (migrations,
  sign-in, protocol) goes on a branch first and merges by fast-forward at green CI.
- No deploy by hand; the deployment is CI's. Migrations never drop, rename or retype anything (see below).
- Dependencies: standard library first, pinned in `go.mod`, a new one needs its reason in the commit body.

`main` is the default branch and always points at the latest release; `dev` is development. A release is a tag on a green
`dev` commit, then a fast-forward of `main` to it (skill `kedalio-merge`).

Production is always asked, whatever is written above.
Anything else that changes something outside this folder, or anything on production: ask. Especially: a force push,
deleting remote branches (local branches already merged are deleted at the end of a session), deleting a deployed resource (Coolify application, volume),
changing the repository's visibility, settings, environments or rulesets, deleting a release, a tag or a package. Agents running under Kedalio push only `kedalio/<run-id>`.

## Rules that bite

- No migration drops a table or column, renames one or changes a type, none is renumbered or removed: the owner's
  database must start under every version. Adding a column that is nullable or has a default (ADD COLUMN) is allowed.
- A change to `protocol/` tool names or arguments updates `plugin/skills/` in the same commit
  (`protocol/skills_test.go` checks tool names).
- Commits: `<type>(<scope>)!: <description>`, subject at most 100 characters, no attribution trailers. A change to
  `protocol/` is `feat(protocol): ...` so the clients' skills pick it up.
- Nothing personal in the repository: no names, clients, accounts, hostnames, home paths, e-mails; examples use
  `example.com` and `alice`. What belongs to an installation is data on its server.
- Secrets never in `.env`, a config file, a command-line argument, a fixture, a log or a tool result; the core reads
  `KEDALIO_CORE_TOKEN`, `BACKUP_S3_ACCESS_KEY`, `BACKUP_S3_SECRET_KEY` from the environment. Never `wrangler login`. Code that
  handles a secret masks it everywhere.
- `gofmt`, `go vet`, `staticcheck` are the gate's (`scripts/go.sh`); tooling TypeScript is Deno, strict. Comments say
  why; no speculative code.

## Where things are

- `core/` (the Go program), `protocol/` (MCP tool arguments, the contract with clients), `deploy/coolify/core.yaml`,
  `scripts/` (gate, hook checks, deploy steps).
- `.agents/skills/<name>/SKILL.md`, read by Claude Code through `.claude/skills/` symlinks: `kedalio-merge`,
  `kedalio-gate`; a skill that does not load: read the file. What a hook can enforce is a hook (`.githooks/`), never only a skill.

## Around this repository

`origin` is the only remote (GitHub). CI is `.github/workflows/` ([docs/ci.md](docs/ci.md)).

State, tasks and decisions live in the maintainer's Kedalio, not in this file.
