# Troubleshooting

Where the core logs: standard error (`docker compose logs core` for the container). A start that fails exits with
status 1 and its last line is the error; the lines before it are warnings. Nothing it prints is a secret.

## Startup errors

### `KEDALIO_CORE_TOKEN is not set: the connector does not start without a bearer token`

The server needs a token. Set `KEDALIO_CORE_TOKEN` in the environment (`openssl rand -hex 32`). It is not needed for
`kedalio-core stdio`. In Docker Compose the same problem shows as
`set KEDALIO_CORE_TOKEN in .env (openssl rand -hex 32)`. If the variable is in `.env` and you still see this, the file
is not where Compose runs: `.env` must sit beside `compose.yaml`, and a line `KEDALIO_CORE_TOKEN=` with nothing after
the sign is an empty token.

### `KEDALIO_CORE_TOKEN is N characters: at least 32 (for example openssl rand -hex 32)`

The token is too short. Make a new one with `openssl rand -hex 32`, put it in `.env` or the environment, and update
the clients that use the token.

### `KEDALIO_CORE_BACKUP_EVERY="..." is not a duration (for example 24h or 30m)`

A value in the environment does not parse, and the core refuses to run with something other than what you set. The
message names the variable: `KEDALIO_CORE_BACKUP_EVERY` takes a duration such as `24h` or `30m`; `KEDALIO_CORE_BACKUP_KEEP`
and `KEDALIO_CORE_BACKUP_S3_KEEP` take a whole number (`... is not a whole number`). Fix or remove the variable.

### `-backup-keep is at least 1 and -backup-every at least a minute`

The two values are out of range.

### `-backup-s3-endpoint sends the copies -backup-dir makes: set -backup-dir too`

The offsite store only receives what the local backup makes. Set `-backup-dir` (`KEDALIO_CORE_BACKUP_DIR`).

### `-backup-s3-endpoint needs -backup-s3-bucket`, `-backup-s3-keep is at least 1`

Set the bucket, and a keep of 1 or more.

### `-backup-s3-endpoint needs the environment variables BACKUP_S3_ACCESS_KEY and BACKUP_S3_SECRET_KEY`

The store's keys come from the environment only, never from a flag.

### An offsite endpoint that starts with `http://`

The core starts, but each offsite upload fails and the log says `offsite backup failed, retrying err="the endpoint must be https (http only on a loopback address)"`. Plain `http://` is accepted only for `localhost`, `127.0.0.0/8` or `::1`. Use the store's https address.

### `the public URL "..." is not an origin (scheme://host[:port])`, `the public URL "..." must be https (http only on a loopback address)`

`-public-url` is an origin, with no path: `https://kedalio.example.com`. It must be https; plain http is accepted only
for `localhost` and other loopback addresses.

### `-trusted-proxies: trusted proxy "..." is neither an address nor a CIDR`

A comma-separated list of addresses (`10.0.0.2`) or CIDRs (`10.0.0.0/8`).

### `memory: database was created by a newer Kedalio; upgrade the binary (schema 7, this binary knows 6)`

