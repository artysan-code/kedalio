# 0004 — What carries over from today's code

- Status: superseded by [0005](0005-memory-and-tasks.md); kept as history
- Date: 2026-11-03

## Context

[0002](0002-architecture.md) rewrites Kedalio in Go and keeps today's code (Deno and TypeScript: server, host,
companion, plugin, search, web app) as a reference, not a base. This record decides, piece by piece, what the Go
version takes from it.

- **Carry**: the logic is rewritten in Go much as it is, with its tests translated first where they guard safety or
  quality.
- **Redo**: the idea stays, its shape changes because of a decision in 0002 or [0003](0003-safety.md).
- **Drop**: not needed any more.
- **Later**: kept for a later phase, not part of MVP 1.

Today's code stays in the tree and in daily use until the Go version does the same things (0002, decision 8). It is
removed piece by piece as each Go piece replaces it; git keeps the history.

## The core

| Today                                                                     | Verdict | Why                                                                           |
| ------------------------------------------------------------------------- | ------- | ----------------------------------------------------------------------------- |
| Search (`packages/search`): words, meaning, fusion, int8 vectors, rerank  | Carry   | Decision 9; models stay a separate process                                    |
| Memory: wiki pages, revisions, recall with graph spreading, consolidation | Carry   | The heart of Kedalio; today's recall eval (`tests/recall`) scores are the bar |
| Tasks: pages in memory, refs, recurrence, stalled tasks, views            | Carry   | They already work as wanted                                                   |
| Vault: AES-GCM, wrapped data key, recovery code                           | Redo    | Same scheme, ported test first, extended to the whole instance (decision 11)  |
| Skills library: revisions, trial / active / lapsed                        | Carry   | "On trial" is decision 16                                                     |
| Kedalio's MCP registry                                                    | Carry   | In Go with the official SDK (decision 2)                                      |
| Permissions: profile, floor, evidence, proposals                          | Redo    | They become the trust ledger and written grants (decision 13, 0003)           |
| Engines: accounts, usage windows, prices                                  | Carry   | Profiles, limits and accounting need them                                     |
| Charts                                                                    | Redo    | They become accounting (decision 17)                                          |
| Identity: passkeys, OAuth for MCP clients, terminal login                 | Carry   | claude.ai and other clients connect through OAuth                             |
| Identity: several accounts per instance, invites, passwords               | Drop    | One owner per instance (decision 10)                                          |
| Database: migrations, backups, export                                     | Carry   | Backups as in decision 11; export becomes the Markdown mirror (decision 9)    |

## The assistant and the host modules

| Today                                               | Verdict | Why                                                                                     |
| --------------------------------------------------- | ------- | --------------------------------------------------------------------------------------- |
| Agent: chats, briefs, cards, sealed sessions        | Carry   | The assistant's module, now a warm session (decision 4)                                 |
| Proactive messages: reactions, "stop", night passes | Carry   | The base of the assistant reaching out first                                            |
| Slack inbox read in quarantine                      | Redo    | Level one of the listeners (decision 5); the tool-less reader is kept against injection |
| Scheduler, cron, webhooks, morning report           | Carry   | Webhooks feed the listeners                                                             |
| Google MCP (mail, calendar, drive, docs, sheets)    | Carry   | The integration served by Kedalio for the assistant (0001)                              |
| Coolify and Forgejo MCP inside the host             | Redo    | Custom MCP servers of their own, outside the core                                       |
| Runs: tree, budget, placement on hosts              | Later   | Decisions 12 and 14, MVP 2                                                              |
| Host process: one container with a sandbox per run  | Later   | Becomes the executor and a container per run (decision 12), MVP 2                       |
| Web push                                            | Drop    | FCM (decision 6); levels and quiet hours carry                                          |
| Voice transcription                                 | Later   | Dictation, MVP 1.5                                                                      |
| Call notes                                          | Later   | MVP 3–4                                                                                 |
| Protocol v5 with older versions accepted            | Redo    | One link redesigned in Go, no backward compatibility (decision 15)                      |

## Orion and the Claude Code plugin

| Today                                                       | Verdict | Why                                                             |
| ----------------------------------------------------------- | ------- | --------------------------------------------------------------- |
| Profiles: one home per engine, launchers                    | Carry   | They already work as described in 0001                          |
| Installing Claude Code and Codex per profile                | Carry   | Orion keeps each profile's software                             |
| Installing Claude Desktop                                   | Carry   | As an optional Orion module                                     |
| `orion run`, `orion login`, MCP children with secrets       | Redo    | The credential broker with devices and fallback (decision 3)    |
| Standalone mode                                             | Carry   | Decision 1                                                      |
| TUI in Ink, nine tabs                                       | Redo    | Bubble Tea, with the assistant's chat (decision 7)              |
| Hooks and guards                                            | Carry   | In Go inside the Claude Code adapter, ported test first         |
| Mods: compact rows, confirmations, secrets, status line     | Redo    | They stay JavaScript, rewritten to be quiet and readable (0001) |
| Side panel mod                                              | Later   | Rethought with the TUI                                          |
| Plugin skills: session start and end, debrief, limits, save | Carry   | Their text moves to the library (decision 16)                   |

## Everything else

| Today                                    | Verdict | Why                                                                               |
| ---------------------------------------- | ------- | --------------------------------------------------------------------------------- |
| Web app (Preact PWA)                     | Drop    | No web app (decision 7); nothing of it is kept                                    |
| Brand: logo, colours, tokens             | Drop    | A new identity comes later                                                        |
| Site                                     | Later   | Rewritten in full once MVP 1 is done                                              |
| Gate and scripts                         | Redo    | For Go (vet, lint, tests); the checks for personal data and commit messages carry |
| Recall eval (`tests/recall`)             | Carry   | The bar of decision 8, run against today's code first to freeze its scores        |
| Deploy: compose, Caddy, embedding server | Redo    | New images for the Go core, the executor and the models process                   |

## Open questions

- A new identity for Kedalio: name aside, logo, colours and tone, before the site is rewritten.
