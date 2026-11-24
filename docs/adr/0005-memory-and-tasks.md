# 0005 — Kedalio is memory and tasks

- Status: accepted
- Date: 2026-11-06

## Decision

The owner decided that Kedalio is a personal tool, not a product (this line is superseded by
[0006](0006-public-release.md): the code is now public open source, still one owner per instance). It is only the memory, the tasks, the projects and the
sign-in, behind one MCP connector. There is no app for the web or the phone, no host, no squad, no assistant, no vault as
a product and no integrations.

This supersedes [0001](0001-kedalio.md) where they differ: its second purpose (an assistant that acts on mail, chats and
calendar), principles 2 to 7 (confirmations and trust, hosts, efficiency, connecting other products' servers) and the
hosts, squads and presets built on them. Principle 1 (any model) stands, as the connector is the same for every client.
[0002](0002-architecture.md), [0003](0003-safety.md) and [0004](0004-what-carries-over.md) describe parts that no longer
exist; they are history.

## Consequences

- Everything removed is gone from the tree; it is not part of the public repository.
- No migration drops a table or column, renames one or changes a type: the database that exists starts under the new
  code, and the tables of the removed parts stay in it, unused.
- The owner's yes to a destructive call, asked in the app, has no place to be answered. `memory_delete` of a document or a
  collection runs on the call; the client's own confirmation (the tool is marked destructive) is the owner's yes.
- The short-lived tokens that enrolled computers were given are gone with the devices. Clients use the static token or
  OAuth.
- The offsite backup reads its S3 keys from the environment (`BACKUP_S3_ACCESS_KEY`, `BACKUP_S3_SECRET_KEY`), not from the
  vault.
- The site that described the removed product is not part of this repository.
