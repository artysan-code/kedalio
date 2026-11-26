# Adding Kedalio to claude.ai

A Kedalio instance can be added as a custom connector in claude.ai, which makes it available in chats, Projects and
cloud Claude Code sessions. What a client like that may do is fixed by the server: see "Cloud clients" in
[architecture.md](architecture.md).

## Before

The instance must be reachable over https and started with `-public-url` (or `KEDALIO_CORE_PUBLIC_URL`) set to its
origin, for example `https://core.example.com`; that turns the OAuth server on. Without it only the static token works,
and claude.ai cannot sign in. The owner approves the connector's code on the server (see step 3); no passkey is needed.

## A public address with a tunnel

A tunnel gives the https address without opening a port on your router. Whatever you use, the address is the passkey's
OAuth issuer (and the relying-party ID of a passkey, if one is made), so it must never change once a client exists.

- Cloudflare Tunnel, for a trial only: a quick tunnel's `https://....trycloudflare.com` hostname changes on every run.
  Start `cloudflared` first to learn the URL, then the core with it as `-public-url`:

  ```sh
  cloudflared tunnel --url http://localhost:8787
  ```

  For real use take a named tunnel or another stable hostname.

- Cloudflare Tunnel, permanent (a domain on Cloudflare): `cloudflared tunnel login`,
  `cloudflared tunnel create kedalio`, `cloudflared tunnel route dns kedalio kedalio.example.com`, then
  `cloudflared tunnel run --url http://localhost:8787 kedalio` (or a service running the same). Set
  `-client-ip-header cf-connecting-ip` and `-trusted-proxies` to the tunnel's address (see the configuration table in
  the [README](../README.md#configuration)). With Docker Compose these are the commented lines in `compose.yaml`.
- Tailscale Funnel is an alternative: `tailscale funnel 8787` publishes the port on your tailnet's `https://....ts.net`
  address. `tailscale serve` keeps it inside your tailnet, which claude.ai cannot reach.
- A reverse proxy with TLS you already run works as well, for example Caddy:
  `kedalio.example.com { reverse_proxy 127.0.0.1:8787 }`.

Do not expose port 8787 as plain http. Without `-client-ip-header` every client behind the proxy shares one rate limit,
and the core says so in its log at start ([troubleshooting.md](troubleshooting.md#warnings-at-start)).

## Claude Code and the connector

Claude Code does not need the claude.ai connector. With the `kedalio-server` plugin it signs in to the same address, with its
own approval code, and gets the owner's full tools, not the cloud policy below:

```sh
claude plugin marketplace add artysan-code/kedalio
claude plugin install kedalio-server@kedalio --config server_url=https://core.example.com/mcp
```

Then run `/mcp`, pick `kedalio` and sign in. The claude.ai connector is for chats, Projects and cloud sessions.

## Add it

1. In claude.ai: Settings, Connectors, add a custom connector.
2. URL: the instance's origin plus `/mcp`, for example `https://core.example.com/mcp`. No client id or secret is
   needed: claude.ai registers itself (`/oauth/register`).
3. claude.ai opens the instance's sign-in page, which names the client and shows a code. The owner approves it on the
   server with `docker compose exec core kedalio-core approve <code>`, after checking that the command shows kind
   `cloud: claude.ai` and the address they expect; the page then finishes on its own. A cloud client's code is approved
   only with that command, never from a chat. Only a client registered with a claude.ai, claude.com or chatgpt.com
   callback is treated as a cloud client. The code lives ten minutes: if claude.ai's window gives up before, add the
   connector again and approve the new code.
4. Enable the connector in the chat or Project that should use it.

## What it can do

It sees 10 tools: the reading ones (`memory_list`, `memory_read`, `memory_recall`, `session_start`, `settings_get`,
`tasks_brief`, `tasks_get`, `tasks_list`), `tasks_save` (not `repeat` or `worked_by`) and `memory_write`, which can only
`append` or `remember` into the inbox. It cannot edit, move or delete pages. The server enforces this policy and
refuses every other tool even when a client calls it by name.

## Where what it writes shows up

Every inbox entry carries `why: cloud: from <client>, waits for the owner`. The nightly upkeep never files these:
`session_start` reports them apart as `inbox.from_cloud`, and the owner places them from a local session.

## Cost

The cloud `tools/list` is about 9.6 KB for 10 tools (budget 9990 bytes, pinned by `TestCloudToolsListStaysLight` in
`core/mcp/weight_test.go`), against about 13.4 KB for the 15 tools of a local client. Every session pays it.

## Removing access

Run the core's own subcommand on the machine that holds the data folder, with the same environment as the core
(`KEDALIO_CORE_DATA` or `-data`), as the core's user (the image's default with `docker exec`). In a container:

```sh
docker exec <container> kedalio-core clients list
docker exec <container> kedalio-core clients revoke <client-id>
```

`list` shows each client's id, name, redirect host, kind (`cloud` or `local`), creation time, last use and how many
tokens are still live; never a token value. `revoke` deletes all the client's access and refresh tokens and the client
itself: its next call is refused, and to come back it has to register again and be approved again. Also
remove the connector in claude.ai, or it will keep asking to sign in. The core need not stop; a core still locked
(no key file in the data folder) cannot be reached this way.

The static `KEDALIO_CORE_TOKEN` is not a client and `revoke` does not touch it: rotate it by redeploying with a new value.

Left alone, access tokens last one hour and refresh tokens 30 days, and a client with no live token for 90 days is
forgotten.

## Tokens for headless agents

An agent that calls `/mcp` with `Authorization: Bearer <token>` and cannot sign in gets a named token with the cloud
policy (the reading tools, `tasks_save`, and `memory_write` into the inbox only). It works with or without
`-public-url`:

```sh
docker exec <container> kedalio-core tokens create alice -policy cloud
docker exec <container> kedalio-core tokens list
docker exec <container> kedalio-core tokens revoke alice
```

`create` prints the token once; only its hash is kept, so a lost token is revoked and replaced by one with a new name.
Its writes are recorded as `token:alice`. `list` shows name, policy, creation, last use and revocation, never a token.
A revoked token's next call is refused, and its name is never reused. The names `owner`, `upkeep` and `client` are
reserved.

## Limits

- Revoking is a command on the server, not a button in a page.
- Cloud clients cannot read or write anything the policy does not list; there is no per-client setting.
- Anyone may register a client (RFC 7591), but nothing is issued until the owner approves its code. A code can be relayed by someone who starts their own flow: approve only a code you started (see [SECURITY.md](../SECURITY.md)).
- ChatGPT uses the same path with its own callback; it is not covered by this page.
