# Credential proxy provider presets: smallest release

Status: implemented

Implemented and verified in the working tree; not committed or deployed.
This plan was narrowed after two design/review rounds with Fable and the user's
explicit request to avoid source bloat. The original expanded proposal is not
the delivery scope. Nothing is committed or deployed by this plan.

## Outcome

Choose **Routes → Add route → Provider** to fill the existing route editor with
an upstream URL, a suggested collision-free name/prefix, native caller
authentication and the corresponding upstream secret-header substitution.
Everything stays editable. Select a previously stored, matching-origin secret
and use the existing Save route operation. Custom HTTP stays available.

## Shipped scope

- One 18-entry data list in `internal/app/proxy/presets.go`, returned only in
  authenticated state responses. No new package, catalog endpoint or loader.
- Fourteen ordinary API-key services: OpenAI, Anthropic, Gemini, OpenRouter,
  xAI, Venice, NEAR AI, SambaNova, Obscurify, OpenCode Zen, ElevenLabs, Jina,
  Voyage AI and Mistral transcription.
- OpenCode Go Chat/Responses and Messages profiles with explicit client/session
  header caveats. These are not a drop-in dynamic `opencode-go` integration.
- Ollama template, with HTTP acknowledgement left unchecked and native-client
  authentication limitations explained; vLLM template with no invented URL.
- One Provider selector in new-route forms only. Selecting a provider fills
  existing controls; manual edits remain allowed. Switching after edits requires
  confirmation. Existing route editing never reapplies preset defaults.
- Existing origin-filtered secret choices, Secrets provisioning, revisioned
  config API, validation, grants, file mode and opaque forwarding are unchanged.
- Missing preset data falls back to the existing Custom HTTP editor.

## Deliberately excluded

No inline API-key entry, atomic route/key endpoint, secret-store rollback,
draft persistence/return flow, preset ID persistence, inferred client-setup
panels, new navigation destination, lazy editor extraction, extra asset/chunk,
provider registry refactor or general-purpose preset framework.

No normal provider config loading, credential/environment discovery, OAuth
refresh, provider constructors, model catalogs, automatic upstream calls,
request-body inspection, signing, query-key substitution or protocol conversion.
OAuth/CLI providers, Bedrock, FLUX and OpenCode Go Gemini remain outside this
release. The existing unsaved-route warning when visiting Secrets remains;
users can store the key first, or return and reselect a preset afterward.

## Client/auth boundaries

- Bearer services replace the incoming bearer caller token with an origin-bound
  upstream key; Anthropic, Gemini and ElevenLabs use their native API-key headers
  for both caller authentication and substitution.
- Client-supplied Anthropic version/beta headers remain unchanged. Presets do not
  silently pin a version. Gemini query-key clients are not supported.
- The route prefix replaces the vendor API root; do not append /v1 or /v1beta a
  second time. WebSocket remains unsupported; clients must use HTTP/SSE.
- OpenCode Go clients must send per-conversation session/client headers. Separate
  protocol templates do not make one dynamically switching native client work.
- Ollama still requires caller authentication even with no upstream key. The
  native term-llm Ollama client cannot supply it; use a compatible client or curl.

## Source and payload decision

Measured against the working tree immediately before this feature:

- Production source: **112 added / 9 removed = 103 net lines** across the data
  list, existing state response, frontend type and existing editor.
- Proxy JavaScript: **39,959 → 40,965 raw bytes** (+1,006); gzip level 6:
  **14,757 → 15,144 bytes** (+387, measured with Go gzip level 6).
- No added CSS, dependencies, assets, packages, forwarding branches or mutation
  APIs. Provider metadata is served as authenticated JSON, not bundled JS.
- Explicitly adjust only the proxy JavaScript test ceiling to 42,000 raw /
  15,500 gzip bytes. Keep the two-asset build and existing CSS/chat/Hub budgets.
  This bounded payload increase avoids the considerably larger source cost of
  extracting and maintaining a separate asynchronous editor asset lifecycle.

## Acceptance checks

1. Go tests validate unique preset IDs, HTTPS except acknowledged local Ollama,
   the deliberately blank vLLM endpoint, normal compile validation, and access
   through authenticated state only.
2. Local upstream fixtures exercise every fixed-URL entry with native caller
   auth, configured base paths, preserved query/body/stream response, replaced
   credential headers and no caller-token leakage or configuration-time probe.
   They do not make paid/live vendor requests or claim vendor acceptance.
3. Frontend tests cover saved OpenAI/Anthropic defaults, matching-origin secrets,
   editable fields, collision-free suggestions, cancelled switching, Custom HTTP,
   unchanged existing-route editing, Ollama acknowledgement and blank vLLM URL.
4. Desktop/mobile Chromium tests select presets, select a stored secret and save
   via the existing revisioned config API. Inspect screenshots; preserve the
   earlier light/dark, input-border and password-manager regressions.
5. Update the credential-proxy guide and frontend README. Run frontend format,
   lint, typecheck and tests; `make build`; `make check-changed ARGS=--all`;
   `make complexity`; `make plans-lint`; inspect the diff.

## Completion record

- Focused Go/proxy frontend tests and 12 desktop/mobile browser tests pass.
- Full `make check-changed ARGS=--all`, final complexity and plan-lifecycle
  checks pass. A compiled-binary Chromium smoke also verified the real
  authenticated state catalog and OpenAI/OpenCode Go Messages defaults.
- No live provider validation is attempted; keys/config on the user's machine
  are not loaded or modified by verification.
- This plan is archived as implemented; the expanded Fable proposal was
  intentionally not implemented. All changes remain uncommitted.

## Follow-up after the first trial: in-place key creation

The first trial showed that leaving an unfinished route to visit Secrets was a
poor default flow. **Add API key** now opens a native dialog on the route page,
reusing the same secret form and existing secret API. It suggests a key name,
pins the upstream origin, focuses the protected key field and selects the new
secret after storing it. Cancel/Escape leave the route intact. Key storage and
Save route remain separate commits; no atomic endpoint or draft store was added.

A shared-state refresh can update metadata/revision without replacing the active
DOM. The route revision advances only if the route list is unchanged; concurrent
route changes force the existing stale-revision conflict instead of overwriting
newer config. Write errors remain visible in the dialog, failed metadata reloads
report that the key was already stored, and auth loss returns to sign-in. The old
navigation/prefill detour was removed rather than retained alongside this flow.

Full repository checks, 14 desktop/mobile browser cases and a compiled-binary
browser smoke pass. Proxy-only ceilings are now 43,500 raw / 16,000 gzip JS bytes
and 10,500 raw / 3,200 gzip CSS bytes; chat/Hub budgets and the two-asset build
remain unchanged. The earlier footprint figures above describe the original
minimal preset release, before this follow-up.
