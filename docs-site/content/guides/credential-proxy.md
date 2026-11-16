---
title: "Credential proxy"
description: "Keep upstream API keys on the proxy and give each client its own revocable token."
weight: 45
---

`term-llm serve proxy` forwards HTTP requests to configured upstream services,
replacing client tokens with server-held credentials. Each client gets a token
and access to selected routes. It runs independently of chat, agents and provider
configuration; setup never sends test requests to upstream services.

## Quick start: local passkey administration

Run this on the machine where you will open your browser:

```sh
term-llm serve proxy
```

1. Open the URL printed at startup: **http://localhost:8082** by default. Use
   **localhost**, not `127.0.0.1` or an alias; passkeys are bound to the hostname.
2. Run the printed `cat` command in another terminal. Paste its output into
   **Bootstrap token**, enter a **Passkey name**, and choose **Create first
   passkey**. The browser will ask you to verify your identity.
3. Under **Routes → Add route**, choose a **Provider**. Select a stored secret or
   choose **Add API key**, then **Save route**. For other services, choose
   **Custom HTTP** and enter the upstream URL and authentication headers.
4. Under **Clients**, enter a **Client name**, select its permitted routes, and
   choose **Create client**. Save the token: it is shown only once.
5. Set your application's API root to the route's local URL and its API key to
   the **client token**, not the upstream key. The client page provides curl
   examples using the route's required authentication header.
6. Open **Requests** to inspect traffic.

The listener defaults to `127.0.0.1:8082`. Keep the same state directory on
restarts to retain configuration, secrets, clients, passkeys and browser sessions.

