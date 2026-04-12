# Poe Bot UX — Improvement Proposal

> How to make the fir↔Poe experience richer without adding complexity.

## Current State

The poe-bridge today does one thing well: it pipes user messages into fir
as channel notifications and streams `reply()` tool-call chunks back as
SSE text events. The meta event is emitted immediately (satisfying the
5-second rule), and done closes the stream.

What's missing: the bridge treats every response as a flat text stream.
It doesn't use most of the protocol's features. The user sees raw
markdown with no interactive affordances.

## Proposal: 6 Changes, All Protocol-Native

### 1. Suggested Replies

**Cost: tiny.** The protocol already supports `suggested_reply` events.
The bridge just needs to accept them from fir.

Add a `suggested_replies` field to the reply tool:

```
reply(message_id, text, final, suggested_replies?: string[])
```

When `final=true` and `suggested_replies` is set, emit `suggested_reply`
events before the `done` event. Poe renders them as tappable buttons.

**Use cases:**
- "Tell me more" / "Show code" / "Explain differently"
- Skill-specific follow-ups ("Run tests", "Deploy", "Show diff")
- The LLM already knows good follow-ups; we just need to surface them

### 2. Status Text via `replace_response`

**Cost: tiny.** Use `replace_response` events to show progress on
long-running operations, then replace with the final answer.

Add a `replace` flag to the reply tool:

```
reply(message_id, text, final, replace?: bool)
```

When `replace=true`, emit `replace_response` instead of `text`. This
lets fir show "⏳ Running tests..." → "⏳ 3/7 passed..." → final results,
without the intermediate status accumulating in the response.

**No thinking-block protocol support exists**, but replace_response is
the closest equivalent: stream visible reasoning, then replace with the
clean answer. Or keep the reasoning visible — user preference.

### 3. File Attachments

**Cost: small.** The protocol supports `file` events with URL, MIME type,
name, and optional inline reference.

Add a `file` event type to the reply tool or a separate tool:

```
reply_file(message_id, url, content_type, name, inline_ref?)
```

The bridge emits a `file` SSE event. Poe renders it as a downloadable
attachment, or inline if `inline_ref` is set (referenced via
`![title][inline_ref]` in the markdown).

**Use cases:**
- Generated code files, patches, logs
- Images from image-generation tools
- Export conversation artifacts

### 4. Error Signalling

**Cost: tiny.** Today, errors from fir just look like regular text or
the stream hangs. The bridge should emit proper `error` events.

When fir's reply indicates an error (or on timeout), emit:

```json
{"allow_retry": true, "text": "description", "error_type": "user_caused_error"}
```

Poe shows a clean error UI with an optional retry button. The
`error_type` field controls the UX:
- `user_message_too_long` — prompt the user to shorten
- `insufficient_fund` — monetisation-related
- `user_caused_error` — general user-fixable error

### 5. Metadata Persistence via `data` Events

**Cost: tiny.** The `data` event lets bots attach a metadata string to
the response, retrievable in subsequent requests via the message's
`metadata` field.

Use this to persist lightweight state across turns without relying on
fir's session memory:

```
reply(message_id, text, final, metadata?: string)
```

On `final=true`, if metadata is set, emit a `data` event before `done`.

**Use cases:**
- Track which files were modified in a session
- Store a task plan ID so follow-up messages can reference it
- Pass structured context between turns

### 6. Introduction Message

**Cost: zero (config only).** Set `introduction_message` in the settings
response. Poe shows it when a user first opens the bot — no query
needed, no compute used.

```go
SettingsResponse{
    IntroductionMessage: "Hi! I'm fir — a coding agent. Send me a task and I'll work on it. I can read files, run commands, and write code.",
    AllowAttachments: true,
}
```

## What We're NOT Doing

Keeping it simple means explicitly skipping:

- **Parameter controls** — sliders/dropdowns are for image-gen bots or
  simple config. Fir's interface is conversation; adding knobs would
  fight that. If we need parameters, we use natural language.

- **Bot-to-bot dependencies** — fir IS the bot. It doesn't need to call
  GPT-4 through Poe's Bot Query API; it has its own model access.

- **Monetisation** — premature. Get the UX right first.

- **Custom tool calling via Poe** — fir has its own tool system.
  Poe's tool calling is for bots that proxy to OpenAI. We don't need it.

- **Multi-response index** — the protocol supports `index` for bots
  that return multiple parallel responses. Not our use case.

## Implementation Priority

| Change | Effort | Impact | Priority |
|---|---|---|---|
| Introduction message | Zero | Medium | Do now |
| Suggested replies | Tiny | High | Do now |
| Error signalling | Tiny | Medium | Do now |
| Status via replace_response | Tiny | High | Do now |
| Metadata persistence | Tiny | Low | Next |
| File attachments | Small | Medium | Next |

All four "do now" items are additive changes to the reply tool schema
and the `newOnQuery` SSE loop. No architectural changes needed.

## Revised Reply Tool Schema

```json
{
  "name": "reply",
  "description": "Send a reply chunk to the Poe user",
  "inputSchema": {
    "type": "object",
    "properties": {
      "message_id": { "type": "string" },
      "text": { "type": "string", "description": "text chunk to append" },
      "final": { "type": "boolean", "description": "true = last chunk" },
      "replace": { "type": "boolean", "description": "replace all prior text" },
      "suggested_replies": {
        "type": "array",
        "items": { "type": "string" },
        "description": "follow-up suggestions (only on final=true)"
      },
      "error": { "type": "boolean", "description": "emit as error event" },
      "error_type": { "type": "string", "enum": ["user_caused_error", "user_message_too_long"] },
      "metadata": { "type": "string", "description": "persist with response (only on final=true)" }
    },
    "required": ["message_id", "text"]
  }
}
```

Single tool, flat schema, backward-compatible (new fields are all optional).
