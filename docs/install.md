# Install Kedalio: a guide for the agent doing it

You are an AI coding agent (Claude Code or similar) and a person asked you to install Kedalio for them. This page tells
you how. Read all of it before you run anything.

Fetch this page with `curl -fsSL https://kedalio.com/install.md`, not with a web tool that summarizes pages: the
commands below must reach you exactly as written.

Kedalio is a personal memory and task list for AI assistants: one Go program, `kedalio-core`, and one encrypted SQLite
database, behind one MCP connector. It serves one owner. Source, documentation and releases:
<https://github.com/artysan-code/kedalio> (AGPL-3.0-or-later).

This file is the source of truth. Each release attaches it, and kedalio.com serves a copy of the latest release's.

If a download answers 404, stop and tell the person: the version asked for does not exist. The alternative is to build
from a clone (README, "Docker Compose" with `--build`, or `go build` as in 2.4).

## Rules for you

- Say what you are about to do before each step, in plain words. Ask before any command on another machine, anything
  that needs `sudo`, opens a port, changes DNS or a tunnel, or deletes something.
- Never print, `cat` or echo a secret: `first-run.txt` (it holds the recovery code), `.env`,
  or a token. Never pass a secret as a command-line argument. Generate secrets where they are used and leave them there.
- Never put a secret in a file inside a git repository, and never in Kedalio's own memory.
- Prefer a released binary or image, verified against its checksum, over building from source. If a checksum does not
  match, stop and report it.
