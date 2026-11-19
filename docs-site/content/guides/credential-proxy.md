---
title: "Credential proxy"
description: "Keep provider credentials on the proxy; connect clients with revocable tokens."
weight: 45
---

`term-llm serve proxy` forwards HTTP/SSE and opt-in WebSockets to fixed upstreams,
substituting server-held credentials. It runs independently of chat and provider
configuration. Saving routes does not probe vendors. Each client has its own token and
provider approvals; admin sessions never authorize forwarding.

## Quick start: local passkey administration

```sh
term-llm serve proxy
```

1. Open **http://localhost:8082** on the same computer. Use `localhost`, not an IP:
   passkeys bind to the hostname.
2. Run the printed `cat` command, paste the bootstrap token, name your passkey and
   enroll. The token expires ten minutes after startup; restart if it expires.
3. Open **Providers → Add provider** and choose a preset. The preset asks for only
   what it needs: sign in for ChatGPT, [sign in once for Claude Code](#set-up-a-shared-claude-subscription),
   [sign in once for personal Antigravity](#run-personal-antigravity-through-the-proxy),
   or add an API key for a service like Venice.
4. Open **Manage** on the new provider card, then choose **Approve access for**
   followed by the provider name. That gives an app or device access to every
   route the provider currently has. For Claude Code and Antigravity, use the
   device launcher instead, after the shared subscription is ready. Save the
   shown-once client token privately. In an app that accepts a custom API root,
   use the route URL and the client token, **not** the upstream credential. The **Clients** tab lets you see and manage
   which providers a client is approved for.

The listener defaults to `127.0.0.1:8082`. Keep its state directory across restarts.
Remote administration requires a stable HTTPS hostname and explicit enrollment.

## Providers and client approvals

**Providers** groups related routes on one card. The card heading names the
provider and is followed by one compact status. Each route line shows only the
local prefix and any condition such as **Disabled**. The card also shows one
approval summary. **Manage** holds the provider account, **Approve access for**
the provider, and each route's edit, enable or disable controls plus
provider-specific settings. Once any client is approved, including a legacy
partial approval, that control is labeled **Manage access for** the provider.
One connected account is shown once even when several routes use it.

Client approval applies to the whole configured provider. Approving a client
authorizes every route the provider has at that moment; it never authorizes a
different provider or a route added later. If you add a route to the provider or
change a route's upstream authority, access policy or credential rules, that
route needs approval again, and the client is marked as needing approval.
Existing clients created with per-route grants keep exactly the grants they
already have; they are not expanded automatically. Approve the provider when you
want those clients to cover all of its current routes. Removing approval removes
the client's grants for the provider's current routes; its token and approvals
for other providers stay unchanged.

Grouping never widens access and does not change routing. An **OpenAI API**
provider keeps its own API key and approval even when it targets
`api.openai.com` like the ChatGPT OAuth embeddings route. Existing `?tab=routes`
bookmarks still open **Providers**. Route names, URLs, and the public management
API are unchanged and remain route-based. Existing YAML works as is; to group
custom routes in a file, give them the same optional `provider` slug. In the UI,
use **Manage → Add route** to add a route to an existing provider.

## Global proxy settings and explicit native launch


1. Choose **Providers → Add provider → ChatGPT**, then **Sign in to ChatGPT**.
2. Open the OpenAI sign-in page from the proxy, enter the one-time code and
   approve the request. Return to the proxy. It stores one login and creates the
   ChatGPT routes together when their names and prefixes are available. No local
   OAuth credential file is required. Your OpenAI account must allow device-code
   sign-in.
3. Open **Manage**, then choose **Approve access for ChatGPT** to authorize an
   app or device for the provider's current routes. A successful provider
   sign-in does not give any new client access by itself.

Use **Manage → Reconnect ChatGPT** to renew the login without changing client
approvals. Use a distinct name when setting up a separate ChatGPT account. The
main route is enough for chat; the other routes cover usage, dictation,
embeddings, and realtime sideband, and one approval covers all of them. If a
route is added later, approve the provider again for clients that need it. A
ChatGPT subscription is separate from the **OpenAI API** key preset. To route
only ChatGPT through the proxy in term-llm, [configure the client allowlist](#using-term-llm-as-a-client)
with the canonical `/chatgpt/` route.

## Run native Claude Code through the proxy

Claude Code can opt in to proxy-held credentials. The launcher never modifies
ordinary `claude`: your regular `~/.claude` settings, credentials, hooks and plugins
are untouched, and a plain `claude` run continues to use its own local login.

### Set up a shared Claude subscription

Claude Code routes use a subscription that you sign in to as the proxy operator.
Clients share its limits, so only approve devices you trust.

1. Choose **Providers → Add provider → Claude Code** and click
   **Create shared route & get sign-in launcher**.
2. Copy the **Operator sign-in launcher command** and run it on your own device
   with an updated `term-llm`. Finish the Claude browser sign-in. The proxy holds
   the subscription credentials; workers do not get them. Keep the operator
   launcher private and do not give it to a worker.
3. Dismiss the one-time launcher after saving it securely. Use **Refresh** on
   **Providers** after finishing sign-in. When the route says **Shared Claude
   subscription ready**, open **Manage** and click **Add device**.
   Run that command on the worker with an updated `term-llm`. The worker uses the
   proxy’s shared sign-in without opening a browser. Issue a separate launcher
   for each worker so you can remove its access without signing out Claude or
   affecting other workers.
4. If the shared sign-in needs attention, open **Manage** and click
   **Get operator sign-in launcher** (or **Reconnect**) for the same
   route. Finish sign-in and refresh **Providers**. **Add device** does not sign
   in or reconnect the subscription, and is unavailable while it is not ready.

Remove a worker’s access from the provider’s **Client approval** section under
**Manage**, or revoke its client in **Clients**. Do not use **Delete provider**
to remove just one worker: it removes all Claude routes, grants and saved proxy
sign-ins.

### Optional: use the same connection with term-llm

The copied launcher command works without changing any configuration file. For
term-llm's `claude-bin` provider, copy the optional provider YAML shown with it.
For example, using a private token file:

```yaml {title="config.yaml"}
providers:
  claude-bin:
    proxy:
      url: https://proxy.example.test/claude-code/
      token: file:///run/secrets/claude-proxy-token
```

`url` is the Claude route root supplied with the launcher, not the bare proxy origin.
Set `token` to a credential reference for that client's scoped proxy token, not an
Anthropic credential. This provider-local block redirects only `claude-bin`.
If you also use a [top-level `proxy:` block](#using-term-llm-as-a-client), list
only the other providers there (for example, `providers: [chatgpt, venice]`).
Do not list `claude-bin` in both places.

```sh
term-llm proxy run claude
term-llm proxy run claude -- --resume
```

The launcher is opt-in per invocation. Supply both `--url` and `--token` to use a
different route root and scoped token for one run; they do not create a route or
provider approval.
Everything after `--` is passed to native Claude. Prefer a `file://` token reference
over a literal value on the command line.

Only the operator launcher signs in. The proxy intercepts the HTTPS token
exchange on `platform.claude.com` and encrypts the real OAuth tokens. It refreshes
the shared login and gives each client launcher a scoped placeholder, not a
reusable Anthropic credential.

The launcher sets a private, term-owned `CLAUDE_CONFIG_DIR` for the child, so normal
Claude state stays separate. The proxy CA is fetched for that launch and trusted
only through the process's `NODE_EXTRA_CA_CERTS`; it is not installed in the
operating-system trust store, and TLS verification is never disabled. Competing
vendor credentials, cloud auth switches and ambient proxy settings are removed
from the child environment. The launcher sets its own `HTTP_PROXY` and
`HTTPS_PROXY` for Claude and marks routing as host-managed so Claude settings
cannot replace them. The calling shell's proxy settings are unchanged.

The tunnel is not a general web proxy or MITM. Only `api.anthropic.com:443` and
`platform.claude.com:443` are accepted. The public startup checks `GET/HEAD
/api/hello` and `GET/HEAD /v1/oauth/hello` work before Claude sign-in; they still
require a granted proxy client, send no credentials upstream, and check the real
upstream response. Other platform traffic is limited to the fixed token endpoint.
On the API host, the proxy forwards validated API-host paths over the authenticated
native tunnel using the operator’s OAuth session, subject to the shared-session
endpoint restrictions and any route Allow rules. Anthropic still decides OAuth
permissions and response status. Platform token exchange remains limited to the
fixed token endpoint, and the public startup probes stay credentialless and
restricted to their fixed paths. There is no credentialless API pass-through or expansion to other
providers. Loopback and safe existing `NO_PROXY` entries stay direct so local tools
and MCP servers keep working; other HTTPS destinations sent through the proxy are
refused. Each connection lasts at most one hour. An intervening reverse proxy must
support HTTP/1.1 CONNECT; otherwise point the launcher at the credential proxy
listener directly. Revoking a client or grant closes its open tunnels. The worker
loses access, but the shared subscription and other workers stay connected. A
compromised worker can still use its granted route and isolated profile; this
is not an OS sandbox.

`term-llm usage -p claude-bin` uses Claude's native `get_usage` control request
through the same binding. There is no separate quota-query implementation, and it
does not fall back to local vendor credentials. The **Billing** selector only
categorizes spending records; it does not change login permissions, provider charges
or limits.

If an older release installed its connector settings into your Claude profile, use
`term-llm proxy disconnect claude` once to undo only the settings that connector
owned. The old connector and its browser-approval flow are retired; the native
launcher does not use them and never edits your Claude settings.

## Run personal Antigravity through the proxy

This setup is for the **Antigravity personal subscription**, not Antigravity
Enterprise and not the Gemini API key preset. The launcher requires Linux and
`agy 1.5.12` or newer. Version `1.5.12` was tested in isolation against synthetic
Google endpoints. Live subscription acceptance and macOS credential isolation
have not been verified. Use HTTPS for remote proxy URLs; plain HTTP is accepted
only on loopback.

1. Choose **Providers → Add provider → Antigravity personal subscription**, then
   **Create shared route & get sign-in launcher**. The route uses the fixed
   personal service `https://daily-cloudcode-pa.googleapis.com`.
2. Run the private **Operator sign-in launcher command** on your Linux device with
   an updated `term-llm`. This command ends in `-- acp --login`. Follow the prompt
   to sign in with your personal Google account. If `agy` prints a sign-in link,
   open it in your own browser. Do not share the operator launcher with workers.
3. Dismiss the one-time launcher after saving it securely. Refresh **Providers**
   after signing in. When the route says **Shared Antigravity subscription ready**,
   open **Manage** and click **Add device** to issue a separate
   launcher for each worker. Run it as shown, without `-- acp --login`. Workers
   use the shared subscription and its limits without signing in to Google.
4. Open **Manage** and click **Get operator sign-in launcher** or **Reconnect** if
   the subscription needs attention. Remove a worker’s provider approval or
   revoke its client token to remove only its access. **Manage → Delete provider**
   removes all Antigravity proxy routes, their grants and saved proxy sign-ins.
   Other providers, request history and your normal Antigravity setup are unchanged.

For term-llm's `agy-bin` provider, use the optional provider YAML shown with the
launcher. For example:

```yaml
providers:
  agy-bin:
    proxy:
      url: https://proxy.example.test/agy-bin/
      token: file:///run/secrets/agy-proxy-token
```

Use the worker’s route URL and scoped proxy token, not the bare proxy origin or
a Google credential. After saving this block, launch the worker with:

```sh
term-llm proxy run agy
```

The launcher isolates native credential state from normal `agy` use. The proxy
handles the operator’s OAuth exchange and refresh through its provider-scoped
tunnel: real Google OAuth tokens stay in the proxy vault, while the client
receives opaque tokens bound to its grant, route and account slot. The tunnel is limited to the personal
service, token exchange on `oauth2.googleapis.com`, and the specific user-info
endpoint `GET /oauth2/v2/userinfo` on `www.googleapis.com`. It does not proxy
arbitrary Google APIs or browser traffic. See [Forwarding behavior](#forwarding-behavior) for the worker
isolation requirements.

### Upgrade an existing native route

The proxy’s schema v8 upgrade keeps routes, client tokens, grants, and existing
shared Claude sign-ins. It does not turn any old per-device Claude Code or
Antigravity sign-in into a shared login. For each converted account, open
**Providers → Manage → Get operator sign-in launcher** on its route and complete one new operator
sign-in. Refresh **Providers** afterward. The operator sign-in restores access
for existing clients; workers do not need new tokens or a vendor login. New
worker launchers and approvals are unavailable until the shared route is enabled
and ready.

## Provision clients with an admin API key

In **Admin → API keys**, create a named key and save the token when shown.
Send the key as a Bearer token to `/_proxy/manage/v1/` over **loopback or HTTPS
only**. Keep it in a secret store; do not use it from browser code.

A key can create clients for **any** grantable route, including routes added
later. Any active key can list, regrant, revoke or purge API-created clients,
regardless of which key created them. Keys cannot see or change browser-created
clients, modify routes, access secrets, accounts, history, money, debug,
passkeys or native provider setup, authorize forwarding, or sign in to the browser UI.
Revoking a key stops provisioning immediately. Select **also revoke clients
created by this key** to revoke its clients too; otherwise those tokens keep
working under their existing grants.

Browser provider approvals apply only to browser-managed clients. The public
management API remains route-based: create and regrant API-created clients with
explicit route grants. Approving a provider in the browser never widens an
API-created client, and adding a route to a provider never adds it to a client
automatically.

Fetch the route names and current configuration `revision` first:

```sh
api=https://proxy.example.test/_proxy/manage/v1
curl -fsS -H "Authorization: Bearer $TERM_LLM_PROXY_ADMIN_KEY" "$api/routes"
```

Use that revision (replace `7` below) to create the client. Save the returned
`token` privately; give the app that client token, not the admin key.

```sh
curl -fsS -H "Authorization: Bearer $TERM_LLM_PROXY_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Build worker","grants":["venice"],"revision":7,"external_id":"build/worker-1"}' \
  "$api/clients"
```

To replace grants, `PUT /clients/{id}/grants` with `grants` and the current
`revision`. If the revision is stale (`409`), fetch routes again and check the
intended grants before retrying. To recover from a lost create response, look
up the client by `external_id`; the API cannot recover its token. Revoke the
active client (`revoked: false`) and recreate it with the same `external_id`. A duplicate active
`external_id` returns `409` with `client_id`.

```sh
curl -fsS -G -H "Authorization: Bearer $TERM_LLM_PROXY_ADMIN_KEY" \
  --data-urlencode 'external_id=build/worker-1' "$api/clients"
curl -fsS -X DELETE -H "Authorization: Bearer $TERM_LLM_PROXY_ADMIN_KEY" \
  "$api/clients/$client_id"
```

Use `DELETE /clients/{id}?purge=1` to remove an already revoked client. Purging
leaves request and money history intact.

## Provider presets and client settings

Choose the preset from **Providers → Add provider**. Keep provider sign-in and client
access separate: provider credentials let the proxy call the vendor, while
approving a provider lets a particular app or device call the proxy through each
of that provider's current routes. For a service
that uses an API key, you can select a saved key or add one while setting up the
route. The setup guide disappears after the first recorded request and stays
complete across restarts and history cleanup. Route YAML download and
replacement live under **Admin**, not Providers.

Presets supply editable creation defaults, not vendor guarantees or discovery.
Existing routes do not change when presets change. Available families include
API-key inference/media/search services, account integrations for ChatGPT, Grok and
Copilot, Bedrock signing, Ollama and configurable compatible servers. OpenCode Go
uses one consolidated route for Chat/Responses, Messages and Gemini; clients
retain its client/session headers. Bedrock requires the regional runtime matching
its account.

Use the route URL without duplicating the vendor version prefix. Client headers:

| Service | Client credential header |
| --- | --- |
| Most services, subscription accounts | `Authorization: Bearer <token>` |
| Anthropic API, OpenCode Go Messages | `x-api-key` |
| Gemini/Live, OpenCode Go Gemini | `x-goog-api-key` |
| ElevenLabs | `xi-api-key` |
| Flux | `x-key` |

Query-key auth is unsupported. Claude, personal Antigravity and Grok CLI gateway
paths have fixture evidence; Cursor and Vibe remain gated. Version/session headers come
from clients. Advanced route fields enable `websocket: true` and `usage: auto`.
WebSockets require HTTP/1.1 and have a one-hour cap; revocation closes live use.
For Responses WebSockets, **Requests** gets one row per observed response turn,
usually as soon as the upstream terminal event arrives. A reused connection
can therefore show several rows before it closes. The first saved turn replaces
the connection's handshake row so it does not double-count usage. Turns still
open when the connection closes are recorded as incomplete, with unknown
usage if there were no terminal counters. A connection with no identifiable turn
keeps its connection row. Turn details link to the shared connection capture
when debug recording was enabled. Previously recorded connection totals are
not split retroactively.

Venice routes using the official `https://api.venice.ai/api/v1` root enable
response token accounting by default, including older routes where `usage` was
unset. An explicit `usage: off` still disables it. The term-llm Venice client
requests streaming usage automatically. Other streaming clients must send
`"stream_options": {"include_usage": true}`; the proxy does not rewrite their
request bodies or invent missing counts. Existing unmeasured records cannot be
backfilled from responses that were not retained.


### OpenCode Go: one key, one provider, one approval

Choose the **OpenCode Go** preset and store one upstream OpenCode Go API key.
The parent route prefix is `/opencode-go/`; approve the OpenCode Go provider once
to cover that route.
All protocols use the same scoped client token, not the upstream key. Configure
term-llm's OpenCode Go native base URL as `https://proxy.example.test/opencode-go`.
Its protocol clients select these three API roots:

| Protocol | Client API root | Fixed upstream API root |
| --- | --- | --- |
| Chat Completions / Responses | `https://proxy.example.test/opencode-go` | `https://opencode.ai/zen/go/v1` |
| Messages | `https://proxy.example.test/opencode-go/messages` | `https://opencode.ai/zen/go` |
| Gemini | `https://proxy.example.test/opencode-go/gemini` | `https://opencode.ai/inference/go/v1` |

The parent rule `/` handles Chat/Responses; `/messages/` and `/gemini/` select
Messages and Gemini respectively. Native clients append their operation paths:
`/chat/completions` or `/responses`, `/v1/messages`, and
`/models/<model>:streamGenerateContent`. Each protocol uses the client credential
header shown above. No body conversion is involved; the OpenCode client/session
headers still come from the client.

Named term-llm providers of type `opencode-go` use their own parent namespace:
for example, `household` uses `/household`, `/household/messages` and
`/household/gemini`. Create and approve that provider explicitly; it does not
borrow the built-in OpenCode Go route or another named provider's subpaths.

**Existing sibling-route installations require explicit migration.** Older
`/opencode-go-messages/` and `/opencode-go-gemini/` routes are not silently moved,
merged or approved by changing presets. Review and explicitly replace the old
setup with the consolidated parent and its three rules, then reapprove the
provider and update client roots; remove obsolete sibling routes/grants.
Route authority/rule changes invalidate existing grants, so the provider shows
as needing approval until you approve it again. Global term-llm proxy
configuration now registers only the nested roots, so migrate the server and
reapprove before switching those clients. Explicitly manually registered legacy
sibling roots remain compatible when bound to the same client principal; missing
protocol roots fail closed rather than contacting a public vendor endpoint.

## Trusted accounts

Under **Providers**, open **Manage** on a provider and choose **Edit → Advanced**;
manual credential JSON is still supported for
`chatgpt`, `grok`, `copilot` and `aws-sigv4`. Use the guided sign-in for new
ChatGPT plan routes.
Select the account when creating its route. Credentials are write-only; metadata
contains state, mode and expiry. Account-managed headers cannot also have static
substitutions. Origins and API paths are restricted to that integration. Sign-out
clears credentials and closes streams; requests fail closed until credentials are
replaced. Claude Code and personal Antigravity use **Manage → Add device**
and **Manage → Delete provider**, not this account editor.
For either shared subscription, use **Get operator sign-in launcher** or
**Reconnect** to manage the operator sign-in.

For rotating accounts, move custody to the proxy: do not leave another process
refreshing the same token. Rotation is durably fenced and committed before use.
Uncertain post-connection issuer outcomes can require fresh login rather than
reusing a consumed token. Safe pre-connection failures back off. Inference is never
replayed automatically after a vendor 401. AWS supports static/session signing,
not automatic credential-chain renewal.

Operator-only startup import:

```yaml
accounts:
  home-chat:
    kind: chatgpt
    file: /run/secrets/chatgpt-account.json
```

The private regular file is limited to 128 KiB, read only when that account is absent.
After import the stored account is authoritative; remove the bootstrap file.
A different existing kind rejects startup. The Admin UI cannot introduce files.

## Using term-llm as a client

To proxy only built-in ChatGPT, add this top-level block to the client's
`config.yaml`. The proxy shows this example when you create a client for the
ChatGPT provider. Use the proxy origin, not the `/chatgpt/` route URL, and point
`token` at a private file containing the scoped client token.

```yaml
proxy:
  url: https://proxy.example.test
  token: file:///run/secrets/proxy-client-token
  providers: [chatgpt]
```

`proxy.providers` is an exact allowlist. `chatgpt` selects built-in ChatGPT and
its related features, including those that use `/chatgpt-backend/` (usage and
dictation) or `/chatgpt-openai/` (embeddings and realtime sideband). Approve the
ChatGPT provider once for the client; that covers every route the provider has
at approval time. If you add a route later, approve the provider again for
clients that need it. Other providers, including named providers of type
`chatgpt`, keep using their own endpoints and credentials.

- To proxy a configured named provider, list its exact configuration name. This
  selects only that provider, not other providers of the same type or their
  ancillary integrations. The named provider uses its own `/<name>/` route.
- To proxy a media-only integration, list its route ID, for example `flux`.
- `providers: []` selects nothing. Omit `providers` to keep the legacy behavior:
  proxy all supported integrations except those listed in `proxy.direct`.
  A nonempty `proxy.direct` conflicts with any explicit `proxy.providers` list,
  even an empty one.

Selected integrations use the scoped client token instead of resolving local
provider secrets, including when they previously used custom API roots. Built-ins
use their canonical route IDs. A missing route or grant never falls back to the
vendor. OpenCode Go uses one parent approval with nested `/messages` and
`/gemini` API roots.

In legacy mode, `direct: [opencode-go]` keeps the whole OpenCode Go protocol
family direct, including named providers of that type. A named provider's own
`direct` exception affects only that provider and all its protocols; legacy
sibling names alone cannot opt individual protocols out. Unknown names fail.
Complete custom chat `url` mappings fail closed: migrate to a compatible
`base_url` or leave that provider direct. Provider/doctor output describes the
configuration, not connectivity. The proxy token resolves at config load;
replacing it requires a worker restart because each proxy root has one caller
identity per process.

`claude-bin` is the term-llm provider name; `/claude-code/` is its canonical
proxy route. With `claude-bin` in the top-level `proxy.providers` list, term-llm
adds `/claude-code/` to `proxy.url` and uses the top-level `proxy.token`. That
client must be approved for the Claude Code provider. If your launcher uses a
different token or route URL (including a noncanonical shared route), use that
exact URL and token in
`providers.claude-bin.proxy` instead, and remove `claude-bin` from the top-level
list. Alternatively, run `term-llm proxy run claude` without flags to use the
binding already in your config.

## DNS SRV discovery

Use a `srv://` URL in client configuration to discover the proxy’s HTTPS host
and port through DNS. For example, set `proxy.url` to
`srv://_term-llm-proxy._tcp.example.test`. If the proxy is mounted under `/team`,
use `srv://_term-llm-proxy._tcp.example.test/team`; term-llm appends each
integration’s route beneath that prefix.

For `providers.<name>.proxy.url` or `term-llm proxy run claude` / `agy` with
`--url`, include the full scoped route path, for example
`srv://_term-llm-proxy._tcp.example.test/team/claude-code`.

Use a DNS zone you trust. Term-llm connects over HTTPS to the first result in
the resolver’s priority/weight order, and the certificate must be valid for
that target hostname. If discovery fails, the connection fails without falling
back to a vendor endpoint. The server’s `--public-url` still takes a concrete
browser origin, not an SRV URL.

## Usage and spending

Home charts **Cost/Tokens** over **Today/7/30 days**, grouped by total, client,
provider, or model. Hover, tap, or use the keyboard for values. The chart uses
your browser timezone: Today starts at local midnight, and longer ranges use
local calendar days. Currencies remain separate.

Only traffic through this proxy is counted. Missing evidence is not zero.
Provider token semantics are normalized without counting caches/reasoning twice;
legacy token history is recoverable only while original evidence remains.
The chart timezone does not change plan summaries or monthly alerts. Set their
timezone and the optional monthly alert under **Money settings**. Alerts warn;
they never stop requests.

Pricing uses the shared **models.dev** catalog, with exact provider/model matches
and published context tiers. Explicit operator rate cards override the catalog;
bundled first-party prices are an offline fallback. The proxy loads its snapshot
from `<state-dir>/pricing`, refreshes in the background (12-hour catalog TTL), and
keeps the last good snapshot if refresh fails. Request handling never fetches
prices. `TERM_LLM_OFFLINE` disables background refresh. Price details show the
source and retrieval date. Missing rates/counts stay unknown; no fuzzy matching,
automatic FX or invented invoice charges.

For Venice chat completions, the proxy uses `usage.cost` from the response,
including the final usage chunk of a stream. It adds `usd` and `diem` at
**1 DIEM = US$1** and rounds the sum once to the nearest millionth of a dollar.
For example, `{"usd":0,"diem":0.0002498}` records **US$0.000250**. This is a
provider-reported cost, not a token-price estimate. Raw payload capture is not
required, and previously recorded requests are not repriced.

Subscription activity is valued at the API rate when a stored **API-equivalent
estimate** is available. The Home Cost graph includes these estimates and shows
the plan portion in each bar's breakdown. They do not count as actual spending
or budget usage. Cache writes use the catalog's published default rate;
TTL-specific prices may differ, as noted in Price details. Unsupported service
tiers remain unpriced. Expand **Plan estimates at API rates** on Home for today
and month-to-date totals, coverage, and **Price details**.
Historic estimates keep their original price; old unpriced history is not
automatically repriced. Subscription fees and savings are not inferred; estimates
exclude taxes, discounts, credits and fees.

The request ledger shows model, client, normalized total input/output, cache-read
share, duration and price. If no model was reported, the row shows the method and
path. If token usage is unavailable, it shows bytes in and out when recorded.
Byte sizes are abbreviated; hover for exact counts. Dates and times use the
browser timezone. Expand a row for reported token counters and technical details.
Missing values are not zero. Total input includes
cached tokens exactly once across provider conventions. Repeated unmeasured HTTP
errors collapse within each page; expand ×N to inspect individual requests.
Measurement requires `usage: auto` and supported response evidence; arbitrary
protocols and responses without usage cannot supply exact counts.

In **Requests**, edit the fields under **Filters**, then choose **Apply filters**.
The count beside **Filters** describes the results on screen, not pending edits.
**Refresh** reloads the current page with the applied filters; **Clear** returns
to the first page of all requests. Visible Requests page 1 checks for changes
once a second using the applied filters. Unchanged results stay in place, and
changes wait while you inspect a request. Older pages and hidden tabs do not poll.
JSON export uses the applied query and includes
up to 1,000 matching metadata records, never bodies, query strings, arbitrary
headers or secret values. Admission commits before dispatch; incomplete outcomes
remain unknown. Requests default to 30-day retention; financial history is separate.
Path redaction cannot detect arbitrary secrets: avoid sensitive path identifiers.

## Configuration

UI-managed routes/secrets are editable. Secrets bind to one origin; referenced
secrets cannot be deleted. YAML import/export contains definitions/references,
not credentials, client tokens, passkeys or history. Only operator-authored files
may introduce private regular secret files (maximum 16 KiB, trailing newlines removed).

With `--config ./proxy.yaml`, routes are read-only; explicit reload validates and
activates atomically, retaining the old configuration on failure. Files are not
watched. Example:

```yaml
routes:
  - name: service
    local_prefix: /service/
    upstream: https://api.example.com/v1
    caller_auth: {header: Authorization, prefix: "Bearer "}
    set_headers:
      Authorization: {secret: service-key, prefix: "Bearer "}
    usage: auto
    billing: pay_as_you_go
    redirects: refuse
secrets:
  service-key:
    origin: https://api.example.com
    file: /run/secrets/service-key
```

Enabled defaults true. `stored: <name>` references an existing same-name secret.
HTTP upstreams need explicit `allow_insecure_http: true`. Prefixes must be canonical,
non-overlapping, non-root and end with `/`; admin/asset/health paths are reserved.
There is no interpolation, command execution or body rewriting.

Routes otherwise grant all methods/relative paths beneath their base, potentially
including management APIs. Use narrow credentials and optional `allow` entries
such as `POST /responses`. Account routes always enforce integration restrictions.
`redirects` is `passthrough` or `refuse`; WebSockets/usage default off. Billing defaults
unspecified; choices are `pay_as_you_go`, `plan`, `local`, `unspecified`.

Changing upstream authority/base, credential rules, account or access policy removes
existing grants for the affected routes, so clients need provider approval again.
Delete/recreate never restores grants or approvals. Disabling retains grants;
rotating a same-origin secret retains them. A replacement credential may have
broader vendor privileges: the proxy cannot infer those.

Provider approval is not part of route YAML. It is stored per client and expands
into route grants for the routes that existed when the approval was made. A
configuration reload or import that changes route authority can therefore leave
a client marked as needing approval.

### DNS SRV upstreams

A route’s `upstream` (or any subpath rule’s `upstream`) can be a DNS SRV URL,
for example `srv://_inference._tcp.example.com/v1`. The server selects the first
result in the resolver’s priority/weight order and connects to that host and
port over HTTPS, preserving the base path. Use a DNS zone you trust and a TLS
certificate valid for the discovered hostname. For example, if the record
points to `api.example.com` on port `8443`:

```yaml
routes:
  - name: inference
    local_prefix: /inference/
    upstream: srv://_inference._tcp.example.com/v1
    set_headers:
      Authorization: {secret: inference-key, prefix: "Bearer "}
secrets:
  inference-key:
    origin: https://api.example.com:8443
    file: /run/secrets/inference-key
```

Bind secrets to the concrete HTTPS origin, including any nondefault port, not
to the SRV record. The proxy checks that origin before activating a route.
Discovery runs on startup, when saving routes, and on operator file **Reload**,
not on each request or when rotating a secret. If discovery fails, the new
configuration is rejected and any active routes stay unchanged. If a new result
changes the origin, any configured secret or account must match it and clients
must be approved again. DNS changes alone do not move active traffic or
implicitly transfer credentials.

## Explicit setup and production administration

Passkey administration is default. For controlled enrollment:

```sh
install -d -m 0700 ./proxy-state ./proxy-secrets
(umask 077; openssl rand -hex 32 > ./proxy-secrets/bootstrap)
term-llm serve proxy --state-dir ./proxy-state \
  --public-url http://localhost:8082 \
  --passkey-bootstrap-token-file ./proxy-secrets/bootstrap
```

A bootstrap/recovery secret exchanges once within ten minutes of startup for a
five-minute enrollment grant. Remove explicit flags/files after enrollment;
automatic local bootstrap files are cleaned up. Enrollment is not a machine token
or permanent admin password. Do not reuse secrets on restart.

### Passkeys, sessions and recovery

Keep a spare passkey. Adding/revoking keys or signing out other browsers needs fresh
authentication; the final key cannot be removed. Sessions expire after 12 idle hours
or seven days overall and survive restart via private token hashes. Restart clears
pending ceremonies/grants, not keys or clients. Logout revokes its session.

If keys are lost, restart the same store with a fresh private
`--passkey-recovery-token-file`, choose **Recover access**, enroll and sign in.
Recovery invalidates sessions but retains old keys: explicitly revoke lost keys,
then remove the recovery flag/file. Routes and upstream credentials are unchanged.

### Explicit token compatibility mode

Legacy/local administration may use `--auth token --admin-token-file <private-file>`.
Rotate by replacing the file and restarting; eight-hour sessions also end on restart.
Passkey mode has no admin-token bypass, and incompatible auth flags are rejected.

### HTTPS termination and hostname routing

The listener has no built-in TLS. Remote deployments require a stable domain,
`--public-url https://proxy.example.test`, explicit enrollment and a trusted external
terminator. Route the whole hostname at its root; preserve Host and paths including
`/_proxy/`, client routes and `/healthz`. Mount paths/IP passkey origins are unsupported.
Keep the backend private; do not trust arbitrary forwarded headers.

```caddyfile
proxy.example.test {
    reverse_proxy 127.0.0.1:8082 {
        flush_interval -1
    }
}
```

Use real DNS/trusted certificates; `.test` is illustrative. Never disable TLS
verification. Preserve streaming/cancellation, allow request timeouts, and do not
add retries. Secure/HttpOnly/SameSite cookies, exact Origin/Host and CSRF protect
administration; network/rate limits still matter. Protect machine traffic with TLS.

## Forwarding behavior

Headers replace rather than append. Duplicate credentials are rejected. Client
auth, cookies, forwarding/rewrite/proxy and hop-by-hop headers are stripped;
upstream Host is fixed and Set-Cookie removed. Opaque bodies, escaped identifiers
and raw queries pass through. Usage-enabled routes request identity encoding;
unexpected encodings/extensions remain unmeasured. Redirects are never followed.
Traversal, controls, absolute targets, CONNECT and unsupported upgrades are refused
by ordinary forwarding. The constrained Claude Code and personal Antigravity
tunnels above are separate CONNECT paths that intercept TLS only for their fixed
provider hosts and require a matching route grant. This is not a general forward
proxy or TLS interceptor. A trusted upstream can echo
its own credential. Credential custody also requires separate worker
filesystems/identities and egress policy: routing settings alone are not an OS
sandbox.

## Storage and operations

State defaults to the user config directory's `term-llm/proxy`; override with
`--state-dir`. Run one process per store. `proxy.db` holds plaintext credentials,
client hashes/grants and history; `admin-auth/` holds passkeys/sessions. Native
Claude and Antigravity shared sign-ins use authenticated ciphertext keyed to
proxy-owned material, but the database is not a general encrypted vault. Use
0700 directories and 0600 files and owner-only Windows ACLs. No credentials are stored in browser storage. Protect
backups like credentials; use consistent SQLite backup or stop the process, not a
live DB-only copy. Include `claude-forward-ca-v2.pem` and `agy-forward-ca-v2.pem`
for the native integrations you use. Each private key is also needed to decrypt
that provider’s stored sign-in. Losing one requires a new operator sign-in for
that provider.
General ledger writes use WAL/NORMAL; credential custody uses FULL-synchronous
commits before returning tokens or dispatching refreshes.

Schema v5 replaced browser pairing with the native Claude vault. Schema v6 adds
admin API key hashes and provisioning metadata; existing clients stay browser-managed. The v5 binary rejects v6 for read-write use: stop
older writers before upgrading, and restore a protected backup to downgrade.
The proxy allows up to 1,024 active clients and keeps up to 4,096 client rows;
new clients can evict the oldest revoked rows without removing history.

```sh
term-llm serve proxy health --host 127.0.0.1 --port 8082
```

Health checks local readiness, not vendors. General `--debug-raw` and profiling
are refused; payload capture has a separate explicit opt-in below.
Defaults: 64 concurrent requests, 64 MiB body, 15-minute request deadline, 30-day request
history. CLI flags override these. Retention is batched; monitor disk. Shutdown
stops admission, drains for 20 seconds, then cancels remaining work.

### Debug payload capture

Capture is **off by default**. Open **Debug** in the signed-in proxy UI to enable
or disable it without restarting, and inspect captured request/response payloads.
Enabling requires confirmation that raw data may contain secrets. Disabling stops
capture, including active streams, without interrupting forwarding. Toggle changes
apply only until restart; startup follows the CLI options.

Without a directory flag, captures use `<state-dir>/debug-payloads`. To choose
another private archive and start with capture enabled, keep your other startup
options and add:

```sh
term-llm serve proxy --debug-payload-dir ./tmp/proxy-payloads
```

**Sensitive:** payloads contain unredacted prompts, replies, tool data and any
body-embedded secrets. A trusted upstream may echo credentials into its body.
Keep this directory out of source control, shared/synced folders and ordinary
backups. Capture uses private directories/files (0700/0600, owner-only Windows
ACLs), rejects symlinks and requires exclusive archive ownership. Do not enable
it routinely in production. Startup prints a warning; file I/O can affect latency.

Each dispatched request gets a directory named for its proxy request ID (also
returned in `X-Term-LLM-Proxy-Request-ID`), containing:

- `request.body` and `response.body`: binary-safe bytes consumed on the forwarding
  path; empty or unread streams may have no file.
- For upgraded WebSockets, `websocket-client.frames` and
  `websocket-server.frames`: raw wire frames, **not decoded JSON messages**.
- `manifest.json`: bounded metadata, response format, reported model/usage when
  available, and per-stream bytes seen/stored, EOF, truncation and error flags.
  While active, files may grow and the manifest is preliminary. `finished` means
  capture finalization, **not** proof of a complete payload; inspect those flags.

No authorization/cookie headers, arbitrary headers or query strings are archived.
Only bounded Content-Type/response Content-Encoding metadata is retained; bodies
are not redacted, decompressed, replayed or pre-read. Captures describe consumed
transport bytes, not proof that all upstream work reached the client. Capture
failures do not fail forwarding. Local pre-dispatch denials are not captured.
To inspect one request, expand its row in **Requests** and choose **View raw
payload**. The button appears when that request has stored body or frame data,
even if capture has since been turned off. Expanding the row checks availability;
it does not fetch payload bytes or turn capture on. Close the row to return to the
same filtered list. The signed-in **Debug** view also lists captures and shows
bounded previews, including manifests, HTTP bodies, and WebSocket frames. Frame
files open in **Decoded** view: client masking is removed, text and JSON are
readable, and frame headers show their wire offsets. Use **Raw bytes** to see
the original capture. Binary, compressed, and incomplete data are identified
rather than guessed at. Decoding is on demand; it does not change the archive.
Payloads are displayed as text, not HTML, and never saved in browser storage.
Only this fixed archive and allowlisted capture files can be inspected; this is
not a general log-file browser. Normal request exports still exclude bodies.
Debug inspection requires the same authenticated administrator session and
same-origin protections as other administration; capture changes require CSRF.

Defaults are **4 MiB per body/WebSocket direction**, **2,560 MiB (2.5 GiB) archive bytes** and
**1,000 request captures**. Override byte limits with
`--debug-payload-max-bytes` (1 byte–64 MiB) and
`--debug-payload-max-total-bytes` (32 KiB–4 GiB). The archive rolls: when a byte
or count limit would be exceeded, the proxy removes the oldest inactive captures
first, including captures kept across restarts. In-flight captures are never
removed. If there is still not enough room, capture can be skipped or truncated
without interrupting proxy traffic. The request ledger is unaffected. Disable
capture after debugging. Restart without `--debug-payload-dir` to leave startup
capture off.

### What is and is not verified

Synthetic upstreams, native-binary fixtures and client/browser fixtures cover scoped
routing, the launcher environment, CA handling, vault placeholders, token exchange
and refresh, cancellation, consent and charts. They do not prove live vendor
issuance, subscription acceptance, live native sign-in, billing or non-Linux
compatibility. In particular, Antigravity was tested with an isolated Linux
`agy 1.5.12` and synthetic OAuth, not with a real Google account. Cursor, Vibe and
exact custom-URL mappings remain gated. No invoice/refund, FX, plan-quota or hard financial-limit
support is claimed.

See the [deployment example](https://github.com/term-llm/term-llm/tree/main/examples/proxy)
for containers; state and upstream keys must stay off workers.
