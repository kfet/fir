# Streaming side_query (and surfacing it via Cards)

## Motivation

`ctx.side_query` is the one bridge call where the host does meaningful
work *between* request and response: it spins up an LLM stream, accumulates
content, and only then returns the final string. From the extension's point
of view today this is a black box — one JSON-RPC request, one JSON-RPC
response, possibly minutes apart, with nothing in between.

Two concrete failures fall out:

1. **Timeouts that aren't actually hangs.** The Python SDK hardcodes a 120s
   deadline on `side_query`. A 6 KB instruction to an opus-tier advisor with
   medium thinking routinely overshoots that. The Go side keeps streaming
   happily; the SDK has already declared failure and walked away.

2. **Opaque failures.** When the advisor returns a response with no usable
   content (e.g. only a redacted thinking block), the SDK gets back
   `side-query: response had no usable content`. The raw response is
   thrown away. There is no way, after the fact, to ask "what did the
   advisor actually say?"

Both vanish once `side_query` streams deltas while it works, and the
streaming is mirrored into the session's observable cards sidecar.

## Goals

1. Extension-visible streaming for `side_query` — text, thinking, and
   terminal status arrive as they happen.
2. No timeout while the stream is producing. Each delta is implicit
   liveness.
3. Durable record of every side_query (full text on success, partial
   text + block summary + error on failure) in the session's cards file.
4. Zero new persistence machinery. Cards already do this.
5. Backwards-compatible: callers that don't opt in keep the blocking
   request/response shape they have today.

## Non-goals

- Streaming for other bridge calls (`call_tool`, `exec`, …). The protocol
  shape designed here generalises, but only `side_query` lands now.
- A new "side-car" concept. We have cards. Use them.
- Persisting *every* delta as a separate card entry. We update one card
  in place as the stream runs. The card always reflects the latest state.
- Live-streaming the deltas to the TUI as scrolling text. The card slug
  is the UI surface — short, polled, fine for our purposes. A second pass
  can add fancier rendering later.

## Wire protocol

Streaming uses correlated JSON-RPC notifications keyed by the originating
request id. The terminating response on the same id closes the stream.

```jsonc
// ext → host: same request as today, with stream:true opt-in
{ "jsonrpc": "2.0", "id": 42, "method": "side_query",
  "params": { "question": "...", "stream": true,
              "model": "anthropic/claude-opus-4-7",
              "provider": "anthropic", "effort": "medium" } }

// host → ext: zero or more deltas (notifications, no id field)
{ "jsonrpc": "2.0", "method": "side_query/delta",
  "params": { "request_id": 42, "type": "thinking",
              "text": "...partial thinking chunk...", "seq": 0 } }
{ "jsonrpc": "2.0", "method": "side_query/delta",
  "params": { "request_id": 42, "type": "text",
              "text": "...partial assistant text...", "seq": 1 } }
{ "jsonrpc": "2.0", "method": "side_query/delta",
  "params": { "request_id": 42, "type": "usage",
              "tokens_out": 1834, "tokens_in": 36,
              "cache_read": 48300, "cache_write": 612, "seq": 2 } }

// host → ext: terminating response on the original id
{ "jsonrpc": "2.0", "id": 42,
  "result": { "text": "<full joined text>",
              "blocks": [ {"type":"thinking","len":3402,"sig_len":920},
                          {"type":"text","len":4218} ],
              "finish_reason": "stop",
              "tokens_in": 36, "tokens_out": 1834,
              "cache_read": 48300, "cache_write": 612 } }
```

Notes:

- `request_id` lives in the notification's `params`, not the JSON-RPC `id`
  field (which is reserved for requests/responses). This is the
  correlation key that ties deltas to their originating request.
- `stream: false` (or absent) keeps today's behavior exactly. No deltas
  emitted. SDK uses the existing blocking code path.
- `blocks` in the terminating response is the explicit answer to the
  "what did the model actually return?" question. Empty `text` paired
  with `blocks: [{"type":"thinking","len":0,"sig_len":940}]` is a
  redacted-thinking outcome and now self-evident.
- Delta `type` kinds for v1: `text`, `thinking`, `usage`. Future kinds
  (`tool_call`, `error`, `retry`) are additive — SDK ignores unknowns.

## SDK surface (Python)

Two flavors. The streaming one is additive; the blocking one stays.

