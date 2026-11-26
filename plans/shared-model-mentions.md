# Share model mentions between the terminal and web composers
Status: proposed

## User-visible behavior

Both composers recognize `@model:` and `@m:`. They also suggest models for the existing bare-`@` provider-prefix syntax without taking away file or agent suggestions. The same model ordering, moving-alias labels, limits, and effort choices apply given the same catalog. Choosing a model that has effort choices opens the effort step; choosing an effort or leaving it unspecified inserts plain `provider:model[-effort]`. This never changes `/model`, attaches a file, or runs an agent.

## Implementation

1. **One Go completer.** Move model-token recognition, matching/ranking, effort rows, and replacement spans from `frontend/src/domain/model-mentions.ts` into a small, pure Go helper alongside `internal/mentions`. It takes the text/caret and a provider catalog supplied by the caller. Return rows with display label, badge, insertion text, and whether selection continues the token. Match up to the caret but replace the full model token, so editing a mention in place does not leave a trailing fragment. Use byte offsets internally; reuse the existing web mention API’s UTF-16 conversion at the boundary.
2. **Share catalog normalization, not discovery transport.** Extract the narrow metadata/effort normalization used by `/v1/models` into `internal/llm` and reuse the existing provider policy and alias helpers. Keep raw ID, display name, moving-alias status, and valid efforts distinct. Already-suffixed IDs admitted by an effort-only allowlist insert verbatim; do not strip arbitrary suffix-like model names. The terminal supplies its configured/cached catalog rather than fetching from every provider on a keystroke. Serve can reuse its upstream/cache/fallback path. An unavailable provider must not hide results from other providers.
3. **Reuse the existing popup and web search transport.** In the terminal, add model rows to `internal/tui/chat/mentions.go` and reuse its keyboard navigation, stale-selection checks, and insertion flow. In serve, extend the authenticated `/v1/mentions/search` response with additive model rows, resolving scoped model queries before project/worktree lookup. In the browser, consume those rows and remove the TypeScript matcher and mention-only fetch path once their behavior is covered; keep `/model` and its catalog cache separate. Preserve progressive discovery: answer from available catalogs without waiting for every cold provider, and refresh a still-current query as pending catalogs become available (a pending indicator with bounded revalidation can reuse the browser’s current request lifecycle). No second server, browser Go/WASM bundle, or duplicate ranking code.

Explicit `@model:` / `@m:` tokens should not fall through to file lookup or eager attachment on submit, even when dismissed or incomplete. Quoted or explicit relative paths must still be able to refer to real files with those names. Bare provider-prefix suggestions remain additive until the user chooses a model. Keep existing file-mention controls in place; scoped model lookup must not need a working project index.

## Verification

- Move the matcher cases to table-driven Go tests: aliases and pinned IDs, scoped versus bare matching, policy input, exact-effort IDs, no-effort/default selection, and a caret inside a token after multibyte text.
- TUI and browser integration tests each exercise `@m:opus` through to `claude-bin:opus-medium`, with no send, attachment or model switch. Cover Tab/Enter/Escape, empty and pending results, stale responses, and unchanged `@agent:` and file behavior.
- Serve tests cover authenticated no-project lookup, offsets, one slow/failing provider with usable results from another, and the existing `/v1/models` contract. Run the new focused Go and frontend tests, generated API type check, production bundle budget, and `make complexity`.

## Out of scope

Making terminal and web discovery equally live, merging their `/model` pickers, changing spawn validation, or adding a new prompt-time model directive.