The local bootstrap token expires 10 minutes after startup. If setup expires,
restart for a fresh token and follow the new printed command. Once enrolled,
startup no longer creates a bootstrap token. Remote deployments require
[explicit enrollment setup](#explicit-setup-and-production-administration) and
[HTTPS termination](#https-termination-and-hostname-routing).

## Provider presets and client settings

Presets fill editable URLs, authentication headers and credential substitutions;
they do not change the forwarding protocol. **Add API key** stores the key
immediately without losing your route draft. **Save route** is a separate step
and does not grant client access. Existing routes do not change when presets
change.

Available services include OpenAI, Anthropic, Gemini, OpenRouter, xAI, Venice,
NEAR AI, SambaNova, Obscurify, OpenCode Zen, ElevenLabs, Jina, Voyage AI and
Mistral transcription. Ollama and vLLM are templates: supply the actual server
URL; plain HTTP requires explicit acknowledgement. OpenCode Go offers **Chat /
Responses** and **Messages** profiles, which still require client/session
attribution headers from the client.

Use the local route URL as the replacement API root; do not append the vendor's
version path again. Client authentication follows the route's configured header:

| Service | Client token header |
| --- | --- |
| Most compatible clients | `Authorization: Bearer <token>` |
| Anthropic | `x-api-key: <token>` |
| Gemini | `x-goog-api-key: <token>` |
| ElevenLabs | `xi-api-key: <token>` |

Clients must support changing their destination URL and authentication header.
Query-key authentication and WebSockets are unsupported; HTTP/SSE is supported.
Version and session headers still come from the client. Presets do not discover
keys from term-llm configuration or the environment, perform OAuth/CLI login,
or implement Bedrock signing, FLUX workflows or OpenCode Go's Gemini protocol.

## Requests and durability

**Requests** has numbered pages of **25 requests**, a matching-row count, and
controls above and below the list. Expand **Filters** to search by client,
route, method, path, outcome, local time or request ID, or export up to **1,000**
matching records as JSON. Path searches accept a relative path or the full local
route path. Select a row for details. New traffic and retention cleanup can
shift page boundaries between visits.

History contains paths, client/route identifiers, status, timing, body-byte
counts, peer IP and an optional configured upstream request ID. It contains no
bodies, query values, arbitrary headers, model/token counts or costs. Generated
request IDs are also returned in `X-Term-LLM-Proxy-Request-ID`.

Retention defaults to **30 days** (`--retention-days`). Known credentials are
redacted from paths, but arbitrary secrets embedded in path identifiers cannot
be reliably detected. Avoid APIs that put sensitive values in paths.

A history row must commit before forwarding; if that fails, the proxy returns
503 without contacting the upstream. Incomplete records remain **unknown**, not
successful. Errors, timeouts, byte counts and interrupted streams do not prove
whether upstream work or billing occurred. The proxy never retries or replays
requests automatically.

## Configuration

A route grants access to **all ordinary methods and relative paths under its
upstream base URL**, potentially including management or billing APIs. Use
narrowly scoped keys and base paths. There are no per-path/method policies or
automatic direct-provider fallbacks.

### UI-managed (default)

Routes and secrets are editable in the browser. Secrets are write-only: replace
them to rotate a key; they cannot be read back. Each secret is bound to one
upstream origin (scheme, host and effective port). Use a new name for a different
origin. Referenced secrets cannot be deleted, even when the route is disabled.

Route names are immutable access-control identities. Changes affect grants as
follows:

| Change | Existing route grants |
| --- | --- |
| Upstream origin/base path or outbound header rules | Removed; review and regrant in **Clients** |
| Delete a route, including delete-and-recreate | Removed permanently |
| Disable a route | Kept for re-enabling |
| Local prefix, client authentication format or request-ID setting | Kept |
| Rotate a secret's value under the same name and origin | Kept |

A replacement key may have wider privileges; the proxy cannot infer that from
its value. Revoking clients, removing grants or disabling/deleting routes blocks
**new requests only**. Already dispatched work can continue until completion,
cancellation or timeout. Grant edits do not reissue client tokens.

Under **Routes → Configuration file**, **Download routes.yaml** exports route
definitions and secret references. To replace routes, choose a YAML file (up to
1 MiB) or **Paste YAML instead**, then **Replace routes from YAML**. This is not
a backup: it excludes secret values, client tokens, passkeys and history.
Concurrent edits are revision-checked; refresh after a conflict.

### Operator-managed (`--config`)

After enrolling the administrator, start with an authoritative route file:

```sh
term-llm serve proxy --state-dir ./proxy-state \
  --public-url https://proxy.example.test --config ./proxy.yaml
```

The route UI becomes read-only; Clients, Requests and stored-secret rotation
remain available. **Reload file** validates the whole file, reads its secret
files, then activates it atomically. Invalid reloads keep the previous valid
configuration. Files are not watched or rewritten automatically.

Switching modes or reloading removes grants for missing routes or changed
upstream authority. Returning to an older configuration does not restore those
grants; review **Clients** and regrant explicitly.

Only operator-authored files may introduce file-based secrets. Sources must be
private regular files, at most 16 KiB, with an explicit matching upstream
`origin`. They are read at activation, not per request; trailing CR/LF is removed.
Admin tokens, enrollment/recovery secrets and passkey state—including hardlink
aliases—cannot be used as upstream secrets.

### Configuration example

```yaml
routes:
  - name: service-a
    enabled: true
    local_prefix: /service-a/
    upstream: https://api.service-a.example/
    caller_auth:
      header: Authorization
      prefix: "Bearer "
    set_headers:
      Authorization:
        secret: service-a-key
        prefix: "Bearer "
    request_id_header: X-Request-ID

  - name: service-b
    enabled: true
    local_prefix: /service-b/
    upstream: https://api.service-b.example/unrelated/base/
    caller_auth:
      header: X-API-Key
    set_headers:
      X-Other-Credential:
        secret: service-b-key
      X-Client-Label:
        value: local-proxy

secrets:
  service-a-key:
    origin: https://api.service-a.example
    file: /run/secrets/service_a
  service-b-key:
    stored: service-b-key
```

These hosts are examples. Create `service-b-key` in the UI before switching to a
file that references it. A `stored` reference must use the stored secret's own
name; an optional declared `origin` must match its binding. Missing credentials
for enabled routes reject startup or reload. Export never includes secret values;
import through the UI cannot introduce file sources.

`enabled` defaults to true. The existing `caller_auth` field configures **client**
authentication and defaults to `Authorization` with prefix `Bearer `. Each header
substitution accepts either `secret` with an optional `prefix`, or a nonsecret
literal `value`. There is no interpolation, command execution or body rewriting.

Upstream URLs must use HTTPS unless `allow_insecure_http: true` is explicitly
set for trusted private/test traffic. URLs cannot include userinfo, queries or
fragments. Local prefixes must be canonical, non-root, non-overlapping paths
ending in `/`; admin, asset and health paths are reserved. Limits are 128 routes,
256 secrets, 32 substitutions per route and 1 MiB of configuration. Host, framing,
hop-by-hop and proxy diagnostic headers cannot be substitution targets.

## Explicit setup and production administration

`--auth passkey` is the default. Loopback listeners (`127.0.0.1`, `::1` or
`localhost`) infer `http://localhost:<port>` when `--public-url` is omitted.
Non-loopback listeners require `--public-url`. Remote administration requires a
stable HTTPS domain origin and an explicit bootstrap file, even if the backend
listener is loopback. IP origins and public URLs with mount paths are rejected.

To manage the initial bootstrap file yourself:

```sh
install -d -m 0700 ./proxy-state ./proxy-secrets
(umask 077; openssl rand -hex 32 > ./proxy-secrets/bootstrap)
term-llm serve proxy --state-dir ./proxy-state \
  --public-url http://localhost:8082 \
  --passkey-bootstrap-token-file ./proxy-secrets/bootstrap
```

Open **http://localhost:8082/_proxy/**, paste the file's contents into **Bootstrap
token**, name the passkey and choose **Create first passkey**. For remote use,
replace the public URL with your HTTPS origin and configure the terminator
[below](#https-termination-and-hostname-routing).

Bootstrap and recovery secrets can each be exchanged **once within 10 minutes
of startup** for a **five-minute enrollment grant**. Retry a cancelled browser
prompt while the grant is valid. If it expires or its cookie is lost, restart:
automatic local setup generates a new token; explicit setup needs a fresh secret
file. Old explicit files are rearmed on restart, so do not reuse them.

Automatic local bootstrap files are removed on clean shutdown. Explicit files
are operator-managed: after enrollment or recovery, remove the flag and securely
remove the file. Normal startup needs neither. A bootstrap flag cannot enroll
another key once credentials exist. Enrollment secrets are not admin passwords
or client tokens; keep them out of URLs, browser storage and source control.

### Passkeys, sessions and recovery

In **Passkeys**, add a spare key and name devices clearly. Adding or revoking a
key, or choosing **Sign out other browsers**, requires a fresh passkey assertion.
The final key cannot be deleted. Revoking a key invalidates its sessions;
**Sign out other browsers** keeps the current browser signed in.

Passkey sessions expire after **12 idle hours** or **seven days** overall. Logout
revokes the server session immediately. Like Hub, browser sessions survive
restarts using a private `admin-auth/sessions.json` file containing only session
token hashes, not raw tokens. Restart clears pending enrollment grants, passkey
ceremonies and fresh-auth approvals, but not passkeys, client tokens or route
grants.

If all enrolled devices are unavailable:

1. Create `./proxy-secrets/recovery` with a fresh private 256-bit random secret.
2. Restart the same state directory with
   `--passkey-recovery-token-file ./proxy-secrets/recovery` instead of the
   bootstrap flag.
3. Choose **Recover access**, enter the **Recovery token** and a **Passkey name**,
   then choose **Recover with new passkey**.
4. Sign in with the new key. Recovery invalidates every session but leaves old
   keys enrolled; review **Passkeys** and revoke lost keys. Remove the recovery
   flag/file after use.

Recovery requires an already enrolled store and does not change routes,
upstream credentials or client tokens.

### Explicit token compatibility mode

For intentional local/legacy administration without passkeys:

```sh
install -d -m 0700 ./proxy-state ./proxy-secrets
(umask 077; openssl rand -hex 32 > ./proxy-secrets/admin)
term-llm serve proxy --auth token --state-dir ./proxy-state \
  --admin-token-file ./proxy-secrets/admin
```

Open **http://127.0.0.1:8082/_proxy/** and enter the **Administrator token**.
Sessions expire after eight hours or on restart. To rotate the token, replace its
private file and restart. Passkey mode rejects `--admin-token-file`; token mode
rejects passkey enrollment/recovery flags. There is no silent fallback, and admin
credentials never authorize forwarding.

### HTTPS termination and hostname routing

The proxy has **no built-in TLS**. Put an externally managed TLS terminator in
front of the private listener and set `--public-url https://proxy.example.test`.
Route the **whole hostname at its root**, preserve the public Host header, and
pass paths unchanged: admin `/_proxy/`, client routes and readiness `/healthz`.
Subpath mounting is unsupported. Keep the backend inaccessible to untrusted
clients and restrict public access to readiness as appropriate.

A minimal separately managed Caddy configuration:

```caddyfile
proxy.example.test {
    reverse_proxy 127.0.0.1:8082 {
        flush_interval -1
    }
}
```

Use a real domain with valid DNS and a trusted certificate for deployment;
`.test` is only an example. For isolated private-CA tests, configure trust in the
test browser rather than disabling HTTPS verification.

Host and browser Origin must match the configured public origin, including its
port. `Forwarded` and `X-Forwarded-*` are not trusted. Passkey sessions use
non-Secure cookies only for the configured localhost HTTP development origin.
In token mode without a public
URL, loopback browser Hosts are accepted, including SSH-forwarded ports. Host
checks are not authentication; protect client routes with TLS too.

Disable response buffering for SSE, preserve cancellation, and allow timeouts
long enough for `--request-timeout`. Do not add automatic retries at the
terminator. Apply network/rate limits: login throttling does not prevent denial
of service. Browser admin sessions use HttpOnly, SameSite=Strict cookies, Secure
on HTTPS; mutations also require Origin and CSRF checks.

## Forwarding behavior

```text
/service-a/v1/anything  → https://api.service-a.example/v1/anything
/service-b/jobs/123     → https://api.service-b.example/unrelated/base/jobs/123
```

- Header substitutions **replace**, never append. Client authentication requires
  one header value with the exact configured prefix and a nonempty token.
  Duplicate authentication/credential fields are rejected.
- The client authentication header, Authorization, Proxy-Authorization, Cookie,
  forwarding headers, rewrite headers, proxy diagnostics and hop-by-hop headers
  are stripped before substitutions. Outbound Host is fixed to the upstream;
  response Set-Cookie is removed.
- Other end-to-end headers and opaque JSON, SSE, binary or media bodies pass
  through. Responses are not transparently decompressed or inspected. Redirects
  are returned unchanged, not followed.
- Escaped path identifiers such as `%2F` and raw query bytes are preserved.
  Traversal/dot segments, backslashes, control characters, absolute-form targets,
  CONNECT and Upgrade/WebSocket requests are rejected. This is not a general
  forward proxy or MITM service.

Only use trusted upstreams: an upstream can echo its own credentials in a
response, and opaque forwarding cannot prevent that. OS administrators are also
trusted; a proxy does not isolate clients with host/root access or prevent them
from making direct outbound connections.

## Storage and operations

State defaults to `os.UserConfigDir()/term-llm/proxy`, separate from agent/Hub
state. On Linux this is `$XDG_CONFIG_HOME/term-llm/proxy`, or
`~/.config/term-llm/proxy`. Override it with `--state-dir`. Run only one proxy
process per state directory.

Routes, **plaintext write-only secrets**, client token hashes/grants and history
live in `proxy.db`; passkey registrations live under `admin-auth/`. This is private server
storage, not an encrypted vault. The UI does not persist secret values or tokens
in browser storage. Keep the directory 0700 and credential/secret files private
regular files (0600 or stricter); bootstrap/recovery files cannot be symlinks.
Provision at least 256 random bits for host secrets. On Windows, configure
owner-only ACLs explicitly; Go file modes do not enforce them.

SQLite uses WAL with `synchronous=NORMAL`: process crashes are supported, but
recent writes may be lost on host power loss. Stop the service or use a consistent
SQLite backup; copying a live `.db` without its WAL is unsafe. Protect backups
like credentials. Downgrading requires a compatible protected backup.

```sh
term-llm serve proxy health --host 127.0.0.1 --port 8082
```

Health checks only the local listener/store, not upstream availability. The
command rejects raw-debug and profiling options and bypasses normal provider
configuration and update initialization.

| Default limit | Value | Override |
| --- | --- | --- |
| Active requests (no queue) | 64 | `--max-concurrent` |
| Request body | 64 MiB | `--max-body-bytes` |
| Total request time, including streaming | 15 minutes | `--request-timeout` |
| Request history | 30 days | `--retention-days` |

Incoming headers are limited to 64 KiB; dial/TLS/header-read bounds are 15 seconds.
There is no short upstream response-header timeout. Body limits also apply to
chunked uploads. Retention deletes in batches, so monitor disk capacity under
heavy traffic. Shutdown stops admission and drains for 20 seconds before
cancelling unfinished work.

For container deployment, see
[the deployment example](https://github.com/term-llm/term-llm/tree/main/examples/proxy).
No image is published by this feature. Keep proxy state and real upstream keys
out of client containers; a private network alone is not an egress firewall.

For a provider-free local demo, run `python3 examples/proxy/mock_upstreams.py`.
It starts two synthetic upstreams and prints their fixture credentials. For
contributor browser acceptance, see `scripts/proxy_passkey_smoke.sh --help`; use
only disposable test credentials and state.
