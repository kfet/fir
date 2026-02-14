# Sync Log

## 2026-02-13 — Sync to commit 9e22d391

- `ai/types.ts` → `pkg/ai/types.go`: Added `Transport` type ("sse", "websocket", "auto") and `Transport` field to `StreamOptions`
- `models.generated.ts` → `pkg/ai/models_generated.go`: Added Palmyra X4/X5 (bedrock), MiniMax-M2.5 (minimax, minimax-cn), qwen/qwen3-4b (openrouter); updated qwen3-235b-a22b pricing; removed tngtech/tng-r1t-chimera:free
- `openai-codex-responses.ts` → `pkg/ai/providers/openai_codex_responses.go` + `codex_websocket.go`: Added WebSocket transport support with session caching using nhooyr.io/websocket
- `agent.ts` → `pkg/agent/agent.go` + `types.go` + `loop.go`: Added `Transport` field to Agent, AgentOptions, AgentLoopConfig; passed through to StreamOptions
- `settings-manager.ts` → `pkg/core/settings.go`: Added `transport` setting with getter/setter and migration from legacy `websockets` boolean
- `sdk.ts` → `pkg/core/sdk.go`: Pass transport from settings to agent options
- `settings-selector.ts` → `pkg/modes/interactive/components/settings_selector.go`: Added transport setting to settings UI
- `interactive-mode.ts` → `pkg/modes/interactive/mode.go`: Wired transport setting change callback
- `terminal.ts` → `pkg/tui/terminal.go` + `terminal_windows.go` + `terminal_nonwindows.go`: Added Windows VT input mode (ENABLE_VIRTUAL_TERMINAL_INPUT) via syscall for Shift+Tab support

## 2026-02-12 — Sync to commit bd040072

- `ai/types.ts`: Added `metadata` field to StreamOptions (map[string]any)
- `models.generated.ts`: Regenerated from 724 models (added new providers and models)
- `anthropic.ts`: Refactored to simplify code (removed unnecessary tool_result check)
- Various provider fixes and updates
- Extension event forwarding (not yet implemented in Go)

## 2025-02-08 — Initial port from commit 1caadb2e

- Phase 0: Scaffolding (go.mod, Makefile, directory structure)
- Phase 1: AI layer core types, event stream, models, registry, providers/options, providers/transform
