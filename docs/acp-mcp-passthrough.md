# ACP extension: MCP data passthrough (`dev.fir.mcp/v1`)

fir forwards raw MCP data to ACP clients so a client (e.g. a chat relay) can
implement [MCP Apps](https://modelcontextprotocol.io) UI itself. fir does no UI
work and does not interpret any of this data — it passes it through.

## Capability

`initialize` advertises the extension under `agentCapabilities._meta`:

```json
{"agentCapabilities": {"_meta": {"dev.fir.mcp/v1": {
  "toolCallMeta": true,
  "resourceBlocks": true,
  "methods": ["_fir/mcp/resources/read", "_fir/mcp/tools/call"]
}}}}
```

## Tool-call `_meta`

`tool_call` and `tool_call_update` notifications for MCP tools
(`mcp__<server>__<tool>`) carry `_meta["dev.fir.mcp/v1"]`. Each field is present
only when known:

| field               | where          | source                                       |
|---------------------|----------------|----------------------------------------------|
| `server`            | start, update  | MCP server name                              |
| `tool`              | start, update  | original MCP tool name (not sanitised)       |
| `toolMeta`          | start, update  | tool definition `_meta` (e.g. `ui.resourceUri`) |
| `resultMeta`        | update         | `CallToolResult._meta`                       |
| `structuredContent` | update         | `CallToolResult.structuredContent`           |

Example `tool_call` (start):

```json
{"sessionUpdate": "tool_call", "toolCallId": "tc1", "title": "mcp__ui__show_app",
 "_meta": {"dev.fir.mcp/v1": {"server": "ui", "tool": "show_app",
   "toolMeta": {"ui": {"resourceUri": "ui://demo/app.html"}}}}}
```

`session/load` replays the same data: result fields are saved with the session
(tool result `details.mcp`); `toolMeta` is added when the server is still
connected.

## Resource content blocks

MCP `resource` (embedded) and `resource_link` result blocks are forwarded in the
`tool_call_update` `content` as ACP content blocks of the same type, after the
displayed text, which omits their text rendering (no duplicate HTML). MCP and
ACP share these shapes, so the blocks are passed as-is. The model still sees a
text rendering, and `rawOutput` keeps the full result.

## Methods

Both methods use the session's existing MCP connection (no second connection,
no second login) and return the raw MCP result object.

`_fir/mcp/resources/read`

```json
{"sessionId": "…", "server": "ui", "uri": "ui://demo/app.html"}
→ {"contents": [{"uri": "ui://demo/app.html", "mimeType": "text/html;profile=mcp-app", "text": "…"}]}
```

`_fir/mcp/tools/call`

```json
{"sessionId": "…", "server": "ui", "name": "show_app", "arguments": {"n": 1}}
→ {"content": […], "structuredContent": {…}, "_meta": {…}}
```

### Permissions

The client gets no more than the model has. A call is refused (`-32602`
invalid params) unless the matching agent tool is in the session's active tool
set: `mcp__<server>__<name>` for `tools/call`, `mcp__<server>__read_resource`
for `resources/read` (present only when the server advertises resources).
Disabled, unknown or logged-out servers fail the same way they do for the
model.

These calls are client actions, not model actions: they go straight to the
MCP server and return its raw result, so extension `tool_call` hooks,
output truncation and progress updates do not apply, and nothing is added to
the conversation.
