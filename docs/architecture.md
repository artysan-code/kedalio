# Architecture

Kedalio is one Go program, `core/cmd/kedalio-core`, and one encrypted SQLite database. It gives its owner's agents a
memory, a task list and a list of projects through one MCP connector. Nothing else runs: there is no app and no second
service. The decisions are [0005](adr/0005-memory-and-tasks.md) and [0006](adr/0006-public-release.md); the older ADRs
are kept as history.

## The pieces

| Folder          | What it is                                                                                                              |
| --------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `core/memory`   | Markdown pages with revisions, links, collections of documents, search by words and by meaning (`search/`).             |
| `core/tasks`    | Tasks with refs, steps, repeats, relations and saved views; closing one leaves a line in the diary.                     |
| `core/projects` | Which folder and which repository belong to which project, and the project's settings.                                  |
| `core/mcp`      | The connector: the tools below, over Streamable HTTP at `/mcp`, stateless; `/health`.                                   |
| `core/identity` | OAuth for MCP clients (discovery, registration, token) and the approval of new clients by code (a passkey is optional). |
| `core/instance` | Opens the database, runs the migrations, wires the above, runs the backups.                                             |
| `core/db`       | SQLite (pure Go) encrypted at rest, migrations, consistent copies.                                                      |
| `core/vault`    | The instance key, its recovery code and the database key; hides known secret values in answers.                         |
| `core/offsite`  | Sends each backup copy to an S3-compatible store; `kedalio-restore` brings one back.                                    |
| `protocol/`     | The arguments of the tools: the contract with the connector's clients.                                                  |
| `plugin/`       | The Claude Code plugin's skills (`.claude-plugin/marketplace.json` lists two plugins, local and server).                |
| `core/image/`   | The container image. `compose.yaml` runs it anywhere; `deploy/coolify/core.yaml` is one example for Coolify.            |

## The plugin

`.claude-plugin/marketplace.json` publishes two Claude Code plugins that share the skills in `plugin/skills/`:
`kedalio-local` runs `kedalio-core stdio` on the same computer (no token, no port) and `kedalio-server` connects to a
server's `/mcp` with OAuth. The skills name the tools below, so a change to a tool's name or arguments in `protocol/`
updates them in the same commit; `protocol/skills_test.go` checks the names.

## The tools

`memory_recall`, `memory_read`, `memory_list`, `memory_write`, `memory_delete`, `memory_sources`, `tasks_brief`,
`tasks_list`, `tasks_get`, `tasks_save`, `session_start`, `projects_link`, `settings_get`, `settings_set`, and
`memory_upkeep` (loaded only when asked for). Tool schemas are flat at the root (an operation choice is an `op` enum),
and a wrong argument is answered as `{errors: [{field, problem, fix}]}`.

## Upkeep

The core does the upkeep that needs no model; a nightly `claude -p` job does the rest through `memory_upkeep`.

- In the core: closures of tasks within 30 minutes share one diary line; the embedding lane, when idle, flags lines
  that look like the same fact with a different value (`fact_conflicts`); pages close in meaning or title are
  duplicate candidates; `start` reshapes up to 50 tasks to the current file shape.
- `memory_upkeep`: `worklist` (what is due per job: inbox, conflicts, duplicates, hubs, logs, weeks), `start` and
  `end` of a run (one at a time, open at most two hours; `session_start` shows the last), `dismiss` for a rejected
  candidate, `inbox_placed` to take inbox entries out once they live on other pages.
- The job uses its own token, `KEDALIO_UPKEEP_TOKEN` (at least 32 characters), and writes as `upkeep`. That author may
  call only the reading tools, `memory_write`, `memory_delete` of pages and `memory_upkeep`, may not restore, and may
  not touch task pages, the inbox or diary days directly. Every sentence a page loses must stand on the page itself or
  on the pages named in `kept_in` (never the page being changed), or the write is refused.
- Marking diary days as summed up by a week page is behind a switch, off until measured on the recall eval.

## Cloud clients

