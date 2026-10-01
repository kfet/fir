# ACP session restore

Every ACP session has one saved config. One function builds every session from it.

```
 session/new ─┐                    ┌──────────────────────────────────────┐
 session/load ├─ client values ──▶ │ openSession(sid, req)                │
 session/resume┘  (override+save)  │  1. load  acp-sessions/<hash(sid)>   │
                                   │  2. overlay req (nil on rehydrate)   │
 prompt on a  ── req = nil ──────▶ │  3. createSession(cwd, project+client│
 reaped / unknown sid              │     MCP)                             │
 (also after a fir restart)        │  4. SwitchSession(transcript)        │
                                   │  5. restore model + thinking         │
                                   │  6. restored client MCP down? ──▶ -32010
                                   │  7. save                             │
                                   └──────────────────────────────────────┘

 saved on: open · set_model · set_mode · set_config_option · end of every prompt · reap
 deleted on: session/release, session/close · after 30 days untouched
```

| Saved config (0600) | Source |
|---|---|
| `setup.cwd`, `setup.mcpServers`, `setup.meta`, `setup.mode` | the client (`acpSetup` — add a field and it is saved and restored with no other change) |
| `transcript`, `model`, `thinking` | snapshot of the live session |

`-32010` carries `data.reason: "session_needs_reload"` and the failed server names. To recover, the client calls `session/load` with fresh `mcpServers`. Logs show server names only, never env or headers.
