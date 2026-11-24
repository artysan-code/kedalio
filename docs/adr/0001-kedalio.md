# 0001 — Kedalio

- Status: superseded by [0005](0005-memory-and-tasks.md); kept as history
- Date: 2026-11-03

## What Kedalio is, and for whom

Kedalio makes any AI agent better at working for one person. It does two things.

- **It makes skills, hooks and MCP servers standard and working**, set up the best way they can be, and adds **memory
  and tasks** that any model can use. A coding session on the owner's computer starts knowing the project, the wider
  picture and what is waiting, and ends leaving everything another computer needs to carry on.
- **It gives the owner an assistant**, with a name the owner chooses. The assistant knows the owner's projects, memory,
  mail, chats and calendar, acts on them, and reaches out first: it is an assistant for the owner's whole life, not
  only for code.

On top of that come the presets: a way of working (skills, rules, defaults) that ships with Kedalio and that anyone
can change.

It is for one owner at a time, self-hosted on the owner's own host. Later it will also be offered as a hosted service.

## Principles

1. **Any model.** Kedalio improves whatever model sits behind it: Claude through an API or a subscription, Codex,
   OpenRouter, others. Memory, tasks, skills and context are not tied to one vendor.
2. **No prohibitions, only confirmations.** Kedalio never takes a capability away from the owner; it only decides what
   an agent does alone and what it asks first. A few actions are the owner's alone and are never delegated
   ([0003](0003-safety.md)).
3. **Judgement before rules.** Fixed links (this project uses that account) are hints for the AI, not constraints. The
   owner can still set rules of their own (quiet hours, "not before an exam"), and the agent keeps them.
4. **Trust is earned.** Actions start with a confirmation. The agent keeps a record of how often it was right and
   proposes to act alone when the record supports it; the owner can also grant it directly. Trust is held per kind of
   action (sending an invite) and per object (reading this chat).
5. **With a host, nothing needed lives only on one computer.** Whatever is needed to carry on lives on the host or in
   the repository. A computer may keep caches and queues, but they are never the truth. Without a host, Kedalio runs
   standalone on one computer and nothing is synchronised.
6. **Efficiency first.** Cheap models do the work, expensive ones advise when something goes wrong; work runs in
   parallel and spends as few tokens as it can.
7. **Kedalio connects, it does not host.** MCP servers of other products live with those products; Kedalio knows them
   and hands them credentials. The few it serves itself are the ones the assistant cannot do without.
8. **Nothing personal in the product.** The repository holds generic rules and presets about how to work, never what
   one owner works on: no projects, people, accounts or hosts.

## The pieces

- **Kedalio** — the whole product.
- **Orion** — the engine on the owner's computer: it gives Claude Code, Codex and the like their context, credentials
  and MCP servers, and holds the management TUI.
- **The host** — the owner's own server: memory, tasks, the vault, the assistant and the agents that work unattended.
- **The Android app** — native, in Kotlin: the assistant in the owner's pocket.
- **The assistant** — the owner's agent, named by the owner, running on the host.

## The working session on the computer

Sessions are opened as they are today (a terminal, a desktop app); Kedalio makes them better through hooks, skills
and MCP servers.

- **At start**: minimal context of the project, a fetch and pull, the few checks needed to answer the first request,
  the project's memory and tasks, and the wider picture (deadlines and urgent things in other projects, as the
  assistant sees them). It names the tasks waiting. Given nothing to do, it proposes; given a task, it does it.
- **During**: it saves decisions, facts and the path taken as they happen, so nothing is lost and the work can be
  accounted for.
- **At the end**: tasks and memory are brought up to date, so that cloning the repository on another computer gives
  the full context to continue.
- **Accounting**: hours worked, per project and per client; tokens per kind of work; cost; share of each profile's
  usage limits. First for the owner, then for clients and invoices.
- **First skills**: start and end of a session (mainly hooks), project design (versatile, not one template), setting
  up and using MCP servers well. Merge and release rules belong to each project, not to Kedalio.
