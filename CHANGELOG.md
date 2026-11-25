# Changelog

What changed for people who run Kedalio, newest first. Versions are SemVer, X.Y.Z: X breaks users, Y adds, Z fixes. A
user-visible change adds a line under Unreleased as it lands; `scripts/release.ts` turns Unreleased into a release.

## Unreleased

## 1.0.1 (2026-11-25)

### Added

- Release archives and the container image carry a SLSA build provenance attestation; check one with
  `gh attestation verify <file> --repo artysan-code/kedalio`.

### Changed

- `get.sh --server` now warns that the client-address header is off and says how to turn it on behind a reverse
  proxy or tunnel; `compose.yaml` carries a commented S3 offsite backup example.
- README and SECURITY state that Kedalio has no telemetry and list its only outgoing calls (offsite S3 backup,
  embeddings server, fetching a document URL); `get.sh --uninstall` is documented.

### Fixed

- stdio mode handles the requests on one stream in the order they arrive, so a read sent right after a write sees
  it and answers no longer come back out of order; cancellations and the client's answers still go through at once.
- `kedalio-restore` now says when `KEDALIO_RECOVERY_CODE` is set but not a valid recovery code, instead of reporting it
  as missing.

## 1.0.0 (2026-11-24)

The first public release, under the GNU Affero General Public License, version 3 or (at your option) any later version.

### Added

- Memory: Markdown pages with revisions and links, collections of documents, and search by words or, with an embeddings
  server, by meaning. An optional nightly upkeep job files the inbox, flags conflicts and duplicates and writes week
  summaries through `memory_upkeep`.
- Tasks with refs, steps, repeats, relations and saved views; closing a task leaves a line in the diary.
- Projects: which folder and repository belong to which project, with its settings.
- One MCP endpoint at `/mcp` (Streamable HTTP) and a stdio mode. Remote clients such as claude.ai sign in with OAuth, each
  new client approved with a short code; other clients can use a static bearer token. `kedalio-core clients list` and `revoke` manage OAuth clients.
- Claude Code plugins (`.claude-plugin/marketplace.json`) with session-start and session-end skills, for a local
  (stdio) or a server connection.
- Encrypted backups to a folder and, optionally, to an S3-compatible store, with signed tags. `kedalio-restore` refuses
  a tampered, foreign or older copy, or a copy with no tag, unless `-allow-older` or `-allow-unsigned` is given. It reads
  the recovery code from `KEDALIO_RECOVERY_CODE`.
- Packaging: a container image, `compose.yaml` (`KEDALIO_IMAGE` and `KEDALIO_PORT` set the image and the host port), an
  example for Coolify, and release binaries of `kedalio-core` and `kedalio-restore` for Linux, macOS and Windows on
  amd64 and arm64, with `SHA256SUMS`.
- `KEDALIO_CORE_BACKUP_KEEP` sets how many local copies are kept.
- At startup the core warns when `-public-url` is set without `-client-ip-header`, or `-client-ip-header` without
  `-trusted-proxies`; neither stops it.
- Approving a client: a new client's sign-in page shows a code such as `4F7K-92QD`; you approve it with
  `kedalio-core approve <code>` on the server, which shows the client, its kind and redirect host and the requesting
  address and asks first ([ADR 0007](docs/adr/0007-approval-codes.md)). An approval authorizes that one client and
  gives the browser no session. The MCP tool `clients_approve` approves the code of a local client from a client you
  already connected (two calls, the second with `confirm`); cloud clients and passkeys are approved only on the
  server. Codes live ten minutes, work once and are stored hashed.
- A passkey is optional. It is made at `/setup` with an approval code, and there may be more than one. A browser signed
  in with a passkey connects clients with one click. `kedalio-core passkeys list | remove` show and remove passkeys,
  and the core logs each passkey it adds.
- A waiting request is limited to 3 per address and 2 for passkeys; `kedalio-core approve -list` and `-deny-all` show
  and refuse them. `clients_approve` approves only a request that came from the caller's own address and needs the
  redirect host repeated. Set `KEDALIO_CORE_CLIENT_IP_HEADER` behind a proxy or the address checks mean nothing.
- Language and time zone belong to the database: English by default, Italian as an option, and the host's time zone.
- Tool answers are compact: lists are short and say when there is more, `memory_read` and `tasks_get` default to 4000
  characters, and `tasks_save` answers only what changed. A tool error that is not the caller's fault is logged by the
  core, and the client receives "the call failed inside the core".
- A value in `KEDALIO_CORE_BACKUP_EVERY`, `KEDALIO_CORE_BACKUP_KEEP` or `KEDALIO_CORE_BACKUP_S3_KEEP` that cannot be
  parsed stops the start with an error naming the variable. `/health` shows the backup state (`waiting`, `ok`, `stale`
  or `failing`) only with the owner's token.
- `<data>/first-run.txt` holds the recovery code, which stays until you delete the file.
- The consent page shows the redirect's scheme, host and port, and marks the client's name as self-declared.
- `-embed-url` accepts a trailing slash.
- The container runs read-only (only `/data` and `/tmp` are writable), with no capabilities and `no-new-privileges`.

### Security

- The static token must be at least 32 characters. Failed bearer attempts are rate limited (a valid token always
  passes) and IPv6 clients are keyed by /64. `-client-ip-header` with `-trusted-proxies` sets which header carries
  the client's address and which proxies may supply it.
- The recovery code goes to `<data>/first-run.txt` (mode 0600), never to the log.
- Request bodies are capped at 8 MB, with per-request deadlines instead of server-wide timeouts.
- The core refuses to start on a database migrated by a newer Kedalio.
- The memory refuses a write that adds a secret (tokens of known shapes, credential pairs, Bearer values).
  `kedalio-core revisions redact` removes secrets from stored revisions.
- Fetching a document by address refuses private and special-purpose ranges. Workbooks and PDFs are capped (a PDF at
  2000 pages and 8 MiB of text; the rest is left unindexed without an error).
- An S3 endpoint over `http://` is refused unless it is localhost; a restore download fails if the store sends more
  than it listed.
- Client names lose control characters when registered and listed, and the redirect-mismatch warning is truncated and
  logged at most once a minute.
- Each backup carries an HMAC-signed tag (hash, size, time, migrations, instance fingerprint).
