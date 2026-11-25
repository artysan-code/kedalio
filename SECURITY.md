# Security policy

## Reporting a vulnerability

Please report a vulnerability privately, not in a public issue. Use GitHub's private vulnerability reporting on
[artysan-code/kedalio](https://github.com/artysan-code/kedalio): open the Security tab and choose "Report a
vulnerability", or go straight to <https://github.com/artysan-code/kedalio/security/advisories/new>. Say what you
found, how to reproduce it and which version or commit you ran. Do not include real tokens, recovery codes or personal
data; a made-up example is enough.

If that form is not available to you, open a public issue that says only that you have a security report and no
details; the maintainer will then arrange a private channel.

Kedalio is maintained by one person without a service-level promise. A report is read as soon as possible, and a fix is
published with a note in the changelog. Please keep the details private until a fixed release is out.

## Supported versions

Only the latest 1.x release is supported. Older releases get no fixes: upgrade to the newest one.

## Scope

Kedalio serves one owner and the clients that owner connects. The threat model, and what the backups protect and do not,
are in [docs/architecture.md](docs/architecture.md). A client that already holds the owner's bearer token, or the data folder
with its instance key, is outside what the core defends against.

## Telemetry and outgoing calls

Kedalio has no telemetry: no usage data, crash reports or update checks leave the core. Its only outgoing calls are
the S3-compatible store of the offsite backup (`-backup-s3-endpoint`, off unless set), the embeddings server
(`-embed-url`, off unless set), and the fetch of a web address the owner or a connected client gives when adding a
document to a collection (private and loopback addresses are refused). `get.sh` and `kedalio-restore` reach the
release host and the backup store when you run them.

## Approval codes and phishing

A new client is authorized when you approve a short code its sign-in page shows. A code is not phishing-resistant, where a
passkey is. An attacker can start the sign-in against your instance (their own claude.ai account, or a program on their
machine), read the code from their screen and ask you to approve it ("please approve 4F7K-92QD"). If you do, they are
connected as you. The code cannot tell who started the request, so you have to:

- approve only a code you started yourself, in the last few minutes, and never one somebody sent you;
- read what `kedalio-core approve` shows before you answer: the client name (self-declared, so unverified), whether it
  is a cloud client or a local program, the host it will be sent back to, and the address that asked.

Behind a proxy or a tunnel (a cloud client always reaches you that way), set `KEDALIO_CORE_CLIENT_IP_HEADER` and
`KEDALIO_CORE_TRUSTED_PROXIES`. Without them every request appears to come from the proxy, and three things stop
meaning anything: the "Asked from" address that `kedalio-core approve` shows, the limit of three waiting requests per
address, and the `clients_approve` tool's check that a request came from the caller's own address.

Defences in the core: a code is eight symbols, lives ten minutes and works once; at most 20 are waiting at a time, 3 from one address and 2 for passkeys (nothing is evicted, so one address cannot push out the owner's request, and `kedalio-core approve -deny-all` clears them); it is
stored hashed and never logged; approving needs the server's shell or an owner-class client, so nobody can guess codes
from outside; the `clients_approve` tool answers only for the owner's token or a local client, shows the details first,
approves only a request that came from the address the call comes from (a program an attacker runs on their machine also
has a loopback redirect, so "local" alone is not enough), needs the redirect host repeated, and refuses cloud clients and
passkeys. A passkey remains the phishing-resistant way to sign in, and it is optional.
Making a passkey is itself approved with a code, only on the server, and the core logs each passkey it adds. Because a
relayed code could add a passkey you did not mean to, look at `kedalio-core passkeys list` now and then and remove
what you do not know with `kedalio-core passkeys remove <id>`, which also signs every browser out.
