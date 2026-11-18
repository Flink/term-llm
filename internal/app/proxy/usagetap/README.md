# Response usage parser

`usagetap` observes response bytes only. It has no networking, credentials,
pricing, persistence or request-body access, and does not modify forwarded bytes.

```go
p := usagetap.ForResponse("/v1/responses", "text/event-stream")
p.Feed(chunk)
usage := p.Finish(transportCompleted)
```

Instances are single-owner. `Snapshot` returns a value; `Finish` is idempotent.
`FeedEvent` accepts decoded events; `NewWebSocket` observes uncompressed server
frames. Do not mix event feeds with buffered partial byte feeds.

JSON bodies, SSE events, NDJSON lines and assembled frames are bounded to 1 MiB.
When an upstream omits `Content-Type`, a response prefix of at most 64 bytes
selects JSON or SSE framing without changing forwarded bytes or headers. Explicit
unsupported media types still remain unknown; no request body is inspected.
Responses retain at most 128 IDs; repeated IDs replace snapshots, distinct IDs
sum. Missing IDs cannot establish multiple turns. Overflow, malformed identities,
unsupported encoding/compression or lost observability remains unknown.

Absent counts are `UnknownTokens` (`-1`), not zero. `Source` distinguishes reported,
partial, unknown and not-applicable evidence; reported does not guarantee every
count exists. OpenAI/Gemini input includes cached tokens; Anthropic input excludes
its separate cache reads/writes. OpenAI reasoning is included in output; Gemini
thoughts are separate. Normalize downstream without double-counting. Models and
tiers are exact upstream assertions, never price guesses. Chat Completions
`service_tier: null` is an absent optional tier, not a conflicting model identity.

## Responses WebSocket turn observer

Call `SetTurnObserver(func(WebSocketTurn))` on a Responses `WebSocket` before
feeding bytes to receive one immutable snapshot per server-observed response.
Callbacks run synchronously during `Feed` or `Finish` and must not call back
into the tap. Response IDs are used only in memory, never exposed in the
callback. Missing or oversized IDs are ineligible for callbacks but keep the
legacy aggregate behavior. At most `MaxWebSocketTurns` (128) distinct IDs can
emit per socket, including pending starts emitted by `Finish`. Once full, new
IDs are ignored so replayed terminal events cannot become new rows.

The first terminal event wins even if it has no usage; later snapshots can
revise only the aggregate. `Started` is the local time of the first observed
`response.created` or `response.in_progress` event, or zero if no start was
seen. `Ended` is the local terminal observation time, or the `Finish` time for
unmatched starts. `Outcome` is `complete`, `incomplete` (including failed,
cancelled, and unmatched starts), or `unknown` (terminal status not established).
Usage counts come only from that turn's first terminal snapshot, never client
frames. Outcome and usage evidence are independent: a completed response may
have no counters.

## Venice provider-reported charges

`ForVeniceChatCompletions` is enabled only for the canonical Venice API route
(`https://api.venice.ai/api/v1`) and a successful response from the exact
`/api/v1/chat/completions` endpoint on that origin. Missing or mismatched
response URL evidence does not qualify. Other providers never use this adapter.
Only identity-encoded JSON or SSE is supported (including bounded framing
inference when `Content-Type` is absent).

The charge comes from `usage.cost` in the JSON response or the final SSE data
chunk before an optional `[DONE]`. A root `cost` is ignored. The final chunk
replaces earlier candidates; cumulative or replayed values are never added
across chunks. `usd` and `diem` are nonnegative JSON numbers (either or both
may be present). At 1 DIEM = 1 USD, sum exact decimals before rounding half-up
to USD micros. The cost object is bounded to 512 bytes, each numeric token to
64 bytes and its exponent to ±64. Duplicate charge fields, unknown denominations,
invalid amounts, overflow, framing loss, and incomplete transport give no charge.

`Usage.VeniceCostMicros` (`venice_cost_micros` in JSON) is omitted when no valid
charge was observed. A non-nil zero is real evidence. Charge validity does not
require valid token counters; `Usage.Source` continues to describe token evidence.
No raw charge bytes are retained in the snapshot.

`proxy.NewUsageObserver` handles concurrent transport reads/close, panic isolation
and publishing before terminal persistence. A Responses WebSocket can report
usage on client close or shutdown when all observed response starts have matching
terminal counters and framing ends cleanly. Unfinished turns, missing usage, or
truncated frames remain partial or unknown. Counts are not bills. Protocol, boundary,
replay and cancellation tests define the detailed behavior.