```python
# 1. Blocking, unchanged. Internally non-streaming wire request.
text = ctx.side_query("question", model="...", timeout=600.0)

# 2. Streaming: generator. Each yielded delta has .type and the relevant
#    payload field (.text for text/thinking, .tokens_out for usage, etc).
#    The generator ends when the terminating response arrives; the final
#    SideQueryResult is available on .result after iteration finishes.
stream = ctx.side_query_stream("question", model="...")
for delta in stream:
    if delta.type == "thinking":
        ctx.report_progress(f"thinking… ({len(delta.text)} chars)")
    elif delta.type == "text":
        # accumulate as we go — see aside usage below
        partial += delta.text
final = stream.result   # SideQueryResult with text, blocks, finish_reason
```

Convenience: `stream.collect()` runs the loop, returns the full text. Use
when you don't actually care about deltas but want the no-timeout property.

### Timeout handling

The 120s SDK deadline is per-message. Each delta resets it. A long-running
advisor that produces a delta every few seconds therefore cannot trip the
deadline — it would only trip if the host went truly silent for >120s,
which is the correct semantics. The blocking flavor keeps the same
per-RPC deadline, but we raise the default to 600s and read
`FIR_SIDE_QUERY_TIMEOUT` from the environment as an override.

## Card lifecycle

When the aside extension makes a streaming side_query, it publishes one
card per call, addressed by a stable per-call key. The host stamps
`source = "aside"`. We update the same card in place as deltas arrive;
the cards file therefore always reflects the latest state of every
in-flight or completed side_query.

```python
key = f"query/{call_id}"   # ext-local short id, e.g. unix-ms

# on start
ctx.put_observable(key, slug="running", detail=instructions[:2000])

# on each delta — coalesce, don't write per-delta if rate is high
ctx.put_observable(key, slug=f"{len(text)}c", detail=partial_text[-8000:])

# on terminating response
ctx.put_observable(key, slug=finish_reason, detail=full_text)

# on error
ctx.put_observable(key, slug="ERR", detail=f"{kind}\n\n{partial_text}")
```

- The slug stays under 24 chars per the cards contract; we render it as
  a short status (`"running"`, `"2.1kc"`, `"stop"`, `"refusal"`, `"ERR"`).
- The detail is plain text, free-form; we put the running partial there
  so post-mortem inspection of failures works directly from
  `<session>.jsonl.cards`.
- We do **not** clear the card after completion. The historical state is
  what makes debugging possible. Cards accumulate across the session;
  TTL/eviction is out of scope per the cards design doc.
- Coalescing: update at most every ~250ms or every 256 bytes of new
  text, whichever comes first. Avoids hammering the atomic temp+rename
  on a fast-streaming model.

## Go-side changes

The streaming path in `pkg/agent/agent.go` already produces events via
`streamAssistantResponse`; today they are drained into a throwaway
goroutine inside `SimplePrompt`. For streaming side_query we surface them.

Sketch:

1. `pkg/agent/agent.go`: add `SimplePromptStream(ctx, msgs, opts, onEvent
   func(AgentEvent)) (string, error)`. Behavior identical to `SimplePrompt`
   but events are fed to `onEvent` synchronously as they're emitted, with
   the same NO-COMPACTION contract. `SimplePrompt` becomes a thin wrapper
   that passes a no-op callback.

2. `pkg/session/agentsession.go`: add `SideQueryStream(ctx, question,
   opts, onDelta func(SideQueryDelta)) (SideQueryResult, error)`. Maps
   `AgentEvent` kinds to wire delta kinds:
   - text chunk → `{type:"text", text:...}`
   - thinking chunk → `{type:"thinking", text:...}`
   - usage event → `{type:"usage", tokens_out:…, tokens_in:…,
     cache_read:…, cache_write:…}`. The prompt-cache counters are what
     make the advisor path's caching observable from an extension; the
     delta fires when ANY counter is non-zero, not only `tokens_out`.
   - Other event kinds are dropped at this layer for v1.

3. `pkg/extension/api.go` + `pkg/extension/session_bridge.go`: extend
   `SideQuery` to the streaming flavor. New signature:
   ```go
   SideQueryStream(question string, opts *session.SideQueryOptions,
       onDelta func(SideQueryDelta)) (SideQueryResult, error)
   ```
   The non-streaming path stays as `SideQuery` (returns just `string,
   error`) and is reimplemented as `SideQueryStream` with a nil callback,
   joining the blocks to produce the legacy string.

4. `pkg/extension/bridge.go`: in the `side_query` handler, look at
   `p.Stream`. When true, capture the in-flight request's id and write
   notifications back through the same writer as deltas arrive. Use
   `sendNotification("side_query/delta", {...request_id, ...delta})`.
   Then send the terminating response with the full `SideQueryResult`.

5. Telemetry: when `SideQueryStream` finishes with the "no usable
   content" error, include the per-block summary in the error string —
   `response had no usable content (blocks: [thinking(th=0,sig=940)])`.
   This is the small in-line fix from the earlier investigation; do it
   here so the error is informative even when no card is attached.