You started an older core on a newer database. See [upgrade.md](upgrade.md#database-is-newer).

### `... has a database but not its key file (kek): put the key file back; the recovery code opens it only in server mode`

Seen with `kedalio-core stdio` on a folder that has `core.db` but no `kek`. Put the `kek` file back. Or start the
server (`kedalio-core -data <folder>`) on that folder and unlock it with the recovery code, see below.

### `... does not open core.db: restore the right key file, or remove it and unlock with the recovery code`

The `kek` file in the folder belongs to another database. Put the right one back, or remove this one and unlock with
the recovery code.

### `listen tcp 127.0.0.1:8787: bind: address already in use`

Another program, or another core, holds the port. Stop it, pass another `-listen`, or, with Docker Compose, set
`KEDALIO_PORT` in `.env`.

### `no user application folder: pass -data`, `no home folder: pass -data`

`kedalio-core stdio` could not find a data folder for your system. Pass `-data <folder>`.

## The core is locked

The log says `locked: POST the recovery code to /unlock`, and `/health` answers `{"status":"locked"}`. The core has the
database but not its key file: a restore with `kedalio-restore`, or a data folder moved without `kek`. It answers
`/health` and `/unlock` only. Send the recovery code (it never goes in an argument):

```sh
read -rs CODE
printf '{"code":"%s"}' "$CODE" | curl -sS -X POST -H 'content-type: application/json' -d @- http://127.0.0.1:8787/unlock
```

A wrong code answers 401 with `that is not this instance's recovery code` (the command has no `-f`, so curl shows it). `/unlock` is rate-limited, so wait a little after a few
tries.

## Warnings at start

These do not stop the core. Both are about running behind a proxy or a tunnel.

### `rate limits: -public-url is set but no -client-ip-header: ...`

Every client looks like the proxy, so one outsider can use up the sign-in, token and unlock limits for everyone. Set
`-client-ip-header` (`x-forwarded-for`, or `cf-connecting-ip` for Cloudflare).

### `rate limits: the client-ip header is trusted from any connection (no -trusted-proxies): ...`

Anyone who reaches the core directly can choose the address the limits see. Set `-trusted-proxies` to the proxy's
address, and make sure the core is reachable only through the proxy.

## Backups

- `kedalio-restore` says `the environment KEDALIO_RECOVERY_CODE (the recovery code) is needed to check the copy's tag`,
  or `-endpoint, -bucket and the environment BACKUP_S3_ACCESS_KEY and BACKUP_S3_SECRET_KEY are needed`, or
  `-to is the new data folder`: set what it names. `-to` must be a folder with no `core.db`.
- `refusing ...: the store holds a newer copy with a valid tag`: the backup you asked for is older than the newest one
  the store verified, which can be a rollback. If you mean it, add `-allow-older`. A backup without a tag needs
  `-allow-unsigned`. Both flags mean you vouch that the backup is the one you expect.
- The download fails when the store sends more than it listed. Run it again; if it repeats, the store or the path to it
  is not trustworthy.
- `/health` with the bearer token shows `offsite_backup`: `waiting` (none sent yet), `ok`, `stale` (the last
  upload is older than it should be) or `failing` (the store refuses the uploads): look in the log for the store's error.

## Sign-in and clients

- claude.ai cannot sign in: the core was started without `-public-url`, or the address differs from the one the client
  uses. Check `curl -fsS https://<address>/.well-known/oauth-authorization-server`: its `issuer` must equal the public
  URL. The address cannot change once a client or a passkey exists.
- `claude mcp list` shows `Needs authentication`: run `/mcp` in Claude Code, pick `kedalio`, and approve the code the browser shows with
  `docker compose exec core kedalio-core approve <code>` on the server.
- A client gets `401`: its token is wrong or revoked. An old `KEDALIO_VIRGIL_TOKEN` no longer exists in 1.0 and gets 401.
- A tool call answers `the call failed inside the core`: the detail is only in the core's log, on purpose.
- The sign-in page says the code expired, or `kedalio-core approve` answers `no such approval`: a code lives ten minutes and
  works once. Press "Get a new code" on the page (or start the sign-in again) and approve the new one.
- The page says `too many waiting approvals`: 20 requests are waiting. Wait a few minutes for them to expire; nothing is
  evicted, so a flood cannot push out your own.
- `kedalio-core approve` refuses to run without a terminal or as root: use `ssh -t` and `docker compose exec` without
  `-T`, and never `-u root` (files made by root cannot be opened by the core afterwards).
- A cloud client's code is not approved by `clients_approve`, nor is one from another address than the caller's: use the
  server command. Behind a proxy or a tunnel, `KEDALIO_CORE_CLIENT_IP_HEADER` (and `KEDALIO_CORE_TRUSTED_PROXIES`) must be
  set, or every request looks like it comes from the proxy and the address checks and per-address limits mean nothing.
- `too many waiting approvals from your address`: three requests from one address are waiting. Wait for them to expire,
  or run `kedalio-core approve -list` and `-deny-all` on the server.
- A passkey you do not know: `kedalio-core passkeys list`, then `kedalio-core passkeys remove <id>` (it signs every
  browser out; sign in again with a passkey you know).
- `first-run.txt` holds only the recovery code; it stays until you delete the file.
