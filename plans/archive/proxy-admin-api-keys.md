# Credential proxy: UI-issued admin API keys for client provisioning

Status: implemented

Implemented in the working tree (not committed). Deviations: `/_proxy/manage/`
is dispatched ahead of the Claude native forwarder's discovery-suffix matching
so unmatched management paths return JSON 404 (CONNECT still reaches the
forwarder); `grants` must be a present, non-null array on create/regrant;
management responses have a 30-second write deadline.

Lets an operator mint named admin API keys in the proxy web UI and use them from
automation to create, regrant, revoke and purge **API-provisioned** proxy
clients over a narrow machine API. Nothing else is administrable with these
keys. Revised after an Astra design review (approve with changes).

## Context (current code)

- Package: `internal/app/proxy` (standalone `term-llm serve proxy`). Frontend:
  `frontend/proxy/*.ts`, API types in `frontend/src/api/proxy-client.ts`.
- Clients ("callers") are managed only by the browser admin UI:
  `/_proxy/api/callers` POST/PUT/DELETE in `admin.go` (`changeCaller`, revoke
  case) behind cookie session + CSRF + exact Origin/Host (`Service.admin`).
- Primitives: `store.createCaller` (`store.go`), `writeGrants`,
  `store.updateGrants`, `store.revokeCaller` (`grants.go`, uses its own
  `accountWrite` transaction — do not nest it), `Service.cancelCallerSockets`,
  `randomToken`, `hashToken`, `inputError`, `jsonReply`/`fail`/`readJSON`.
  The store uses a single SQLite connection (`SetMaxOpenConns(1)`).
- `ServeHTTP` (`server.go`) order: `claudeForward`, exact `whoami`, `/healthz`,
  then `/_proxy*` browser admin, then forwarding.
- Passkey fresh-auth: `Service.consumeRecentAuth(w, sess)` (`passkey_admin.go`).
  Frontend: `assertPasskey(api, platform, 'reauth')` and pending/shown token
  protection via `context.protectToken` in `frontend/proxy/route-access.ts`.
- Schema: `store.go` `schema` const (fresh DBs) and `migration.go`
  (`schemaVersion = 5`). Follow `internal/sqliteutil/README.md` exactly.
- History attribution: `requests.data` JSON holds `Record.CallerName`;
  `money_history.data` holds `MoneyRecord.Device`/`DeviceID`. Neither is an SQL
  column; purge must not touch these tables.
- Defect: the 1,024-client limit counts revoked rows (`createCaller`,
  `claude_setup_store.go`) and revoked rows are never deleted; `store.callers()`
  loads every row.

## Trust model (state in UI and docs)

An admin API key is a whole-proxy provisioner: it can mint clients for **any**
grantable route, including routes added later. It can list, regrant, revoke and
purge only clients created through the machine API (`created_by != ''`), by any
active key — so key rotation needs no ownership transfer. It cannot see or
change browser-created clients, routes, secrets, accounts, history, money,
debug, passkeys or Claude setup, cannot forward traffic and cannot become a
browser session. Use only over loopback or HTTPS.

## Design

### 1. Schema v6

Canonical fresh schema: add `created_by TEXT NOT NULL DEFAULT ''` and
`external_id TEXT NOT NULL DEFAULT ''` to `CREATE TABLE callers`, plus:

```sql
CREATE UNIQUE INDEX callers_external_id ON callers(external_id) WHERE external_id != '' AND revoked = 0;
CREATE TABLE admin_keys (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, hash TEXT UNIQUE NOT NULL,
  created INTEGER NOT NULL, last_used INTEGER NOT NULL DEFAULT 0
);
```

Migration v6: `sqliteutil.ColumnExists` + explicit `ALTER TABLE callers ADD
COLUMN` for each absent column, create the index and table; the marker is
written by the existing immediate migration transaction. Bump
`schemaVersion`/fresh `PRAGMA user_version` to 6. Add a proxy v6 note to
`internal/sqliteutil/README.md` (v5 binary rejects v6; restore backup to
downgrade).

- Key token `randomToken("sk-tlpadm-")`; id `randomToken("ak_")[:26]`; only the
  SHA-256 hash stored; token shown once. Max 32 keys.
