# Kedalio on your own computer

No server needed: Claude Code or Claude Desktop starts `kedalio-core` itself and talks to it over stdin and stdout.
It is the same program, the same tools and the same encrypted database as on a server; the person who runs the process
is the owner, so there is no HTTP port, no token and no passkey.

## Install

The quickest way is the one-prompt install in the [README](../README.md#install-with-one-prompt): Claude downloads the
release, checks it and connects Claude Code for you. By hand, take the archive for your system from the
[releases](https://github.com/artysan-code/kedalio/releases) page (`kedalio-<os>-<arch>.tar.gz`, `.zip` on Windows,
checked against `SHA256SUMS`) and put `kedalio-core` on your `PATH`, or build it (Go, see `go.mod` for the version):

```sh
go build -o ~/.local/bin/kedalio-core ./core/cmd/kedalio-core
```

Check it: `kedalio-core -version` prints the version, and `kedalio-core stdio -h` the flags. Where the client config below
says `kedalio-core`, a full path (`/home/alice/.local/bin/kedalio-core`) works the same, and is safer for Claude
Desktop, which does not read your shell's `PATH`.

## Claude Code

With the plugin, which also brings the skills that teach Claude when to recall, save and close tasks:

```sh
claude plugin marketplace add artysan-code/kedalio
claude plugin install kedalio-local@kedalio --config core_path=/home/alice/.local/bin/kedalio-core --config language=English
```

`core_path` is the full path of the binary and `language` is `English` or `Italian`; it counts only when the database
is made. `claude mcp list` then shows `plugin:kedalio-local:kedalio` as `Connected`. Without the plugin, add the
connection alone:

```sh
claude mcp add kedalio -- kedalio-core stdio
```

Everything after `--` is the command. Options go after `stdio`, for example
`claude mcp add kedalio -- kedalio-core stdio -data /home/alice/notes/kedalio -language Italian`. `-s user` makes it
available in every folder (the default, `local`, is this folder only for you).

To share it with a project instead, put a `.mcp.json` in the project folder:

```json
{
  "mcpServers": {
    "kedalio": {
      "command": "kedalio-core",
      "args": ["stdio"]
    }
  }
}
```

Do not put the recovery code or any key in that file; there is nothing secret in the config.

## Claude Desktop

Edit `claude_desktop_config.json` (Settings, Developer, Edit Config opens it) and restart Claude Desktop:

| System  | File                                                              |
| ------- | ----------------------------------------------------------------- |
| macOS   | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Windows | `%AppData%\Claude\claude_desktop_config.json`                     |
| Linux   | `~/.config/Claude/claude_desktop_config.json`                     |

```json
{
  "mcpServers": {
    "kedalio": {
      "command": "/Users/alice/bin/kedalio-core",
      "args": ["stdio"]
    }
  }
}
```

On Windows use `"command": "C:\\Users\\alice\\bin\\kedalio-core.exe"`.

## Where the data is

`kedalio-core stdio -data <folder>`, or, without `-data` (or `KEDALIO_CORE_DATA`), the folder an application of your
system keeps its data in:

| System  | Default folder                                          |
| ------- | ------------------------------------------------------- |
| Linux   | `$XDG_DATA_HOME/kedalio`, else `~/.local/share/kedalio` |
| macOS   | `~/Library/Application Support/Kedalio`                 |
| Windows | `%AppData%\Kedalio`                                     |

The folder is made on the first start, readable by you only. It holds `core.db` (the encrypted database), `kek` (the
key that opens it) and, until you delete it, `first-run.txt`.

The first start writes a recovery code to `first-run.txt` and says so on stderr (the client's log). Copy the code
somewhere safe, away from this computer, and delete the file; if an agent installed Kedalio for you, open it yourself;
the agent should not read it. The code is what unlocks the database if the `kek` file is
lost, and what reads a backup after a lost disk. Without the key file and without the code the data cannot be read.

The language and time zone of a new database are settled as on a server (`-language`, `-timezone`, else English and the
computer's zone) and stay with the database.

## What runs in the background

Only what the process needs while a client is connected, and it all ends when the client closes stdin or the process
gets SIGINT or SIGTERM; the database is then closed cleanly.

- Search indexing of what you write is part of each write, not a job.
- Embeddings run in the background only with `-embed-url` and `-embed-model` (an OpenAI-compatible server, for
  example a local one); without them search is by words alone.
- Backups run only with `-backup-dir <folder>`: an encrypted copy there when the last is older than `-backup-every`
  (a day), the newest `-backup-keep` kept. Put the folder on another disk. There are no offsite (S3) flags in stdio
  mode; copy the folder yourself, or run a server for that.
- Nothing listens on a port and nothing leaves the computer unless you set an embeddings URL or a source URL is fetched
  by a tool call.

Stdout carries the protocol only; every log line goes to stderr.

## Two clients at once

Claude Desktop and Claude Code (or two windows) each start their own process on the same folder. SQLite serializes
their writes (a write waits up to 5 seconds for the other) and the first start of a new folder is safe with several
processes. The one cost is duplicated optional work: with `-backup-dir` or `-embed-url` set, each process checks on its
own, so a copy may be made twice in the same minute. There are no lock files. Do not point two _different computers_ at a
folder on a network drive: SQLite's locking does not hold there.

## Restoring a backup

With `-backup-dir`, the backups are files `core-<time>.db`. To restore one, close every client, make a new folder, copy
the backup into it as `core.db` and the `kek` file of the old folder beside it, and start on that folder
(`-data <folder>`). The `/unlock` step is for a server only: a folder with `core.db` and no `kek` does not open in
stdio mode (see below). Copying a file from `-backup-dir` is not verified: only `kedalio-restore` checks a backup's tag, and it reads from an
S3-compatible store (it needs `-endpoint` and `-bucket`). Details and the restore from an S3 store: [README](../README.md#backups-and-restore).

## Moving to a server later

Close every client, then copy the whole folder (`core.db` and `kek`, plus a `core.db-wal` or `-shm` if one is left) to
the server's data folder and start `kedalio-core` there as in the [README](../README.md#install-by-hand). The key file travels
with it, so nothing needs unlocking. With `-public-url` set, the first start on the server prints the link that makes
your passkey. The reverse works too: a server's data folder copied to the computer opens with `stdio`.

A folder that has `core.db` but no `kek` does not open in stdio mode; put the key file back, or start a server on it and
unlock with the recovery code (`POST` it to `/unlock`, as in [troubleshooting.md](troubleshooting.md#the-core-is-locked)).
