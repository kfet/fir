# tau: Overview

Go port of the [pi coding agent](../pi-mono/packages/coding-agent/).

## Goals

1. **Run on Raspberry Pi Zero W/W2** — static Go binary (~15MB) vs Node.js (~100MB+)
2. **Easy upstream sync** — structural mirror of the TS source for trackable merges

## Source Stats

| Package | TS Lines | Go Package | Role |
|---|---|---|---|
| `packages/ai` | 22,062 | `pkg/ai/` | LLM providers, streaming, models |
| `packages/agent` | 1,495 | `pkg/agent/` | Core agent loop |
| `packages/tui` | 9,751 | `pkg/tui/` | Terminal UI framework |
| `packages/coding-agent` | 37,026 | `pkg/core/`, `pkg/modes/`, `cmd/tau/` | CLI app, tools, sessions |
| **Total** | **~70,334** | | |

## Build Targets

| Target | GOOS | GOARCH | GOARM | Notes |
|---|---|---|---|---|
| macOS Apple Silicon | darwin | arm64 | — | Dev machine (M1/M2/M3/M4) |
| macOS Intel | darwin | amd64 | — | Older Macs |
| RPi Zero W | linux | arm | 6 | ARMv6, single-core, 512MB |
| RPi Zero W2 | linux | arm64 | — | ARMv8 quad-core, 512MB |
| Linux x86_64 | linux | amd64 | — | General Linux |

## Module

```
github.com/kfet/tau
```

## Upstream Reference

Based on `pi-mono` commit `1caadb2e` (2025-02-08).
