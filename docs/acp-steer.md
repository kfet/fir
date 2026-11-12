# ACP extension: `_fir/session/steer`

Two modes, chosen by `params.mode`:

- `"steer"` (default) — inject a message into the running turn (below).
- `"record"` — log a side Q&A in the transcript without the model seeing it.

## Mode `steer`

Inject a message into a session's **running** `session/prompt` turn without
cancelling it. The content goes onto fir's steering queue (the same queue as
interactive steer and extension `deliverAs: "steer"`), so the model sees it as
a user message at the next tool boundary — or, if the model was about to end
the turn, the turn continues with it.

Use it when a relay answered a mid-turn user message elsewhere (e.g. in a
forked side session) and wants the main turn to see that exchange now.

## Capability

`initialize` advertises it under `agentCapabilities._meta`:

```json
"dev.fir.steer/v1": { "method": "_fir/session/steer" }
```

## Request

```json
{"jsonrpc":"2.0","id":7,"method":"_fir/session/steer",
 "params":{"sessionId":"…","prompt":[{"type":"text","text":"…"}]}}
```

`prompt` is a `ContentBlock[]`, parsed like `session/prompt` (text, images,
resource blocks). Slash commands and `/skill:` expansion are **not** applied.

## Responses

| Result | Meaning |
|---|---|
| `{}` | Queued on the running turn. The pending `session/prompt` keeps going and ends normally. |
| error `-32002` (`No active turn`) | No turn is running, or it was already cancelled (`session/cancel`) and is unwinding. **Nothing was queued** — send the content as a regular `session/prompt` instead. |
| error `-32001` | Session not in memory (released/reaped). |
| error `-32602` | Bad params or empty prompt. |

Race: if the turn ends between the accept and the next boundary, the message
stays queued and is delivered at the start of the session's next prompt — it
is never dropped.

To abort instead, use the standard `session/cancel` and re-prompt.

## Mode `record`

For side queries that should not affect the main turn. Appends a custom
session entry (`type: "custom"`, `customType: "side_query"`) to the
transcript. Custom entries are **not** sent to the LLM, so this never alters
the running turn or later turns. Works whether or not a turn is running.

```json
{"jsonrpc":"2.0","id":8,"method":"_fir/session/steer",
 "params":{"sessionId":"…","mode":"record",
           "question":"…","answer":"…","forkSessionId":"…"}}
```

`question` and `answer` are required; `forkSessionId` (the side session that
answered) is optional. `prompt` is ignored. The entry's `data` is
`{question, answer, forkSessionId?}`.

| Result | Meaning |
|---|---|
| `{"entryId":"…"}` | Entry appended; `entryId` is its transcript id. |
| error `-32001` | Session not in memory. |
| error `-32602` | Missing `question`/`answer`, or unknown `mode`. |