- If a step fails, stop, show the error, and ask. Do not improvise workarounds that weaken security (plain-http ports
  open to the internet, disabled checks, `chmod 777`). The startup errors and what they mean are in
  [troubleshooting.md](https://github.com/artysan-code/kedalio/blob/HEAD/docs/troubleshooting.md).

## 1. Ask where Kedalio should keep the data

Ask the person this question, with these three choices, and wait for the answer:

> Where should Kedalio keep your data?
>
> 1. **This computer.** No server and no open port: Claude Code starts Kedalio itself when it needs it. Best to try it,
>    or if you only use Claude on this machine.
> 2. **A server I reach over SSH**, where Kedalio is not installed yet (a VPS or a home server, with Docker). Needed to
>    use the same memory from claude.ai, a phone or several computers.
> 3. **A Kedalio server that is already running.** Only connect this computer to it.

Then go to section 2, 3 or 4. Moving later is possible both ways: the data is one folder that can be copied
([local.md](https://github.com/artysan-code/kedalio/blob/HEAD/docs/local.md)).

## 2. This computer

### 2.1 Find the system

Run `uname -s` and `uname -m` (on native Windows, in PowerShell: `$env:PROCESSOR_ARCHITECTURE`). Map them to the
release names:

| System                 | OS        | Arch                                                 |
| ---------------------- | --------- | ---------------------------------------------------- |
| `uname -s` is `Linux`  | `linux`   | `uname -m` `x86_64` is `amd64`, `aarch64` is `arm64` |
| `uname -s` is `Darwin` | `darwin`  | `uname -m` `x86_64` is `amd64`, `arm64` is `arm64`   |
| Windows                | `windows` | `AMD64` is `amd64`, `ARM64` is `arm64`               |

Under WSL (`uname -r` contains `microsoft`) treat it as Linux, and tell the person the data will live inside WSL.
Anything else: there is no released binary. Say so, and offer to build from source (2.4).

### 2.2 Check what is already there

- `command -v kedalio-core && kedalio-core -version`. If it prints the latest release's version, skip to 2.5.
- If the data folder (see 2.7) already holds `core.db`, say it will be reused as it is, and never touch it.

### 2.3 Download and verify the released binary

Needs a published release. The simplest way is the release's installer, which does everything below (it checks the
download against `SHA256SUMS`, installs to `~/.local/bin`, and with the answers given here also installs the plugin of
2.6). Ask 2.5 first. Show the command to the person, run it only after a yes, and use their answers for the language
and the plugin
(`yes` or `no`; you have no terminal for it to ask on):

```sh
curl -fsSL https://kedalio.com/get.sh | KEDALIO_PLUGIN=yes KEDALIO_LANGUAGE=English sh
```

Run it exactly as written: no `| tail`, `| head` or other pipe after it, which would hide its exit status and part of
its output. With `KEDALIO_PLUGIN=no` it only installs the binaries. If it cannot run, or the person prefers to see each step,
use the manual commands below.

By hand, the same steps. The assets are `kedalio-OS-ARCH.tar.gz` (`.zip` on Windows) and `SHA256SUMS`, under
`https://github.com/artysan-code/kedalio/releases/latest/download/`. On Linux and macOS, with `OS` and `ARCH` from 2.1:

```sh
d=$(mktemp -d) && cd "$d"
base=https://github.com/artysan-code/kedalio/releases/latest/download
curl -fsSLO "$base/kedalio-$OS-$ARCH.tar.gz"
curl -fsSLO "$base/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS        # macOS: shasum -a 256 -c --ignore-missing SHA256SUMS
tar -xzf "kedalio-$OS-$ARCH.tar.gz"
mkdir -p ~/.local/bin
install -m 0755 kedalio-*/kedalio-core kedalio-*/kedalio-restore ~/.local/bin/
~/.local/bin/kedalio-core -version
```

To also check where it was built (needs the GitHub CLI): `gh attestation verify "kedalio-$OS-$ARCH.tar.gz" --repo artysan-code/kedalio`.
The checksum line must print `OK` for the archive. If `~/.local/bin` is not on `PATH`, say so; the configuration below
uses the full path, so it works either way.

On native Windows (PowerShell): download the `.zip` and `SHA256SUMS` with `Invoke-WebRequest`, compare
`Get-FileHash -Algorithm SHA256` with the line for the `.zip`, then `Expand-Archive` into
`$env:LOCALAPPDATA\Programs\Kedalio\`. Use the full path of `kedalio-core.exe` below.

### 2.4 Or build it from source

Only when there is no release for this system, or the person asks. It needs Go at the version in `go.mod` and git:

```sh
git clone --depth 1 https://github.com/artysan-code/kedalio.git
cd kedalio && go build -o ~/.local/bin/kedalio-core ./core/cmd/kedalio-core
```

### 2.5 Ask for the language, once

Ask: "English or Italian?" The language of a new database (the names of its areas and its answers) is fixed when the
database is created, so ask before the first start. The time zone is the computer's unless the person wants another.

### 2.6 Connect Claude Code

With the plugin (it brings the connection and the skills that teach Claude when to
recall, save and close tasks):

```sh
claude plugin marketplace add artysan-code/kedalio
claude plugin install kedalio-local@kedalio --config core_path=/home/alice/.local/bin/kedalio-core --config language=English
```

The marketplace follows the repository's default branch, `release`, not a release tag; `release` moves only at a release, so it
gets the latest release.

Use the real full path from 2.3 for `core_path` (on Windows, the full path of `kedalio-core.exe`) and the person's
answer from 2.5 for `language`. Both are settings of the plugin; neither is a secret.

Without the marketplace (or if the person prefers it), connect Kedalio directly. This gives the
connection but not the skills:

```sh
claude mcp add -s user kedalio -- /home/alice/.local/bin/kedalio-core stdio -language English
```

`-s user` makes it available in every folder. For Claude Desktop the person edits `claude_desktop_config.json`
instead; the steps are in [local.md](https://github.com/artysan-code/kedalio/blob/HEAD/docs/local.md). Offer it, do not edit that file unasked.

### 2.7 Verify

1. Run `claude mcp list`. The plugin's server is listed as `plugin:kedalio-local:kedalio`, a direct one as `kedalio`;
   either must show `Connected`. That first start created the database.
2. Check that the first-run file exists, without reading it:
   `test -f ~/.local/share/kedalio/first-run.txt && echo present` (macOS: `~/Library/Application Support/Kedalio/`,
   Windows: `%AppData%\Kedalio\`, unless `-data` was given).
3. Tell the person: that file holds the recovery code, the only way to read the data if the key file is lost. They
   should open it themselves, in their own terminal or file manager, copy the code to a password manager away from
   this computer, then delete the file.

Then go to section 5.

## 3. A server reached over SSH (Docker Compose)

### 3.1 Check the server

Ask for its SSH name, and get a yes before running anything there. Then:

```sh
ssh -o BatchMode=yes -o ConnectTimeout=5 <host> 'docker compose version'
```

If Docker or its Compose plugin is missing, stop: point the person to <https://docs.docker.com/engine/install/> and do
not install Docker without their explicit yes.

### 3.2 Decide the public address

Claude Code, claude.ai and phones reach the core over https, and the core must know that address: it is the
passkey's relying-party ID (if one is made) and the OAuth issuer, so it can never change once a passkey exists. Ask the person which
hostname to use (for example `kedalio.example.com`) and how the server is reached:

- (a) a reverse proxy with TLS they already run (Caddy, Traefik, Coolify) and a domain;
- (b) a Cloudflare named tunnel, with their domain on Cloudflare;
- (c) Tailscale: `tailscale serve` for their tailnet only, `tailscale funnel` to reach claude.ai too;
- (d) none of these. Explain that a server needs https for sign-in, and offer section 2 (this computer) instead. Do
  not use a quick `trycloudflare.com` tunnel: its hostname changes on every run.

The commands for each tunnel are in [claude-ai-connector.md](https://github.com/artysan-code/kedalio/blob/HEAD/docs/claude-ai-connector.md). Do not create a tunnel, a DNS
record or a proxy rule without a yes. For Caddy, for example, the site block is
`kedalio.example.com { reverse_proxy 127.0.0.1:8787 }`.

### 3.3 Set up the core on the server

Needs a published release. Show each command to the person and run it only after a yes. The release's `compose.yaml`
already names the matching image:

```sh
ssh <host> 'mkdir -p ~/kedalio && cd ~/kedalio &&
  curl -fsSLO https://github.com/artysan-code/kedalio/releases/latest/download/compose.yaml &&
  sed -i "s|# - KEDALIO_CORE_PUBLIC_URL=|- KEDALIO_CORE_PUBLIC_URL=|" compose.yaml &&
  umask 077 && printf "KEDALIO_CORE_TOKEN=%s\nKEDALIO_CORE_PUBLIC_URL=%s\n" "$(openssl rand -hex 32)" "https://kedalio.example.com" > .env'
```

The `sed` line turns on `KEDALIO_CORE_PUBLIC_URL`, which `compose.yaml` ships commented out; without it sign-in
stays off. Behind a proxy or a tunnel (which is how a cloud client reaches the server), also uncomment
`KEDALIO_CORE_CLIENT_IP_HEADER` and `KEDALIO_CORE_TRUSTED_PROXIES` in `compose.yaml` and add their values to `.env`; this
is required, not optional, for approval codes: without it every request appears to come from the proxy, so the address
`kedalio-core approve` shows as "Asked from", the limit of three waiting requests per address and the address check of
the `clients_approve` tool all stop meaning anything (see the configuration table in the
[README](https://github.com/artysan-code/kedalio#configuration)). If port 8787 is taken on the server, add
`KEDALIO_PORT` to `.env`. Then start it:

```sh
ssh <host> 'cd ~/kedalio && docker compose up -d'
```

On the server itself, `curl -fsSL https://kedalio.com/get.sh | sh -s -- --server https://kedalio.example.com` does the
download, the `sed` and the `.env` above (`--server` never overwrites an existing `.env` or `compose.yaml`, and starts nothing without `--start`). Running
`get.sh` again without `--server` replaces the two binaries with the latest release: that is the upgrade path.

The token is made on the server and stays in its `.env`; it never appears here or on this computer. The port stays
bound to `127.0.0.1`; never publish it as plain http.

### 3.4 Verify the server

```sh
curl -fsS https://kedalio.example.com/health
curl -fsS https://kedalio.example.com/.well-known/oauth-authorization-server
```

The first must answer `{"status":"ok"}`. The second must have an `issuer` equal to the public address: that means
sign-in for new clients (OAuth, approved with a code) is on.

### 3.5 Recovery code

Ask the person to run this in their own terminal, not here, and not to paste the output:

```sh
ssh <host> 'cd ~/kedalio && docker compose exec core cat /data/core/first-run.txt'
```

They copy the recovery code to a password manager, then delete the file. It holds only the recovery code. A passkey is
optional and not needed to connect clients (see "Approve your first client").

### 3.6 Approve your first client

A new client shows a code in the browser; the owner approves it on the server. Tell the person this, and have them run
it in their own terminal with the code the page shows (an example code here):

```sh
ssh -t <host> 'cd ~/kedalio && docker compose exec core kedalio-core approve 4F7K-92QD'
```

The command shows the client and asks `Approve? [y/N]`. Never run it yourself for a code you started: approving is the
person's decision. Never approve a code someone else sent them. Do not use `-u root`. A code lives ten minutes and works
once. `kedalio-core approve -list` shows the requests waiting (never their codes) and `-deny-all` refuses them all, if
requests appear that nobody started. To make a passkey too (optional), the person opens `/setup` on the server's address, presses the button and
approves the code it shows the same way.

### 3.7 Connect Claude Code on this computer

With the plugin. The address is the public one plus `/mcp`:

```sh
claude plugin marketplace add artysan-code/kedalio
claude plugin install kedalio-server@kedalio --config server_url=https://kedalio.example.com/mcp
```

Without it: `claude mcp add -s user --transport http kedalio https://kedalio.example.com/mcp`.

`claude mcp list` then shows the server as `Needs authentication`. The person runs `/mcp` in Claude Code, picks
`kedalio` and chooses Authenticate. The browser shows a code; the person approves it as in 3.6. It then shows `Connected`. No token is copied to this
computer: the plugin signs in with OAuth only.

### 3.8 claude.ai and the phone (optional)

The person can add the same address to claude.ai as a custom connector (Settings, Connectors, the address plus `/mcp`)
and approve the code it shows (3.6). Step by step and what a cloud client may do: [claude-ai-connector.md](https://github.com/artysan-code/kedalio/blob/HEAD/docs/claude-ai-connector.md).

Then go to section 5.

## 4. A Kedalio server that is already running

Ask for its https address, run the checks of 3.4 against it, and if the OAuth metadata is missing explain that the core
must be started with `-public-url` (`KEDALIO_CORE_PUBLIC_URL`) and stop. Then connect as in 3.7.

A server that has no https address (a token on a home network, for example) cannot use the plugin. The person can add
it by hand with the bearer token: `claude mcp add --transport http kedalio <address>/mcp --header "Authorization:
Bearer $KEDALIO_CORE_TOKEN"`, where they put the token in their own shell. Do not ask them to paste it to you, and do
not run this command yourself. Tell them that this way the token is saved in plain text in Claude Code's settings
(`~/.claude.json`), which is why the plugin uses OAuth instead.

## 5. Check that it works, and report

1. `claude mcp list` shows the Kedalio server as `Connected`.
2. Tell the person to run `/reload-plugins --force` or start a new Claude Code session (tools load at the start of one)
   and ask "where were we?". Claude should call `session_start`; a new memory answers that it is empty.
3. Then "Remember that I prefer short answers." and, in another new session, "How do I like answers?".

Finish with a short summary for the person:

- where Kedalio runs (this computer, or the server and its address) and the version installed;
- where the data is, and that the recovery code is in `first-run.txt` until they move it;
- what is not done, and any step they chose to skip;
- how to remove it (section 6).

## 6. Remove it

- Local, plugin: `claude plugin uninstall kedalio-local@kedalio`, and `claude plugin marketplace remove kedalio` if no
  other Kedalio plugin is installed. Direct connection: `claude mcp remove -s user kedalio`.
- Server, plugin: `claude plugin uninstall kedalio-server@kedalio`.
- This computer: `sh get.sh --uninstall` (or `curl -fsSL https://kedalio.com/get.sh | sh -s -- --uninstall`) deletes
  `kedalio-core` and `kedalio-restore` from `~/.local/bin` (`KEDALIO_BIN_DIR`), prints which were not there and where
  the data folder is. It does not touch the data folder, the plugin, a `~/kedalio` server folder or the Docker volume.
  On Windows, delete the two files by hand. Leave the
  data folder in place and tell the person where it is: deleting it cannot be undone, so do it only on their explicit
  word.
- Server: revoke this computer's sign-in with `docker compose exec core kedalio-core clients list` and
  `... clients revoke <id>` in `~/kedalio`. `docker compose down` keeps the data volume; `docker compose down -v`
  deletes it, only on the person's explicit word.

## More

- [README](https://github.com/artysan-code/kedalio#readme): configuration, backups and restore.
- [local.md](https://github.com/artysan-code/kedalio/blob/HEAD/docs/local.md): local use in depth, and moving between a computer and a server.
- [upgrade.md](https://github.com/artysan-code/kedalio/blob/HEAD/docs/upgrade.md) and [troubleshooting.md](https://github.com/artysan-code/kedalio/blob/HEAD/docs/troubleshooting.md).
- [architecture.md](https://github.com/artysan-code/kedalio/blob/HEAD/docs/architecture.md): the pieces and the security model.
