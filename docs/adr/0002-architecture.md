# 0002 — Architecture

- Status: superseded by [0005](0005-memory-and-tasks.md); kept as history
- Date: 2026-11-03

## Context

[0001](0001-kedalio.md) says what Kedalio is and why. This record decides how it is built: the pieces, where each one
runs, how they talk, and where the data lives. Each decision names what was weighed and why it won, so that it is not
argued again without a new reason.

## The pieces at a glance

```
owner's computer                       owner's host (or the computer itself, standalone)
┌──────────────────────────────┐       ┌──────────────────────────────────────────────┐
│ Claude Code · Codex · …      │       │ core: memory · tasks · vault · skills        │
│   ├ Kedalio MCP ─────────────┼──────▶│       Kedalio MCP · trust ledger · accounting│
│   ├ native MCP servers       │       │ host modules (host only):                    │
│   └ hooks (engine adapter)   │       │   assistant · listeners · run executor       │
│ Orion: TUI (assistant chat), │◀═════▶│   push · per-run containers                  │
│   credential broker, hooks   │ one   └──────────────────────────────────────────────┘
└──────────────────────────────┘ link                 ▲ wake-ups (FCM), then direct
                                        Android app ──┘
```

- **The core**: memory, tasks, vault, skills library, trust ledger, accounting, and Kedalio's own MCP server.
- **Orion**: one binary on the owner's computer. It holds the TUI (with the assistant's chat), the hook adapters of each
  engine, and the credential broker. In standalone mode it also runs the core.
- **Host modules**, switched on only on a host: the assistant, the listeners, the run executor, push.
- **The Android app**: native, in Kotlin.
- **Small shared services**, only when Kedalio is offered to others: a push relay, the verified Google app, sign-up.

A **host** is any always-on machine running the core as a service with the host modules: the owner's server, a
user's own server, or one of their computers. The **operator** is whoever runs the host for someone else.

| Phase   | Decisions                                                                                                                       |
| ------- | ------------------------------------------------------------------------------------------------------------------------------- |
| MVP 1   | 1, 2 (Claude Code adapter), 3, 4 (warm session on the subscription), 5, 6, 7, 8, 9, 10 (one owner), 11, 13, 15, 16, 17 (events) |
| MVP 1.5 | 2 (Codex adapter), 4 (API fallback), 9 (Markdown mirror), 17 (weekly summary, reports)                                          |
| MVP 2   | 12, 14, the computer as a worker                                                                                                |
| Later   | 10 (instances for others, shared services)                                                                                      |

## Decisions

### 1. One core, run in two ways

The core is one codebase. On a host it runs as an always-on service; standalone, it runs inside Orion on one computer.
Moving from standalone to a host moves data, not systems; a computer acting as the host later is the same core.
_Rejected_: two products (memory and tasks written twice, drifting apart); host only, even locally (Docker needed just
to try Kedalio). _Cost_: the core stays light; anything heavy is a host module.

Standalone has memory, tasks, vault, skills, the Kedalio MCP, hooks, the TUI and accounting from the engines. It has
no always-on assistant, listeners, push, phone app, unattended runs or synchronisation between computers; the
assistant is the TUI chat while Orion runs, and the session's "wider picture" is tasks and memory only.

### 2. Kedalio's MCP for every engine, hooks through Orion

The core exposes one MCP server with Kedalio's own tools (memory, tasks, skills), the same for every engine and for
claude.ai or the phone. On the computer, Orion installs each engine's hooks through an adapter per engine: context at
start, saving during the work, closing at the end. A new engine is a new adapter; the core does not change. MCP
servers of other products are installed natively in the engine, per project; the core never proxies them for the
engines. On the host, the assistant calls the servers it needs (the few served by Kedalio, templates, custom ones)
directly, with credentials from the vault. _Rejected_: MCP alone (nothing happens unless the model remembers to ask); all
through Orion (nothing works where Orion is not).

### 3. Credentials handed over, never stored where they are used

A native MCP server's config names the server and the account, never a token. When the engine starts it, Orion takes
the secret from the vault and hands it to that process only, through a pipe rather than an environment its children
inherit, with its output masked. Each computer is an enrolled device with its own key, revocable from the TUI or the
app. Orion keeps an encrypted fallback of the secrets this computer used in the last days, sealed with that device key
and expiring on its own: when the host is down, Orion uses it and says so; revoking the device makes it useless. The
vault stays the only source of truth.