- **What Kedalio shows in the session is quiet and well made.** The mods that change how Kedalio's work appears in
  the engine fold a run of similar actions into one short line ("deleted 35 pages from memory ✓"), with the details
  one keystroke away; they never print internal state ("module started", "0 tools from the cache", "catalogue not
  updated"); a problem is shown once, in plain words, with what to do.
- **More skills** are written by hand, or fetched from a list of sources the owner keeps; each one fetched is checked
  for prompt injection before it is used.
- **Delegation across profiles**: like subagents, but a session on one profile can hand work to workers on other
  profiles and engines, to spread limits and to pick the right model for each piece; advisors brought in on errors
  come later, as an experiment. The same mechanism serves the assistant when it sends agents to a project.

## Integrations

Three kinds of MCP server:

1. **Served by Kedalio** — few, the ones the assistant cannot do without. Google first: Kedalio's verified Google app
   by default, the owner's own app as an option.
2. **From a template** — official servers (a database, a cloud) installed natively per project by a skill. Any server
   the owner finds can become a template and then be changed freely.
3. **Custom** — the MCP servers of the owner's own platforms, and non-essential ones the owner adds with an address
   and a credential.

Every service can have several accounts, used by context. The owner picks the scopes: the account's permissions and
which tools of a server are on. **All credentials live in Kedalio's vault on the host.**

What the assistant reaches from the phone: mail, calendar, Drive and Docs, contacts, chat (Slack), weather and maps,
the web, the state of projects and sessions, and custom servers. Developer services (databases, clouds) stay on the
computer; the assistant knows their state but does not act on them. Communication services follow one default: read
and manage, propose to write, act alone once trusted.

## The assistant

- **An assistant, not a worker.** It plans the owner's life with them, questions the schedule ("move this call, you
  need time to study"), brainstorms, reads several things at once, makes the shopping list. Project work goes to
  agents.
- **It works while the owner is away.** It sets its own wake-ups, reminders, tasks and calendars, and proposes its
  own listeners (a new mail, a new message, ones it invents), which the owner approves.
- **How it reaches the owner**: the app first, with notifications; for something urgent, a mock phone call that opens
  a voice conversation; Telegram or WhatsApp when the app goes unanswered or the context calls for it.
- **When to interrupt** is learned (work, study, training, appointments), within quiet hours and the owner's rules.
- **Privacy**: reading private chats sits at the lowest trust. The assistant says which chat it is about to read; the
  owner can approve chats for good.
- **Talking**: a chat like a coding session, with dictation; then a turn-based voice mode; then full conversation.

## The TUI

All management on the computer: profiles, MCP servers and accounts, settings, projects, and the traceability of what
happened (the accounting above). A **profile** is one account of one engine, with its login, limits and settings; a
session starts on one profile, its workers may run on others. Near a limit, Kedalio warns and moves the work to
another profile, judging when as it learns.

## The host and unattended agents

- Agents run on the host, in isolated containers.
- They start on request, on a schedule, and, as trust grows, on the assistant's own initiative.
- They work on a branch of their own and open a pull request; when the task is well defined and CI is green, they
  merge into `dev` on their own, except for the protected paths of [0003](0003-safety.md), which wait for the owner.
- Each run leaves a report in its task and a memory of what was done, and the assistant tells the owner.
- **A project that needs help**: a signal (CI failing, a service down, errors in production) sends an agent to look;
  the owner gets a report with what happened and a fix.

## The Android app

At first: the chat with the assistant, notifications, approvals and a view of what sessions and agents are doing, the
day and its tasks, profiles and their limits. Right after: voice, the mock call, reading WhatsApp notifications. It
grows into a full remote control of any session on any profile.

## Phases

- **MVP 1** — what the owner uses every day, as early as possible: the core (memory at today's quality, tasks, vault,
  skills, the Kedalio MCP) standalone and on the host; Orion with the Claude Code hooks and mods done well, the credential
  broker and the TUI with the assistant's chat; the assistant on the host reading mail, calendar and chat, drafting with
  confirmation, a morning message and a few listeners the owner approves, with the trust ledger recording from day one
  (proposals to act alone come once there are weeks of data); the Android app at first; accounting events.
- **MVP 1.5** — the mock call and voice, WhatsApp notifications and the Telegram or WhatsApp channel, the Codex adapter,
  the Markdown mirror of memory, the weekly hours summary and client reports.
- **MVP 2** — agents sent to projects by the assistant or a session; the owner's computer as an extra worker; full
  remote control.
- **MVP 3–4** — full voice conversation, listening to calls and taking notes, controlling the computer: one assistant
  on the phone, the computer or anything else, whatever model is behind it.

## What Kedalio is not, and what comes later

- Not an IDE, and not a replacement for Claude Code or Codex: it makes them better.
- Not a model.
- Not an agent framework for building other products.
- Not a host for other products' MCP servers.

Later: Kedalio as a hosted service for others; one of the owner's computers acting as the host.

## Open questions

- **WhatsApp**: notifications on the phone first; is there a way to reach full chats without risking the owner's
  number?
- **Prompt-injection check** for fetched skills: which tool (a known one from NVIDIA is to be found and assessed).
- **Efficient agent work**: research how others organise cheap workers, advisors and parallel runs.
- **Remote control**: when the full version lands.
- **The project-design skill**: its first draft was deduced from real projects and must be made generic before it
  enters the repository.