- **Revoking a key deletes its row** (no tombstones). `created_by` keeps the
  old id; the UI labels unknown ids "revoked API key".
- `Caller` JSON gains `created_by` and `external_id`.

### 2. Browser admin endpoints (issuance)

New `admin_keys.go`; `admin.go` gets only dispatch cases plus `admin_keys` in
`state`.

- `POST /_proxy/api/admin-keys` `{name}` (trimmed, 1–80) →
  `201 {key:{id,name,created,last_used}, token}`.
- `DELETE /_proxy/api/admin-keys` `{id, revoke_clients?: bool}` → under `s.mu`
  write lock, in **one** transaction: delete the key; if `revoke_clients`, mark
  that key's active callers revoked and delete their `claude_vault` rows. After
  commit, before unlocking, cancel affected callers' sockets. Returns
  `{ok, revoked_clients}`. 404 for unknown key.
- `state` gains `admin_keys:[{id,name,created,last_used}]`, never hashes/tokens.
- Existing session/CSRF/Origin path; in passkey mode the server calls
  `consumeRecentAuth` for both mutations. Token mode: session suffices.
- Audit `log.Printf` for issue/revoke: action, key id, cascade count; names
  quoted with `%q`; never tokens.

### 3. Machine API: `/_proxy/manage/v1/`

New `manage_api.go` (+ helpers in another focused file if needed). Dispatch in
`ServeHTTP` after exact `whoami` and `/healthz`, before generic `/_proxy*`
admin, for the `/_proxy/manage/` prefix (anything under it not matched → 404 JSON,
never the browser handler).

Boundary:
- Reject if the request has **any** `Origin` header (presence, including empty
  or `null`) → 403. Ignore cookies. No CORS headers (OPTIONS → 405).
  `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`.
- Exactly one `Authorization` header of form `Bearer sk-tlpadm-…`, otherwise 401.
- Take an admission slot (`s.slots`) before any SQLite work; bound body read
  deadline like `admin`; decode JSON (`readJSON`) before taking `s.mu`.
- Invalid-key throttling: separate bounded per-`RemoteAddr` bucket (8 failures
  per minute, map ≤1024 entries), 429 with `Retry-After: 60`; checked before
  the DB lookup. Invalid and unknown keys get the same 401. DB errors → 503, not
  counted as failures. Valid keys bypass the invalid-attempt bucket but not
  admission.
- **Serialization:** every mutation re-validates the key (hash lookup) while
  holding `s.mu` (write lock), checks revision, commits, cancels sockets, then
  unlocks — so a key revocation can never be overtaken by a queued create.
  Reads use `s.mu.RLock` and validate the key under it.
- `last_used` updated at most once per minute per key (in-memory throttle).
- Audit `log.Printf` per committed mutation: action, key id, client id, quoted
  names, grant list or count. Never tokens, headers or bodies.

Endpoints (all JSON; errors `{"error":…}`):

| Method | Path | Body | Result |
|---|---|---|---|
| GET | `/routes` | — | `{revision, routes:[{name,local_prefix,enabled,websocket,account_state?,caller_auth:{header,prefix},rules:[{path_prefix,caller_auth}]}]}` — explicit allowlist projection, never serialized `Route` |
| GET | `/clients` | optional `?external_id=` | `{clients:[Caller]}` API-created only |
| GET | `/clients/{id}` | — | `{client}` targeted query; 404 if absent or browser-created |
| POST | `/clients` | `{name, grants[], revision, external_id?}` | `201 {client, token}`; 409 `{error, client_id}` if `external_id` is held by an active client |
| PUT | `/clients/{id}/grants` | `{grants[], revision}` | `{client}`; closes the client's sockets |
| DELETE | `/clients/{id}` | — | `{ok}`; revoke + vault clear + close sockets; 404 unknown, 409 already revoked |
| DELETE | `/clients/{id}?purge=1` | — | `{ok}`; only already-revoked (409 otherwise): delete caller, grants, vault rows |

- `revision` is **required** on machine create/regrant and checked against the
  same snapshot passed to `writeGrants` (409 if stale). It is a config
  revision, not per-client optimistic concurrency.