On the host, the assistant and each unattended run get only the secrets their task names, scoped and short-lived where
the service allows it, handed over at start and revoked at the end; never the owner's broadest tokens. _Rejected_:
tokens in config files (plaintext on disk); the keyring as the store (copies to keep in step everywhere).

### 4. One engine interface, two kinds

The assistant and the workers talk to an _engine_ that is either an engine CLI on a subscription (Claude Code, then
Codex) or a model API (Anthropic, later OpenRouter and others), chosen per profile and per kind of work. When Kedalio is
offered to others, each user brings their own subscription or key.

The assistant runs on the subscription as one **warm session**: a long-lived Claude Code process that receives messages
and events one after another, instead of starting again and reloading its MCP servers, skills and memory at every
wake-up. It is compacted from time to time so it does not grow without end. Its model is Sonnet: a well-written prompt
and a character matter more than the biggest model. The API is the fallback, used only when the subscription is at its
limit or unavailable, under a daily spending cap. Part of the weekly limit is always kept for the owner's own work, and
nothing switches to the API at night without that cap.

### 5. Listeners: a two-level filter

Level one is cheap and uses no model: the core collects events (push where a service offers it, polling where it does
not) and matches them against the listeners' rules. Level two wakes the assistant only when a rule fires, or asks a
cheap model when an event is ambiguous. The assistant writes the rules and proposes them; the owner approves each one
([0003](0003-safety.md)). MVP 1 starts with a few listeners and measures what the owner accepts or ignores before the
timing of interruptions is learned. This needs study before it is built.

### 6. The phone: wake-ups through FCM, then a direct link

Firebase Cloud Messaging carries only empty wake-ups ("something is waiting"), never content; the app then fetches from
the host. It is free and wakes the phone reliably, which the mock call needs. While the app is open, chat and voice use
a direct connection to the host. For others, a small relay will hold the FCM keys. _Rejected_: a connection always
open (battery, killed by Android); UnifiedPush (an extra app, less reliable wake-ups).

### 7. The assistant on the computer lives in the TUI

A chat like a coding session, made to look good, inside Orion's TUI. No web app in MVP 1.

### 8. Go

The core, Orion, the TUI (Bubble Tea) and the hooks are written in Go: fast hooks, one static binary per platform,
macOS and Windows within reach, the core inside Orion as one binary. What stays in other languages: Claude Code mods
(JavaScript, they run inside Claude Code), the project-design kit (Node), the Android app (Kotlin). Today's code is a
reference, not a base: its logic is carried over where it holds. _Rejected_: Deno and TypeScript (bigger binaries,
slower start for hooks); a mix of both (the core could no longer live inside Orion).

The rewrite holds to four conditions:

1. Today's system stays the owner's daily tool until the Go version does the same things; nothing is switched off
   before.
2. The guards and the vault are ported test first: the existing test cases are translated into Go before the code that
   passes them.
3. Memory must reach today's quality: the scores of today's recall eval (`tests/recall`) are the bar the Go version has
   to clear before it replaces anything.
4. The SQLite library (pure Go, no C dependencies) is chosen first, with a speed test of memory search at realistic
   sizes, before the rest is written.

The work is split so that several sessions build in parallel on separate pieces.

### 9. Memory: SQLite is the truth, Markdown a mirror

Pages, revisions, links and indexes live in one SQLite database. A read-only Markdown copy, kept up to date, can be
browsed with any editor or Obsidian. The LLM Wiki lies in how the model curates pages, links and consolidation; search is
hybrid (words, meaning, fusion, rerank, graph spreading). The embedding and rerank models run as a separate process
next to the core, never inside the binary. They start as today's models, so the eval compares like with like; then a
light embedding model with no reranker is tried, and kept if the eval holds, which frees about 2 GB on a small host.
_Rejected_: Markdown files as the truth (two things to keep in
step, conflicts when several agents write at once).

### 10. One instance per person

The code has one owner. Kedalio for others means one isolated instance per person, run by the operator, plus the small
shared services. The same product runs standalone, on the operator's host, on a user's own server, or on a user's
computer. Moving from standalone to hosted is a planned path, not a restart. _Rejected_: one owner now and accounts
later (a long, risky rework); many accounts in one instance (complexity everywhere, very private data side by side).

### 11. Encryption at rest

