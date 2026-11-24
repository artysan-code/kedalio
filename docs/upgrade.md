# Upgrading Kedalio

Kedalio versions are `X.Y.Z`. A release tag is `vX.Y.Z`, and the image and the archives carry the same number.
`kedalio-core -version` prints the one you run. The [CHANGELOG](../CHANGELOG.md) says what changed in each release,
and anything that breaks something is marked there.

The database moves forward by itself: the first start of a newer core migrates it. A migration never drops, renames or
retypes anything, so a newer core always opens an older database. The reverse does not hold (see "Database is newer"
below), which is why you take a backup first.

## Before you upgrade

1. Make sure a backup is recent. With `-backup-dir` the core makes one every day; to be sure, copy the data folder with
   the core stopped (`core.db` and `kek`, plus a `core.db-wal` or `core.db-shm` if one is left), or copy the newest
   `core-<time>.db` from the backup folder together with `kek`. How to restore one: [README](../README.md#backups-and-restore).
2. Read the CHANGELOG from your version to the new one.
3. Check that you still have the recovery code. If you do not, you cannot read a backup after a lost `kek` file.

## Docker Compose

If you use the image from a release, `compose.yaml` names a tag. Download the new release's `compose.yaml` over it, or
change the tag in `KEDALIO_IMAGE`, then:

```sh
docker compose pull
docker compose up -d
curl -s "http://127.0.0.1:${KEDALIO_PORT:-8787}/health"
```

If you follow `latest` (the default of a clone's `compose.yaml`), the same two commands pull the newest release. To
build from a clone instead, update it and run `docker compose up --build -d`.

The data lives in the `core-data` volume, so it survives the new container. Never run `docker compose down -v`: it
deletes the volume.

## Binary

Download the new archive and `SHA256SUMS` from the [releases](https://github.com/artysan-code/kedalio/releases) page,
check the sum, and put `kedalio-core` and `kedalio-restore` over the old ones. Restart whatever runs the core. On a
computer that uses `kedalio-core stdio`, the client starts the new binary the next time it connects, so close the client
first and reopen it. If Claude Desktop and Claude Code both use it, close both: an old process left running keeps the
old code.

## The plugin

The plugin (`kedalio-local` or `kedalio-server`) holds the skills and the connection settings, not the core. Update it
with `claude plugin update kedalio-local@kedalio` (or `kedalio-server@kedalio`), then run `/reload-plugins --force` or
start a new session.

## Database is newer

If you start an older core on a database that a newer one has migrated, it stops with:

```
memory: database was created by a newer Kedalio; upgrade the binary (schema 7, this binary knows 6)
```

(the numbers are the database's schema and the one this binary knows). It changed nothing. Run a core at least as new
as the one that last opened the database. To go back to an older core for real, restore a backup made before the
upgrade; do not copy files by hand into a database the newer core has migrated.

## When something fails

The startup errors, with what to do about each, are in [troubleshooting.md](troubleshooting.md).
