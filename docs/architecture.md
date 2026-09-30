# Architecture

```
                         ┌──────────────────────────────┐
                         │            fir               │
   user ──► modes ───────┤  pkg/modes: tui · -p · acp   │◄── Zed / relays (ACP JSON-RPC)
                         └──────────────┬───────────────┘
                                        │
                         ┌──────────────▼───────────────┐
                         │        agent loop            │  pkg/agent
                         │  prompt ► model ► tools ►…   │
                         └──┬────────┬────────┬─────────┘
                            │        │        │
          ┌─────────────────▼┐  ┌────▼─────┐  ┌▼──────────────────────┐
          │ providers         │  │ tools    │  │ context               │
          │ pkg/ai · models   │  │ built-in │  │ system prompt         │
          │ auth · cache      │  │ + MCP    │  │ + AGENTS.md + skills  │
          └───────────────────┘  │ + ext    │  │ + [SYS_EXT sections]  │
                                 └────┬─────┘  └───────────────────────┘
                                      │
              ┌───────────────────────┼────────────────────────┐
     ┌────────▼─────────┐   ┌─────────▼────────┐   ┌───────────▼────────┐
     │ MCP servers      │   │ extensions       │   │ sessions           │
     │ pkg/mcp (stdio/  │   │ pkg/extension    │   │ pkg/session JSONL  │
     │ http)            │   │ JSON-RPC stdio   │   │ -c / -r resume     │
     └──────────────────┘   └──────────────────┘   └────────────────────┘
```

## Where things go

```
  need prompt text only? ──yes──► skill      (.fir/skills/<name>/SKILL.md)
          │ no
  script can do it?      ──yes──► extension  (.fir/extensions/<name>.py)
          │ no
                         ────────► core Go   (pkg/…)
```

## Release flow

```
 generate-models ► make all ► docs review ► CHANGELOG/VERSION ► commit+tag
   ► make install ► make publish ► watch CI ► fir update on hosts
```

Details: [extensions](extensions.md) · [protocol](extension-protocol.md) · [MCP](mcp.md) · [ACP](acp-spec/) · [SYS_EXT](sys-ext.md)