## SDK-side changes

`pkg/extension/sdk/python/fir_ext.py`:

1. `side_query(...)` — keep the signature; default `timeout=600.0`; read
   `FIR_SIDE_QUERY_TIMEOUT` env var when present. Internally sends
   `stream:false`. The blocking-RPC plumbing already exists.

2. `side_query_stream(...)` — new method. Returns a `SideQueryStream`
   object that:
   - Allocates a request id, sends `{"method":"side_query",
     "params":{"stream":true, ...}}`.
   - Registers a per-rid delta queue with the dispatcher loop.
   - Exposes `__iter__` yielding `Delta` objects pulled from the queue.
   - On terminating response, populates `self.result` and ends iteration.
   - Exposes `.collect()` for the no-loop case.

3. Dispatcher loop (`_read_loop` or equivalent) — extend to recognise
   `side_query/delta` notifications, look up the request id, push the
   delta payload into that request's queue. Unknown rids are dropped
   silently (orphans from a request the SDK already abandoned).

The existing `_pending`/`_results` plumbing already handles the
terminating response. The streaming path adds a parallel `_delta_queues`
dict keyed by rid, populated by the dispatcher and consumed by
`SideQueryStream.__iter__`.

## Aside extension changes

`pkg/resources/builtin_extensions/aside.py`:

1. Replace the two `ctx.side_query(...)` call sites with
   `ctx.side_query_stream(...)`, consuming the generator. Accumulate
   `text` deltas into a local buffer; update the card per the lifecycle
   above (running → coalesced progress → final slug).

2. The synthesis-path call (`_run_aside` with tools) gets the same
   treatment — it's the same call shape, just with a longer prompt.

3. On terminating response:
   - Success → put card with slug `finish_reason` (typically `"stop"`),
     detail = full text. Return today's structured result with
     `_prefix_advisor(text, advisor_used)`.
   - "No usable content" → the error from the Go side now includes the
     block summary. Mirror that into the card slug (`"empty:thinking"`,
     `"empty:redacted"`, etc.) and put the raw block summary in detail.
   - Other side-query failures → card slug `"ERR"`, detail = error text
     plus any partial text accumulated so far.

4. The card key is `query/<unix-ms>` — short, sortable, unique per call.

5. The advisor's `[advisor: <spec>]` prefix logic is unchanged; it
   wraps the final text regardless of streaming.

## Tests

- Bridge: stream:true request emits well-formed `side_query/delta`
  notifications correlated to the originating request id, followed by
  the terminating response. Stream:false matches today's behavior
  byte-for-byte. (`pkg/extension/bridge_test.go`)
- Agent: `SimplePromptStream` fires the callback for each event kind
  and produces the same final string as `SimplePrompt` when callbacks
  are no-ops. (`pkg/agent/agent_test.go`)
- Session: `SideQueryStream` is NO-COMPACTION-safe (existing contract).
  (`pkg/session/agentsession_test.go`)
- Aside: a side_query that produces text + thinking writes a single
  card with key `query/<id>`, source `"aside"`, terminal slug `"stop"`,
  and the full text in detail. A side_query that produces zero usable
  blocks writes a card with slug starting `"empty:"` and the block
  summary in detail. (`pkg/resources/testdata/aside_test.py`)
- SDK: `side_query_stream(...)` yields deltas in order; orphan deltas
  for an abandoned rid are silently dropped. Blocking `side_query` with
  `stream:false` is unaffected. (`pkg/extension/sdk/python/aside_ext_test.py`
  or new file)

## Out of scope (followups)

- Streaming `call_tool`. Same wire shape generalises; not needed yet.
- TUI rendering of side_query progress beyond the card slug. Cards
  + `/observe` already give us read-back.
- Per-card TTL/eviction. Cards design doc explicitly defers this.
- A "side-car" abstraction distinct from cards. Don't introduce one.

## File-level checklist

- [ ] `pkg/agent/agent.go` — `SimplePromptStream` + block-summary error
- [ ] `pkg/session/agentsession.go` — `SideQueryStream`
- [ ] `pkg/extension/api.go` — extend `BridgeAPI`
- [ ] `pkg/extension/session_bridge.go` — implement streaming flavor
- [ ] `pkg/extension/bridge.go` — wire handler emits deltas
- [ ] `pkg/extension/sdk/python/fir_ext.py` — `side_query_stream`,
      timeout bump, env var
- [ ] `pkg/resources/builtin_extensions/aside.py` — switch to streaming,
      publish cards
- [ ] Tests across each layer (see Tests section)
- [ ] `CHANGELOG.md` entry
