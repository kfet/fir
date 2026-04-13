---
name: poe-reply
description: How to reply to Poe channel messages. Stream output directly via reply(), show tool calls as markdown.
---

# Poe Channel Reply Protocol

When you receive a channel message from Poe (indicated by `[Channel message from ... via poe ...]`), follow these rules:

## Streaming Your Response

**Your text output IS the reply.** Stream it directly using `reply()` chunks as you compose your answer. Do NOT compose a full answer internally and then send it — stream as you go:

```
reply(message_id, "Here's what I found:\n\n", false)
reply(message_id, "The answer is...", false)  
reply(message_id, "", true)  // final=true closes the stream
```

Keep chunks natural paragraph-sized. Don't send one word at a time, but don't buffer everything until the end either.

## Showing Tool Calls

When you use tools (bash, read, write, etc.), **show them to the user** by streaming their invocation and results as markdown before and after:

Before executing:
```
reply(message_id, "\n\n⚙️ `bash: git status`\n", false)
```

After the tool returns, stream the output:
```
reply(message_id, "```\nOn branch main\nnothing to commit\n```\n\n", false)
```

Then continue with your analysis/response text.

## Using `replace` for Status Updates

For long-running operations, use `replace=true` to show progress:

```
reply(message_id, "⏳ Running tests...", false, replace=true)
// ... tests run ...
reply(message_id, "⏳ 5/7 tests passing...", false, replace=true)
// ... done ...
reply(message_id, "✅ All tests pass.\n\nHere are the results...", false, replace=true)
```

## Error Handling

If something goes wrong that the user should retry:
```
reply(message_id, "Failed to connect to the server", true, error=true, error_type="user_caused_error")
```

## Key Rules

1. **First reply chunk must come quickly** — send an initial acknowledgment within a few seconds
2. **Stream tool calls visibly** — the user wants to see what you're doing
3. **Use `replace`** for intermediate status that shouldn't clutter the final output
4. **One `final=true`** at the end closes the stream — nothing after that
5. **Don't duplicate** — your streamed reply IS the output. No need to compose text AND then call reply with the same text separately