An OAuth client registered with a redirect off this machine (claude.ai's connector, for its chats, Projects and cloud
Claude Code sessions; ChatGPT's) is a cloud client: `identity.Auth.ClientOf` says so from the registered redirects,
since a code only ever goes back to one of them, while the client's name is its own choice. A client registered only
with loopback redirects (Claude Code, the CLI plugins) is the owner, as is the static token. Where a cloud client runs
no local hook guards what it writes, so the server holds a fixed policy (`core/mcp/cloud.go`), in the same `policy`
struct as the upkeep's (`core/mcp/policy.go`):

- it may call the reading tools, `tasks_save` and `memory_write`, and its `tools/list` shows only those (a second SDK
  server, `getServer` picks it); the others are refused all the same if called by name;
- `tasks_save`: everything but `repeat` and `worked_by`;
- `memory_write`: only `append` to the inbox and `remember`, which always lands in the inbox, whatever page it names.
  Each entry carries the line `why: cloud: from <client>, waits for the owner`; a line of the client's own starting
  `why:` is refused, so the mark cannot be pushed out;
- the nightly upkeep's inbox worklist skips those entries and `inbox_placed` refuses them to the upkeep token: they wait
  for the owner, and `session_start` counts them apart (`inbox.from_cloud`).

There is no cap on how many cloud clients register: adding the connector takes the owner's approval of its code.

## Secrets

The memory never keeps a secret: a write whose text the mask would change (a vault value, a token of a known shape, a
Bearer credential, a password in an address) is refused, and so is one that brings a credential-like `name = value` pair
(`vault.SecretPairs`, read as prose: `auth: OAuth` and `` token: `KEDALIO_CORE_TOKEN` `` pass, `password: hunter2` does
not). Only pairs the write brings count, so a page that already holds one stays editable. `tasks_save` checks its text
fields the same way. `memory_list what=health` lists the pages and older versions that look secret, by path only;
what to do with them is the owner's call.

## Signing in

A client reaches `/mcp` with a bearer token: the static `KEDALIO_CORE_TOKEN`, the upkeep job's `KEDALIO_UPKEEP_TOKEN`
(see Upkeep), or an access token the core issued through OAuth. With `-public-url` set the core is also an OAuth
authorization server: a client registers itself, sends the owner to `/oauth/authorize`, the page shows a short
code, the owner approves it with `kedalio-core approve` (or, for a local client, the `clients_approve` tool), and the
client trades its code at `/oauth/token`. A browser with a passkey session sees the consent page instead and agrees in one
click. A passkey is optional and made at `/setup` with an approval code. Pending approvals are rows in the `approvals`
table, which is how the command, a separate process, reaches the running core (ADR 0007). The pages are Go templates in
`core/identity`; they need nothing else served. A passkey proves who the owner is; it does not unlock anything. A code does not prove who started the request: see SECURITY.md.

## The database key

The database is encrypted with a key derived from the instance key, a file in the data folder (`<data>/kek`). The first
start makes it and writes the recovery code once to `<data>/first-run.txt` (mode 0600). A data folder that has the
database but no key file, a restored one for example, starts locked: the core answers `/health` (`{"status":"locked"}`)
and `POST /unlock` with `{"code": "<recovery code>"}` only, and opens when the code arrives. Without the file or the
code the database and its backups cannot be read.

## Backups

`-backup-dir` keeps still-encrypted copies of the database (`-backup-every`, `-backup-keep`). With
`-backup-s3-endpoint` each copy also goes to an S3-compatible store, using the environment variables
`BACKUP_S3_ACCESS_KEY` and `BACKUP_S3_SECRET_KEY`.

`/health` always answers `status` as `ok`, or `locked` while the core waits for the recovery code. With the owner's
token it also shows `offsite_backup`, one of:

- `waiting`: no copy has reached the store yet;
- `ok`: a recent copy is on the store;
- `stale`: the last upload worked, but copies stopped arriving (the newest is older than two backup intervals);
- `failing`: the store refuses the uploads.

### Backup authentication and its threat model

Next to each copy sits `core-<time>.db.tag`: the copy's SHA-256, size, creation time, the migration versions inside it
and a fingerprint of the instance, under an HMAC-SHA256. The HMAC key is derived (HKDF) from the database key, which
is itself derived from the instance key: the raw key is never used, and the owner keeps no new secret. The tag goes to
the store beside the copy, before it, and is rotated with it. `kedalio-restore` needs the recovery code
(`KEDALIO_RECOVERY_CODE`, in the environment) and refuses a copy whose tag fails, whose name carries another time than
the signed one, or that belongs to another instance. It also refuses a copy older than the newest one the store holds
a valid tag for, unless `-allow-older` is given, and a copy with no tag (one made before tags existed) unless
`-allow-unsigned` is given, with a warning.

Protected: confidentiality (Adiantum) and integrity of copies: a byte changed, a copy truncated, renamed to look
newer, taken from another instance, and a rollback to an older copy while the newer one and its tag are still in the
store.

Not protected: anyone who holds the instance key can forge tags, and the `kek` file lies beside `core.db` by default,
so a stolen volume gives both and confidentiality and integrity both fall. Keep the backups away from the data
folder. An attacker who deletes the newest copy and its tag from the store leaves an older, valid pair that nothing
can tell from the newest: rollback detection works from what the store still holds, with no state outside it. An
unsigned copy is as trusted as the store it came from. A tag left in the store with no copy newer than the real ones
(a crash between the tag and the copy upload) counts as a newer copy: restoring the newest copy then needs
`-allow-older` until the next backup. The tag authenticates the file, not the owner's data: a bad import made by the
core itself is signed as well.

## Migrations

Each package that owns tables migrates itself under its own name in `schema_versions` (`core/db`), in order, appending
steps and never editing them. Nothing drops a table or column, renames one or changes a type; adding a nullable column
or one with a default is allowed. A database made by a newer Kedalio is refused at start: upgrade the binary.
Tables of features that no longer exist can remain in an older database, unused.

## Decisions it keeps

- Flat input schemas: no top-level `oneOf`/`anyOf`, which the Claude API has rejected.
- Writes are recorded under the author who made them (`oauth:<client>` for an OAuth client), and the memory's writing
  rules (`core/mcp/rules.go`) refuse what an agent should not write without asking.
