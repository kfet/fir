# Interactive authentication over ACP

A chat relay (zulip-acp, poe-acp-relay — anything built on acp-kit) talks to
fir over ACP on behalf of a user whose browser is usually **not** on the
agent host. An OAuth redirect to `127.0.0.1` on the agent host never reaches
fir, so the user has to paste the landing URL back through chat. The
`authenticate` method carries that round trip as a two-call protocol, opted
into with `_meta.auth.interactive`.

Two families of method ids use it:

| methodId | Logs in to | Advertised in `initialize.authMethods` | Needs `sessionId` |
|----------|------------|-----------------------------------------|-------------------|
| `oauth-<provider>` | a model provider (`!login`) | yes | no |
| `mcp:<server>` | a remote MCP server from the MCP config (`!mcp login`) | **no** — dynamic | **yes** |

Code: `pkg/modes/acp/auth_interactive.go` (shared machinery, provider flow),
`pkg/modes/acp/auth_mcp.go` (MCP flow).

## Wire format

```
Call 1 request:  authenticate { methodId, _meta: { auth: { interactive: true, sessionId? } } }
Call 1 response: { _meta: { auth: { state: "needs_redirect", id, url, short_url?, instructions? } } }
             or  { _meta: { auth: { state: "ok", id } } }     already authorised / finished without a URL
             or  JSON-RPC error                               login failed before producing a URL

Call 2 request:  authenticate { methodId, _meta: { auth: { interactive: true, id, redirect: "<landing URL or bare code>" } } }
Call 2 response: { _meta: { auth: { state: "ok", id } } } or JSON-RPC error

Cancel:          authenticate { methodId, _meta: { auth: { interactive: true, id, cancel: true } } }
                 → { _meta: { auth: { state: "cancelled", id } } }
```

- `id` is an opaque per-flow handle returned by call 1. Several flows for the
  same method may run at once. Call 2 and cancel must send the same `methodId`
  as call 1; a call 2 with an `id` from another method is rejected as "no
  pending interactive login", and such a cancel is a no-op.
- A parked flow times out after 10 minutes. Call 2 after that, or for an
  unknown `id`, fails with "no pending interactive login" — the relay should
  tell the user to start over.
- If the caller drops call 1 before a URL is produced, or call 2 before the
  login finishes, the flow is cancelled.

## `mcp:<server>` specifics

- `<server>` is the MCP server name as it appears in the session's MCP config.
- `_meta.auth.sessionId` (the ACP session id) is **required** on call 1: the
  MCP manager is per session. Missing → error; unknown session → error
  ("session not found"). An idle-reaped session is re-hydrated first.
- Only the interactive protocol is accepted; a call without
  `_meta.auth.interactive`, `redirect` or `cancel` is an error.
- Errors on call 1 come straight from the login: server not configured, a
  stdio server, `auth.mode` `none`/`bearer`, discovery failure, …
- The loopback callback listener stays up while fir waits for call 2, so a
  browser that *is* on the agent host still completes the flow on its own;
  whichever of loopback hit and pasted URL arrives first wins. The `state` in a
  pasted URL is validated; a bare code carries no state.
- On success the server is reconnected and its tools are pushed into the
  session — no `/mcp reload` needed.
- The method ids are dynamic, so clients must not reject an `mcp:*` method for
  being absent from `initialize.authMethods`.

## Legacy `/mcp login` over ACP

Typing `/mcp login <server>` as a prompt still works over ACP, but it has no
paste-back channel: it posts the URL and waits for the loopback redirect,
which has to land on the agent host. Its message says so and points at the
relay's interactive flow (`!mcp login <server>`), which uses `mcp:<server>`.
