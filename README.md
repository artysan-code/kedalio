# Kedalio

Kedalio is a personal memory and task list for your AI assistants. You host it yourself: one Go program and one
encrypted SQLite database, behind one MCP connector. Claude, Codex, ChatGPT and any other MCP client read and write the
same notes, tasks and projects. You connect each client by approving a short code; a passkey is optional.

It serves one owner (you) and the clients you use. It has no team features and there is no hosted service: you run it on
your laptop, on a VPS or at home, and you keep the data. Why it is shaped like this:
[docs/architecture.md](docs/architecture.md) and [docs/adr/](docs/adr/README.md).

## Install with one prompt

Paste this into Claude Code:

> Install Kedalio for me. Download https://kedalio.com/install.md with curl (do not summarize it with a web tool, the
> commands must be exact), read it, and follow it. Ask me before changing anything on another machine.

Claude asks whether Kedalio should keep your data on this computer or on a server, installs the core, connects Claude
Code and checks that it works. The guide it follows is [docs/install.md](docs/install.md). It never reads your
recovery code or token; you copy those yourself.

## Install with one line

On this computer (Linux or macOS):

```sh
curl -fsSL https://kedalio.com/get.sh | sh
```

It downloads the release for your system, checks it against the published checksums, puts `kedalio-core` and
`kedalio-restore` in `~/.local/bin` (no sudo) and, if Claude Code is installed, offers its plugin. To read the script
before it runs, download it, read it, then run it:

```sh
curl -fsSL https://kedalio.com/get.sh -o get.sh
less get.sh
sh get.sh
```

On a server with Docker Compose, run it there with `sh get.sh --server https://kedalio.example.com`; it sets up
`~/kedalio` with a new token and starts nothing until you add `--start`. `sh get.sh --help` lists the rest.

The release downloads, the image and the plugin marketplace described below exist from the v1.0.0 release on. Before
it, build from a clone (Docker Compose with `--build`, or `go build`).

## Features

- Memory: Markdown pages with revisions and links, collections of documents, search by words and, with an embeddings
  server, by meaning.
- Tasks: refs, steps, repeats, relations and saved views; closing a task leaves a line in the day's diary.
- Projects: which folder and repository belong to which project, with the project's settings.
- One MCP endpoint (`/mcp`, Streamable HTTP) with flat, small tool schemas that answer a wrong argument with how to fix it.
- OAuth for remote MCP clients (claude.ai and others) with each new client approved by a short code, an optional passkey, or a static bearer token.
- Encrypted at rest; encrypted, HMAC-signed backups, optionally copied to an S3-compatible store.
- English by default, Italian as an option; one static binary, no C toolchain, a container image.

## Install by hand

Pick one. All three end at "Connect a client" below.

### Prerequisites

- Docker with Compose, for the container path; nothing for the released binary; Go (the version in `go.mod`) to build
  from source.
- `openssl` (or any other way to make 32 random bytes of hex) for the token of a server.
- For a server that claude.ai or a phone reaches: a public https address (a domain with a reverse proxy, or a tunnel,
  see [docs/claude-ai-connector.md](docs/claude-ai-connector.md)).
- Claude Code with its `claude` command, to follow the plugin path.

### a) Docker Compose on any machine or VPS

```sh
git clone https://github.com/artysan-code/kedalio.git && cd kedalio
echo "KEDALIO_CORE_TOKEN=$(openssl rand -hex 32)" > .env   # the bearer token: at least 32 characters, never committed
docker compose up -d
docker compose exec core cat /data/core/first-run.txt     # see "First run" below
curl -s "http://127.0.0.1:${KEDALIO_PORT:-8787}/health"   # {"status":"ok"}
```

[compose.yaml](compose.yaml) pulls `ghcr.io/artysan-code/kedalio-core:latest`, keeps the data in a named volume and
binds the port to `127.0.0.1` only. The container's file system is read-only except `/data` and `/tmp`, and it has no
capabilities.

- `KEDALIO_IMAGE` picks another image, for example a tag: `KEDALIO_IMAGE=ghcr.io/artysan-code/kedalio-core:1.0.0`. The
  `compose.yaml` attached to a release already has its own tag.
- `docker compose up --build -d` builds the image from the clone (`core/image/Dockerfile`) instead of pulling it.
- `KEDALIO_PORT` is the port on this machine (default 8787).
- Behind a proxy or tunnel, uncomment `KEDALIO_CORE_PUBLIC_URL`, `KEDALIO_CORE_CLIENT_IP_HEADER` and
  `KEDALIO_CORE_TRUSTED_PROXIES` in `compose.yaml` and set them in `.env`. Approval codes need the client-address header
  behind a proxy (see SECURITY.md).

### b) The single binary

