# Poe Server Bot Protocol — Reference

> Research summary of the Poe custom server bot API.
> Source: [Poe Protocol Spec](https://creator.poe.com/docs/poe-protocol-specification),
> [`fastapi_poe` types](https://github.com/poe-platform/fastapi_poe).

## Overview

Poe server bots receive HTTP POST requests from Poe and respond via
**Server-Sent Events (SSE)**. The protocol is request–response: Poe sends
a query, the bot streams back events. No persistent connection from bot to
Poe exists — Poe holds the SSE stream open until the bot emits `done`.

## Request Types

| Type | Purpose |
|---|---|
| `query` | User sent a message; bot must respond via SSE |
| `settings` | Poe asks the bot for its configuration (JSON response) |
| `report_feedback` | User liked/disliked a message |
| `report_reaction` | User reacted to a message |
| `report_error` | Poe reports a protocol error back to the bot |

### Query Request Fields

```
query[]              — conversation history (role, content, content_type, timestamp, message_id, attachments)
message_id           — identifier for the response message
user_id              — anonymised user identifier
conversation_id      — conversation identifier (resets on context clear)
temperature          — optional model temperature
tools[]              — OpenAI-compatible tool definitions
tool_calls[]         — tool calls from a previous assistant turn
tool_results[]       — tool execution results
language_code        — BCP 47 language code (default "en")
```

Each message in `query[]` has:
- `role`: `system` | `user` | `bot` | `tool`
- `content`: message text
- `content_type`: `text/plain` | `text/markdown`
- `attachments[]`: file attachments with `url`, `content_type`, `name`, `parsed_content`
- `sender`: `{id, name}` — useful in multi-user chats
- `message_type`: `"function_call"` for tool call messages
- `parameters`: user-set parameter values from custom UI controls

## SSE Response Events

All events carry JSON `data`. Poe concatenates all `text` event payloads
to form the final visible response.

| Event | Data | Purpose |
|---|---|---|
| `meta` | `{content_type, linkify, suggested_replies, refetch_settings}` | Must be first event. Sets rendering mode. |
| `text` | `{text}` | Append a text chunk (streaming) |
| `replace_response` | `{text}` | Discard all prior text, replace with this |
| `suggested_reply` | `{text}` | Add a clickable follow-up button |
| `file` | `{url, content_type, name, inline_ref?}` | Attach a file |
| `json` | arbitrary JSON | Send structured data |
| `data` | `{metadata}` | Attach metadata to the response (retrievable in later requests) |
| `error` | `{text?, allow_retry?, error_type?}` | Signal an error |
| `done` | `{}` | **Must** be the last event — closes the stream |

### Error Types

`user_message_too_long` · `insufficient_fund` · `user_caused_error` · `privacy_authorization_error`

## Settings Response

Returned as JSON (not SSE) for `type: settings` requests.

| Field | Default | Purpose |
|---|---|---|
| `server_bot_dependencies` | `{}` | Bots this bot calls (for Bot Query API) |
| `allow_attachments` | `true` | Allow file uploads |
| `introduction_message` | `""` | Greeting shown on first visit |
| `expand_text_attachments` | `true` | Auto-parse text files into `parsed_content` |
| `enable_image_comprehension` | `false` | Auto-describe images |
| `enforce_author_role_alternation` | `false` | Merge consecutive same-role messages |
| `enable_multi_entity_prompting` | `true` | Combine bot messages in multi-entity chats |
| `parameter_controls` | `null` | Custom UI controls (sliders, dropdowns, etc.) |
| `context_clear_window_secs` | server-decided | Auto-clear context after inactivity |
| `allow_user_context_clear` | `true` | Let users manually clear context |

## Tool Calling (OpenAI-compatible)

### Defining Tools

```json
{
  "type": "function",
  "function": {
    "name": "get_weather",
    "description": "Get current weather for a location",
    "parameters": {
      "type": "object",
      "properties": { "location": { "type": "string" } },
      "required": ["location"]
    }
  }
}
```

### Tool Call Flow

1. Bot receives `query` with `tools[]` definitions
2. Bot responds with `tool_calls[]` in the message
3. Poe executes tools (or sends back to bot with `tool_results[]`)
4. Bot receives follow-up query with `tool_results[]` and generates final response

Streaming tool calls use `ToolCallDefinitionDelta` with `index`, `id`, `type`, and partial `function.arguments`.

## Parameter Controls

Bots can define interactive UI controls in settings:

- **TextField** / **TextArea** — text input
- **DropDown** — select from options
- **ToggleSwitch** — boolean
- **Slider** — numeric range with step
- **AspectRatio** — image dimension picker
- **Divider** — visual separator
- **ConditionallyRenderControls** — show/hide based on other parameter values

Controls are grouped into **Sections** (optionally collapsible) with optional **Tabs**.
User selections arrive in `query[-1].parameters`.

## Monetisation

Bots can authorise and capture costs:
- `authorize_cost(amount_usd_milli_cents, description)` — pre-auth before expensive work
- `capture_cost(amount_usd_milli_cents, description)` — charge after completion

## Limits

| Limit | Value |
|---|---|
| First event | Within 5 seconds |
| Total response | Within 120 seconds (spec; bridge uses 50 min) |
| Response length | 10,000 characters |
| Event count | 1,000 events |

## What's NOT in the Protocol

- **Thinking/reasoning blocks** — no native event type for collapsible thinking
- **Typing indicators** — no way to signal "bot is thinking" beyond the meta event
- **Reactions from bot** — bots receive reactions but cannot send them
- **Edit/delete** — bots cannot edit or delete previous messages
- **Push messages** — bots cannot initiate messages; they only respond to queries
