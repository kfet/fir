# Poe Reply Tool: `replace` and `error` Support

> **Status: ✅ Implemented** — merged to main.

## What Was Added

Two fields on the `mcp__poe__reply` tool, both backward-compatible:

1. **`replace`** (`bool`) — emits `replace_response` SSE event instead of
   `text`, letting fir show progress then overwrite it with final content.

2. **`error`** (`bool`) + **`error_type`** (`string`) — emits `error` SSE
   event with `allow_retry: true`, giving Poe a clean error UI with retry
   button. Supported error types: `user_caused_error`,
   `user_message_too_long`.

## Wire Format

Reply chunks on the relay websocket carry `replace`, `is_error`, and
`error_type` fields. The SSE handler in `newOnQuery` dispatches:

- `is_error` → `error` SSE event
- `replace` → `replace_response` SSE event  
- default → `text` SSE event

## Auto-Reply Integration

The auto-reply system (`pkg/mcp/autoreply`) now rarely requires explicit
reply tool calls. Agent events (text deltas, tool calls, thinking blocks,
plan updates) are streamed automatically. The reply tool is still used for:

- `replace=true` — progress updates from extensions
- `error=true` — explicit error signalling

The relay also sends error replies automatically when an agent crashes or
disconnects mid-query (commit 5f6bb6a).
