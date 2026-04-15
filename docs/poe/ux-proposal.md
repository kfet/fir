# Poe Bot UX — Improvement Proposal

> How to make the fir↔Poe experience richer without adding complexity.

## Current State

The poe-bridge pipes user messages into fir as channel notifications and
streams reply chunks back as SSE text events. The meta event is emitted
immediately (satisfying the 5-second rule), and done closes the stream.

### What's Working Now

The auto-reply system (`pkg/mcp/autoreply`) intercepts agent events and
streams rich markdown to Poe without explicit reply tool calls:

- **Tool calls** render as markdown code fences with language hints
  (`bash`, `text`, `tool`). Bash commands show `$ cmd` syntax.
- **Tool output** is shown inline, truncated to 8 lines for readability.
- **Thinking blocks** stream as italic blockquotes (`> *thinking...*`).
- **Plan updates** render with Unicode progress bars (`████░░░░`),
  status icons (✓ completed, → in progress, ○ pending), priority
  markers (❗), and metadata. First update shows full detail; subsequent
  updates use compact blockquotes.
- **Attachments** are supported: images are downloaded, base64-encoded,
  and sent to fir as multi-modal content blocks for vision. Documents
  with `parsed_content` are inlined as text; other files are linked.
- **Error signalling** via `error` and `error_type` fields on the reply
  tool. Relay sends error to Poe when an agent crashes or disconnects.
- **Replace responses** via `replace` flag on the reply tool — enables
  progress status updates that get replaced by final content.

## Proposal: Remaining Improvements

### 1. Suggested Replies *(not yet implemented)*

The protocol supports `suggested_reply` events. Add a `suggested_replies`
field to the reply tool:

```
reply(message_id, text, final, suggested_replies?: string[])
```

When `final=true` and `suggested_replies` is set, emit `suggested_reply`
events before `done`. Poe renders them as tappable buttons.

**Use cases:**
- "Tell me more" / "Show code" / "Explain differently"
- Skill-specific follow-ups ("Run tests", "Deploy", "Show diff")

### 2. File Attachments *(not yet implemented)*

The protocol supports `file` events with URL, MIME type, name, and
optional inline reference.

```
reply_file(message_id, url, content_type, name, inline_ref?)
```

**Use cases:**
- Generated code files, patches, logs
- Images from image-generation tools
- Export conversation artifacts

### 3. Metadata Persistence *(not yet implemented)*

The `data` event lets bots attach metadata to the response, retrievable
in subsequent requests via the message's `metadata` field.

```
reply(message_id, text, final, metadata?: string)
```

**Use cases:**
- Track which files were modified in a session
- Store a task plan ID for follow-up reference
- Pass structured context between turns

### 4. Introduction Message *(not yet implemented)*

Set `introduction_message` in the settings response. Poe shows it when
a user first opens the bot — no query needed, no compute used.

```go
SettingsResponse{
    IntroductionMessage: "Hi! I'm fir — a coding agent. ...",
    AllowAttachments: true,
}
```

## What We're NOT Doing

- **Parameter controls** — fir's interface is conversation, not knobs
- **Bot-to-bot dependencies** — fir has its own model access
- **Monetisation** — premature
- **Poe tool calling** — fir has its own tool system

## Implementation Status

| Feature | Status | Commit |
|---|---|---|
| Error signalling | ✅ Done | 5f6bb6a |
| Status via replace_response | ✅ Done | auto-reply |
| Rich tool call rendering | ✅ Done | e334e34, 9db23e8 |
| Thinking block streaming | ✅ Done | e334e34 |
| Plan progress rendering | ✅ Done | 5746649, c3c2cc6 |
| Image attachment support | ✅ Done | 49fae22 |
| Document attachment inlining | ✅ Done | 49fae22 |
| Introduction message | 🔲 Todo | — |
| Suggested replies | 🔲 Todo | — |
| File attachments (outbound) | 🔲 Todo | — |
| Metadata persistence | 🔲 Todo | — |

## Reply Tool Schema (Current)

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
      "error": { "type": "boolean", "description": "emit as error event" },
      "error_type": { "type": "string", "enum": ["user_caused_error", "user_message_too_long"] }
    },
    "required": ["message_id", "text"]
  }
}
```

> **Note:** Auto-reply is now the primary output path. The reply tool is
> rarely called directly — agent events (text deltas, tool calls, plan
> updates, thinking) are streamed automatically. The reply tool is still
> used for `replace=true` (progress updates) and `error=true` (error
> signalling).
