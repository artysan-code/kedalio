# 0006 — Kedalio is public open source

- Status: accepted
- Date: 2026-11-23

## Decision

Kedalio is published as open source under the GNU Affero General Public License, version 3 or (at your option) any later version (AGPL-3.0-or-later; `LICENSE` is the
unmodified text, with no extra terms). This supersedes the line of [0005](0005-memory-and-tasks.md) that calls it
"a personal tool, not a product": the code is for anyone to run, read and change. What 0005 cut stays cut.

- **One owner per instance.** An instance serves one person and the many clients that person uses. It is not
  multi-tenant, has no accounts and no sharing between people; whoever needs two owners runs two instances. The threat
  model in [architecture.md](../architecture.md) assumes this.
- **English by default.** A new database is English unless `-language Italian` is given when it is made; the language and
  the time zone then belong to the database.
- **A fresh root.** The public repository starts from one new commit of the current tree. The earlier history, its tags
  and its issues stay private; nothing in the tree points at them.
- **Support.** Issues and pull requests are welcome and read when there is time. There is no SLA, no roadmap promise and
  no guarantee that a change is accepted. A security report goes privately, as [SECURITY.md](../../SECURITY.md) says.

## Consequences

- The README, the compose file, the release binaries and the security and contributing notes are written for a stranger
  who knows nothing of the author's server.
- The deployment example for Coolify stays as one example among others; no deployment is the project's own.
- The rule that no migration drops or alters a table or column stands: published databases must start under every
  version.
