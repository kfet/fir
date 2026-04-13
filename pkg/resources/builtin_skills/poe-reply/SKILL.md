---
name: poe-reply
description: How to reply to Poe channel messages. Output streams automatically — no manual reply() calls needed.
---

# Poe Channel Reply Protocol

When you receive a channel message from Poe (indicated by `[Channel message from ... via poe ...]`), your response is **automatically streamed** to the Poe user.

## How It Works

- Your text output streams directly to Poe as you generate it
- Tool calls and their results are shown to the user automatically as markdown
- You do **NOT** need to call the `reply()` tool — it happens transparently
- The stream closes automatically when your response is complete

## Just Respond Normally

Simply answer the user's question. Your output tokens flow to Poe in real-time. Tool calls appear as `⚙️ tool_name` blocks with fenced output.

## When to Use reply() Manually

Only use the `reply()` tool for these special cases:

- **`replace=true`** — to show progress that gets overwritten:
  ```
  reply(message_id, "⏳ Running tests...", false, replace=true)
  ```
- **`error=true`** — to signal a retryable error:
  ```
  reply(message_id, "Failed to connect", true, error=true)
  ```

For normal responses, just write your answer. No reply() needed.