Download the archive for your system from the [GitHub releases](https://github.com/artysan-code/kedalio/releases) page:
`kedalio-<os>-<arch>.tar.gz` (`.zip` on Windows) for linux, darwin or windows on amd64 or arm64, with `SHA256SUMS` to
check it. It holds `kedalio-core` and `kedalio-restore`. Then:

```sh
export KEDALIO_CORE_TOKEN=$(openssl rand -hex 32)   # keep it: clients use it
./kedalio-core -data ./data
```

It listens on `127.0.0.1:8787`, and the connector is at `/mcp`. Or build it: Go 1.27, `go build ./core/cmd/kedalio-core`.

### c) Local, no server

Use it from Claude Code or Claude Desktop on your own computer, with nothing exposed: Claude starts `kedalio-core stdio`
itself. [Run it locally (Claude Code / Claude Desktop, no server)](docs/local.md).

## Connect a client

### Claude Code, with the plugin

The plugin gives Claude Code the connection and the skills that teach it when to recall, save and close tasks. It is
in the marketplace `kedalio` of this repository, in two flavours; install one.

```sh
claude plugin marketplace add artysan-code/kedalio
# the core runs on this computer (stdio): no token, no port
claude plugin install kedalio-local@kedalio --config core_path=/home/alice/.local/bin/kedalio-core --config language=English
# or the core runs on a server with a public https address: approve a code, no token on this computer
claude plugin install kedalio-server@kedalio --config server_url=https://kedalio.example.com/mcp
```

Then `claude mcp list` shows the server as `Connected` (`kedalio-local`) or `Needs authentication` (`kedalio-server`):
in the second case run `/mcp` in Claude Code, pick `kedalio` and start the sign-in. The browser shows a code: approve it on the server
with `docker compose exec core kedalio-core approve <code>` (see below).

### Claude Code, with `claude mcp add`

Without the plugin, over HTTP with the bearer token (the connection only, no skills):

```sh
claude mcp add --transport http kedalio http://127.0.0.1:8787/mcp \
  --header "Authorization: Bearer $KEDALIO_CORE_TOKEN"
```

Put the real address where `127.0.0.1:8787` is when the core runs elsewhere (over https, see below). The local
stdio form is `claude mcp add kedalio -- kedalio-core stdio` ([docs/local.md](docs/local.md)).

### claude.ai and other remote MCP clients

A remote client reaches the core from the internet, so it needs a public HTTPS URL, and the core must know it: start it
with `-public-url https://kedalio.example.com` (`KEDALIO_CORE_PUBLIC_URL`). It must be https (http is accepted only on
loopback) and it must not change later. That turns the OAuth server on; the client registers itself and you approve
its code. The step-by-step for claude.ai, the Cloudflare and Tailscale tunnels, what a remote client may do and
how to revoke it: [docs/claude-ai-connector.md](docs/claude-ai-connector.md).

## First run

The first start makes the instance key (`<data>/kek`) and writes `<data>/first-run.txt` (mode 0600, never the log): the
recovery code. Copy it to a password manager or other safe place, then delete the file. Without the key file or the
recovery code, the database and its backups cannot be read.

### Approve a new client

A client that connects for the first time (claude.ai, Claude Code on a server install) opens a page that shows a code
such as `4F7K-92QD`. The page names the client, but that name is whatever the client says. Approve the code on the
server:

```sh
docker compose exec core kedalio-core approve 4F7K-92QD
```

The command shows the client, whether it is a cloud client or a program on this machine, where it will be sent back to,
and the address that asked, then asks `Approve? [y/N]`. The page notices and finishes the connection; the browser does
not get a session. A code lives ten minutes and works once. Approve only a code you started yourself, just now: a code
can be relayed (see [SECURITY.md](SECURITY.md)).

Once one local client is connected, you can also ask it to approve the code of another local client on your computer
(the `clients_approve` tool, which asks you first and works only for a request that came from your own address). Cloud clients and passkeys are approved only with the command.

A passkey is optional. To make one, open `/setup` on your server's address, press the button and approve the code it
shows with the same command; you may make more than one. With a passkey, the approval page offers "Sign in with a
passkey instead", and a browser signed in that way connects clients with one click. `kedalio-core approve -list` shows
the requests waiting (never their codes), `-deny-all` refuses them, and `kedalio-core passkeys list | remove <id>` shows
and removes passkeys: a passkey is added with an approval code, so look at the list if you did not expect one.

## Backups and restore

With `-backup-dir` the core keeps encrypted backups of the database (every 24 hours by default, the newest 14 kept; the
Compose file sets it to `/data/backups`, on the same volume, so it protects against a mistake but not a lost disk). With
`-backup-s3-endpoint` each backup also goes to an S3-compatible store (Cloudflare R2, Backblaze B2, MinIO, AWS S3).
Beside each backup sits a tag: its hash, size, time and the instance's fingerprint under an HMAC derived from the
instance key, so a changed, truncated, renamed or foreign backup is detected. `/health` always answers `status: ok` (or
`locked`); its `offsite_backup` field (`waiting`, `ok`, `stale` or `failing`) is shown only to a caller with the owner's bearer
token.

### Restore from the store, on a new machine

Fetch a backup with `kedalio-restore` into a new data folder, then start `kedalio-core` on it:

```sh
BACKUP_S3_ACCESS_KEY=... BACKUP_S3_SECRET_KEY=... KEDALIO_RECOVERY_CODE=... \
  kedalio-restore -endpoint https://<account>.r2.cloudflarestorage.com -bucket kedalio-backups -to ./data -list
```

Drop `-list` to fetch the newest backup (`-name` picks another). `-prefix` and `-region` must match the core's
`-backup-s3-prefix` and `-backup-s3-region` if you changed them. It needs the recovery code in `KEDALIO_RECOVERY_CODE`
to check the tag. A backup older than the newest verified one is refused unless you pass `-allow-older`; a backup with
no tag (made before backups were signed) unless you pass `-allow-unsigned`. Both flags mean you vouch that the backup
is the one you expect. The folder `kedalio-restore` makes has `core.db` but no instance key, so `kedalio-core` starts
**locked**: it answers only `/health` and `/unlock`. Open it with the recovery code, read from the terminal so that it
is not in your shell history or in a process list:

```sh
read -rs CODE
printf '{"code":"%s"}' "$CODE" | curl -sS -X POST -H 'content-type: application/json' -d @- http://127.0.0.1:8787/unlock
```

The protections and their limits: [architecture.md](docs/architecture.md#backup-authentication-and-its-threat-model).

### Restore from a local backup folder

A backup in `-backup-dir` is a file `core-<time>.db` (with a `core-<time>.db.tag` beside it). Stop the core, make a new
data folder, copy the backup into it as `core.db`, and copy the instance key `kek` from the old data folder next to it.
No `/unlock` is needed, because the key is there:

```sh
mkdir -p ~/kedalio-restored
cp /path/to/backups/core-<time>.db ~/kedalio-restored/core.db
cp /path/to/old-data/kek ~/kedalio-restored/kek
kedalio-core stdio -data ~/kedalio-restored        # or: kedalio-core -data ~/kedalio-restored
```

If the `kek` file is lost, start `kedalio-core` (server mode) on the folder without it: it comes up locked, and you
unlock it with the recovery code as above.

## Configuration

Every flag has an environment twin, `KEDALIO_CORE_` plus the name in capitals with `_` for `-` (so `-public-url` is
`KEDALIO_CORE_PUBLIC_URL`), except where the table says otherwise. `kedalio-core -h` lists them all. A number or a
duration that does not parse in the environment stops the start and names the variable. `kedalio-core -version` prints
the version and exits (it is not in `-h`). `kedalio-core stdio` takes only `-data`, `-owner`, `-language`, `-timezone`,
`-embed-url`, `-embed-model`, `-backup-dir`, `-backup-every` and `-backup-keep`. Some subcommands do not start the core:
`kedalio-core clients [-data dir] list | revoke <client-id>`,
`kedalio-core approve [-data dir] [-deny] [-yes] <code> | -list | -deny-all`,
`kedalio-core passkeys [-data dir] list | remove <id>` (removing one also signs every browser out) and
`kedalio-core revisions redact [-data dir] [-dry-run] (<path>@<rev> | <path>)+`.

| Flag or variable                                                  | What it does                                                                                                                                                                                                         |
| ----------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `KEDALIO_CORE_TOKEN` (environment only)                           | Required for the server. The static bearer token, at least 32 characters (`openssl rand -hex 32`). Not used by `stdio`.                                                                                              |
| `KEDALIO_UPKEEP_TOKEN` (environment only)                         | Optional second bearer token, for an upkeep agent: what it writes is recorded as its own. Empty: none.                                                                                                               |
| `-data` (default `data`)                                          | Folder of the database (`core.db`) and the instance key (`kek`). The image uses `/data/core`. In `stdio` the default is the application data folder of your system.                                                  |
| `-listen` (default `127.0.0.1:8787`)                              | Address to listen on. The image listens on `0.0.0.0:8787`.                                                                                                                                                           |
| `-public-url`                                                     | The https origin the core is reached at. Turns OAuth (approval codes, optional passkey) on; empty: the static token only.                                                                                            |
| `-client-ip-header`                                               | Header a proxy in front sets with the client's address (`x-forwarded-for`, `cf-connecting-ip`), for the rate limits.                                                                                                 |
| `-trusted-proxies`                                                | Comma-separated addresses or CIDRs of the proxies in front. With it set, the header counts only on a connection from one of them, and in `x-forwarded-for` the right-most address that is not a proxy is the client. |
| `-language`                                                       | `English` or `Italian`, for a new database: the names of the memory's areas and the answers. Fixed when the database is made; English when empty.                                                                    |
| `-timezone` (alias `-zone`)                                       | IANA time zone: of a new database, else its own; given, it overrides the stored zone for that run. Empty: the host's (`TZ`; in Docker usually UTC).                                                                  |
| `-owner`, `-label`                                                | Your name for the instructions clients are given; a word clients read beside the address.                                                                                                                            |
| `-embed-url`, `-embed-model`                                      | An OpenAI-compatible embeddings server (a trailing slash is fine) and its model, for search by meaning; empty: search by words alone.                                                                                |
| `-backup-dir`, `-backup-every`, `-backup-keep`                    | Folder of the backups, how often (default 24h, at least a minute) and how many are kept (default 14, at least 1). `KEDALIO_CORE_BACKUP_KEEP` and `KEDALIO_CORE_BACKUP_EVERY` are their environment twins.            |
| `-backup-s3-endpoint`, `-backup-s3-bucket`                        | An S3-compatible store the backups are also sent to, and its bucket. Empty endpoint: backups stay on this disk. Needs `-backup-dir`. Plain `http://` is refused unless the store is on loopback.                     |
| `-backup-s3-region`, `-backup-s3-prefix`, `-backup-s3-keep`       | Region (default `auto`), folder in the bucket (default `kedalio-core/`; a trailing `/` is added if missing), backups kept there (default 30).                                                                        |
| `BACKUP_S3_ACCESS_KEY`, `BACKUP_S3_SECRET_KEY` (environment only) | The store's keys. Never a flag, a file or a log.                                                                                                                                                                     |
| `KEDALIO_RECOVERY_CODE` (environment only)                        | Read by `kedalio-restore` only, to check a backup's tag.                                                                                                                                                             |
| `KEDALIO_PORT`, `KEDALIO_IMAGE`                                   | Read by `compose.yaml`, not by the core: the port on the host (default 8787) and the image to run.                                                                                                                   |

Behind a proxy or tunnel set `-client-ip-header` and `-trusted-proxies`, or every client looks like the proxy. Language and
time zone are the database's own: a `-language` that disagrees with the stored one is logged and ignored, since it would
move every path. A database made before 1.0 records on its first start what the old code ran with.

## Security notes

- One owner per instance. The core trusts its owner and the clients the owner connects; it is not multi-tenant.
- Clients sign in with the bearer token or with OAuth, and a new OAuth client is approved by a code (or by a signed-in passkey session at consent). The
  consent page shows where the client will be sent back to (scheme, host and port); the client's name is what the
  client says it is, so trust the address. A client registered with a remote redirect (claude.ai, ChatGPT) is a cloud
  client with a fixed, smaller policy: it can read, save tasks and put notes in an inbox that waits for you.
- The database is encrypted at rest with a key derived from the instance key in `<data>/kek`; a stolen volume that holds
  the key file gives both, so keep the recovery code elsewhere.
- The memory refuses to store what looks like a secret. Never put the token or keys in a page.
- Do not expose port 8787 as plain http. Put a tunnel or a TLS proxy in front, and set the proxy flags. The core warns
  at start when it sees a risky mix. With `-public-url` and no `-client-ip-header`, every client behind the proxy shares
  one rate-limit bucket, so one outsider can use up the sign-in limits for everyone. With `-client-ip-header` and no
  `-trusted-proxies`, anyone who reaches the core directly can choose the address the limits see, so the core must be
  reachable only through the proxy. Neither warning stops the start.
- Reading a PDF stops at 2000 pages or 8 MiB of text; the rest is left out of the index without an error.

The full model, including what backups do not protect against: [docs/architecture.md](docs/architecture.md). To report a
vulnerability privately: [SECURITY.md](SECURITY.md).

## More documentation

- [docs/install.md](docs/install.md): the guide an agent follows to install Kedalio.
- [docs/local.md](docs/local.md): on your own computer, with no server.
- [docs/claude-ai-connector.md](docs/claude-ai-connector.md): a public address, tunnels and claude.ai.
- [docs/upgrade.md](docs/upgrade.md) and [docs/troubleshooting.md](docs/troubleshooting.md).
- [docs/architecture.md](docs/architecture.md) and [docs/adr/](docs/adr/README.md): how it fits and why.

## Development

[CONTRIBUTING.md](CONTRIBUTING.md) has how to build, test and send a change. [AGENTS.md](AGENTS.md) is how the
maintainer's agents work on it.

## License

Kedalio is free software under the GNU Affero General Public License, version 3 ([LICENSE](LICENSE)).

Copyright (C) 2026 The Kedalio authors