Each instance's data is encrypted with its own key. By default that key is wrapped by the host's key, kept outside the
data volume and the backups, so restarts need nothing. This protects backups, a stolen disk, and lets a user's data be destroyed by destroying the key; it
does not protect against the operator of a running host, and the product says so. Full privacy means one's own server.

Because the backups cannot be read without the key, each instance key is also escrowed in a second place the owner
holds (a recovery code). Backups are consistent SQLite copies (online backup, not a file copy), kept off the host; a
RAID mirror is not a backup. A restore is tested on a schedule.

### 12. A container per run

Each unattended run starts a fresh container with its project's environment (`devcontainer.json`), and the container is
thrown away at the end. A small executor service, separate from the core, is the only piece allowed to start
containers. Before starting a run, the core checks free memory (minus a reserve for itself and the assistant), CPU and
disk against what the run needs: an estimate learned from the project's past runs. If the run does not fit, it waits in
a queue where requests for help and the owner's own requests go first. The owner may set a ceiling. On a small host
(4 cores, about 10 GB free) this means two runs at once, sometimes three.

The executor reads `devcontainer.json` through an allowlist: image and features yes; privileged mode, host mounts and
the Docker socket never. Each container has hard memory, CPU and time limits (cgroups), so a run that runs out of memory
dies alone and nothing else on the host does; network egress is limited to what the run needs. Images are built once
per hash of the project's environment and cached; package caches are shared volumes; old images are pruned under a cap.
_Rejected_: one shared container with a
sandbox per run (one toolchain for every project, runs disturb each other); micro-VMs (complexity for a risk that one
instance per person already keeps far).

### 13. Trust

Every proposal and its outcome (approved, edited, refused, undone) goes into a ledger, keyed by kind of action and by
object. The assistant reads the ledger and proposes to act alone; a yes from the owner becomes a written grant, listed
in the TUI and the app and revocable. A grant is suspended by itself when the owner undoes or disputes an action done
under it. The owner can also grant directly. Some actions can never be granted; [0003](0003-safety.md) lists them.

### 14. Delegation: one mechanism, the core places the work

A session asks the core for a worker on another profile or engine; the core decides where it runs. Host first, the
computer as a worker later, with the same interface. Native subagents on the same profile stay as they are. The model
for each kind of work comes from a simple default table. Advisors (a strong model consulted when a cheap worker is
stuck) are a direction of 0001, not part of the core: they come as an experimental skill.

### 15. One link between Orion and the host

Orion opens one outbound connection to the host and keeps it alive; messages flow both ways, behind any router and with
no port open on the computer. When the host is unreachable, Orion uses the vault fallback and queues writes (memory,
tasks) on disk, sending them when the link is back. Conflicts follow fixed rules:

- each write carries its own id, so a replay never applies it twice;
- each write names the revision it started from; if the page moved meanwhile, the write becomes a branch revision and
  the assistant proposes the merge;
- the host gives the order, not the computers' clocks;
- task fields are last-writer-wins one field at a time, with every change in the task's log;
- the TUI shows the queue's age and size, and warns when it grows.

Every write records the device that made it.

### 16. Skills on two levels

Project skills live in the project's repository (`.agents/skills/`) and travel with the code. The owner's and Kedalio's
skills live in the core's library, with revisions; Orion writes them in each engine's own format, and the assistant and
claude.ai read them through MCP. Skills fetched from outside enter the library only after a prompt-injection check, and
stay on trial until the owner promotes them.

### 17. Accounting

Raw events are kept from day one, so reports can be recomputed: project, client, profile, engine, model, tokens, cost,
share of the usage limits, kind of work, outcome. The owner's hours are active time (pauses over a threshold
do not count), the agents' hours are counted apart. Hours outside the engines come from the calendar, from commits made
by hand, and from what the owner tells the assistant. Every block of time has a type from the owner's list, suggested by
the AI; each week the assistant shows the summary for the owner to confirm or correct.

## What this means for today's code

Everything is rewritten in Go to this design. Today's code (server, host, companion, plugin, search, web app) stays
readable as a reference: the memory's ranking and its tuning, the vault's key scheme, the guards, the protocol's
lessons. Which pieces carry over and how is decided piece by piece in a separate document, before any code is written.

## Open questions

- Installing and updating: the core, Orion, the host modules and the app.
- Backups: where off the host, and how often.
- The Android app in detail.
- Listeners: the study decision 5 calls for.
- How trust looks in the TUI and the app.
