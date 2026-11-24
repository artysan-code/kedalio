# 0003 — Safety

- Status: superseded by [0005](0005-memory-and-tasks.md); kept as history
- Date: 2026-11-03

## Context

The assistant of [0001](0001-kedalio.md) holds the three things that together make an agent dangerous: the owner's
private data, text written by other people (mail, chat, web pages, fetched skills), and actions that reach outside
(sending, sharing, inviting). Text from others can be written to steer a model, and no model resists that reliably.
The worst case is not one bad action but a lasting one: a mail convinces the assistant to propose a listener or a
grant that looks harmless, the owner says yes, and from then on the attack runs in silence. Agents that merge on their
own carry a second risk: a branch can weaken the very checks that judge it.

This record names the few lines that hold whatever the trust ledger of [0002](0002-architecture.md) says.

## Owner-only actions

Kedalio takes no capability away from the owner, but a few actions are the owner's alone. The assistant may propose
them; it can never do them under a grant, however long its record. Each one needs the owner's explicit confirmation,
every time:

- creating or changing a listener;
- granting or changing a grant;
- touching the vault: exporting it, adding or replacing a credential;
- sending anything to a recipient never written to before, even inside an existing grant.

The list is enforced by the core, outside the model: no prompt, skill or setting written by an agent can shorten it.

## Text from others

- When a proposal comes from a context that holds text written by someone else, the owner sees the literal rule,
  grant or message to be sent, not the model's summary of it, and where it came from ("from reading the mail of …").
- Memory pages written from mail, chat or the web carry their provenance, so the assistant never treats them as
  something the owner said.
- Skills fetched from outside stay on trial ([0002](0002-architecture.md), decision 16) and have no access to the vault
  until the owner promotes them.

## Protected paths

An agent merges into `dev` on its own only when its task is well defined, CI is green, and its branch touches none of
these:

- CI and its workflows;
- the checks: hooks, the gate's scripts, security tests;
- dependencies: lockfiles and new packages;
- the code of the vault, credentials and access.

A branch that touches one opens its pull request and waits for the owner. CI always runs with `dev`'s configuration,
never with the branch's own.

## Where the other defences live

In [0002](0002-architecture.md): enrolled devices with their own revocable keys and an expiring credential fallback
(decision 3); scoped, short-lived secrets for each run and for the assistant (decision 3); what the executor accepts
from `devcontainer.json`, and each container's limits (decision 12); the record of the device behind every write
(decision 15).

## Open questions

- The prompt-injection check for fetched skills: which tool.
- How to test the assistant against injection: a set of hostile mails and messages it must not act on, run before each
  release.