- Preserve `writeGrants` invariants: exact authority binding, unique valid
  grants, 128 cap, vault cleanup on grant removal, post-commit socket cancel.
- Typed store errors so missing → 404, revoked → 409, invalid input → 400,
  storage → 503. Do not change the browser endpoints' behavior.
- `external_id`: optional, 1–128 chars `[A-Za-z0-9._:/-]`. Recovery from a lost
  create response: `GET /clients?external_id=X`, revoke, recreate.
- `name` 1–80 chars as in the browser path; `created_by` set in the INSERT.

### 4. Bounded caller state

- Active limit: 1,024 non-revoked callers (both `createCaller` and
  `claude_setup_store.go`).
- Retained-row cap: 4,096 total caller rows. When a create (any path) would
  exceed it, purge the oldest revoked callers (grants and vault rows too) in the
  same transaction; if none are revoked, fail with the existing limit error.
- `store.purgeCaller`: revoked-only, one transaction over callers/grants/vault.
- Request and money history are untouched; attribution remains in their JSON.

### 5. Frontend

- New `frontend/proxy/admin-keys.ts` (+ `admin-keys.test.ts`), rendered as an
  **API keys** section of the **Admin** tab via `frontend/proxy/admin.ts`; pass
  the passkey platform from `app.ts` into the Admin view.
  - Text explaining the trust model (any route; manages API-created clients only).
  - Create: name → `assertPasskey('reauth')` in passkey mode → POST → show-once
    token with copy button; pending **and** shown `protectToken` protection as
    in `route-access.ts`; `curl` example using the proxy origin and a
    `$TERM_LLM_PROXY_ADMIN_KEY` placeholder, not the literal token.
  - List: name, created, last used; **Revoke** with confirm, an "also revoke
    clients created by this key" checkbox (off), and passkey reauth.
- `frontend/src/api/proxy-client.ts`: `ProxyAdminKey`, `createAdminKey`,
  `revokeAdminKey`, `admin_keys` on state, `created_by`/`external_id` on
  `ProxyCaller`.
- `client-list.ts`: "Created by API key ‹name›" or "revoked API key"; show
  `external_id` when set.

### 6. Out of scope

Per-key route scoping; general idempotency keys; CLI wrapper; managing
browser-created clients via keys; any non-client administration via keys.

## Tests

Backend (`admin_keys_test.go`, `manage_api_test.go`, migration test):
- Migration: `ValidateMigrations`; faithful v5 fixture → v6 keeps callers,
  token auth and authority-bound grants with `created_by=''`; fresh vs migrated
  `SchemaSignature` parity; callback failure rollback and retry.
- Passkey mode: key create/revoke without fresh auth → 403; with → ok. Token
  mode: session suffices. Token only in create response; `state` metadata only.
- Deleted key → 401. Admin key gets nothing from forwarding or `/_proxy/api/*`;
  client token, browser cookie and cookie-only requests get 401 on
  `/manage/v1`. Origin present (incl. empty/`null`) → 403; OPTIONS → 405;
  duplicate Authorization → 401; 9th bad attempt → 429 with Retry-After.
- Create → token forwards on granted route, 401 on ungranted; `created_by` set.
  Browser-created clients invisible/404 to the machine API.
- Missing/stale revision → 409/400 per contract; unknown route → 400; unknown
  client → 404; already revoked → 409; failed ops leave state unchanged.
- Regrant/revoke closes sockets and fails immediately; revoke/purge clear vault.
- `external_id` conflict → 409 with client_id; lookup works; reusable after
  revoke.
- Key revoke with `revoke_clients` revokes only that key's active clients in one
  transaction; concurrent create vs key revoke never leaves an active client of a
  deleted key when cascade was requested.
- Churn: many create/revoke cycles with few active clients never hit the active
  limit and keep total rows ≤ 4,096; request/money attribution survives purge,
  including a request admitted before and finished after purge.

Frontend: reauth before create/revoke API calls; token shown once with
pending/shown protection; revoke confirm + cascade checkbox; created-by label.

Docs: "Provision clients with an admin API key" section in
`docs-site/content/guides/credential-proxy.md` (trust model, curl examples,
external_id recovery, loopback/HTTPS only) and the v6 storage note.
