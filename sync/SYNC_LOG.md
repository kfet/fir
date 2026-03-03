# Sync Log

## 2026-03-02 — Sync to commit c65de34e

- `ai/src/types.ts` → `pkg/ai/types.go`: Added `Redacted` field to `ThinkingContent` for safety-redacted thinking blocks.
- `ai/src/env-api-keys.ts` → `pkg/ai/envkeys.go`: Already had HF_TOKEN/KIMI_API_KEY; Vertex ADC caching fix is Node-specific, skipped.
- `ai/src/models.generated.ts` → `pkg/ai/models_generated.go`: Regenerated models.
- `ai/src/providers/anthropic.ts` → `pkg/ai/providers/anthropic.go`: Sonnet 4.6 adaptive thinking support; redacted_thinking block handling; xhigh→high clamping for non-Opus; temperature incompatible with thinking; skip interleaved-thinking beta for adaptive models; Claude Code version bumped to 2.1.62; drop "(external, cli)" from user-agent.
- `ai/src/providers/openai-completions.ts` → `pkg/ai/providers/openai.go`: Z.ai now uses `enable_thinking` instead of `thinking` param (same as Qwen); guard nil choices array.
- `ai/src/providers/amazon-bedrock.ts` → `pkg/ai/providers/bedrock.go`: Sonnet 4.6 adaptive thinking; xhigh→high clamping.
- `ai/src/providers/transform-messages.ts` → `pkg/ai/providers/transform.go`: Drop redacted thinking blocks for cross-model conversations.
- `ai/src/utils/oauth/index.ts` → `pkg/ai/oauth/registry.go`: Added `UnregisterProvider` (restores built-in) and `ResetProviders`.
- `coding-agent/src/cli/args.ts` → `cmd/fir/args.go`: Added `--offline` flag.
- `coding-agent/src/main.ts` → `cmd/fir/app.go`: Added offline mode (`--offline` / `FIR_OFFLINE`); skip version check when offline.
- `coding-agent/src/core/model-registry.ts` → `pkg/core/modelregistry.go`: Added `UnregisterProvider`; `Refresh` now resets API/OAuth registrations before reapplying.
- `coding-agent/src/core/system-prompt.ts` → `pkg/core/systemprompt.go`: Added `ToolSnippets` and `PromptGuidelines` to `BuildSystemPromptOptions`; guideline deduplication.
- `coding-agent/src/core/agent-session.ts` → `pkg/core/agentsession.go`: Added `toolSnippets`/`promptGuidelines` fields wired to system prompt builder.
- `coding-agent/src/core/keybindings.ts` → `pkg/core/keybindings.go`: Use `alt+v` for image paste on Windows.
- `coding-agent/src/core/session-manager.ts` → `pkg/core/session.go`: Fork defers file write when no assistant message present (prevents duplicate headers).
- `coding-agent/src/modes/interactive/interactive-mode.ts` → `pkg/modes/interactive/mode.go`: Ignore SIGINT while suspended (Ctrl+Z); restore on SIGCONT.
- `coding-agent/src/core/extensions/types.ts` → `pkg/extension/api.go`: Added `PromptSnippet`/`PromptGuidelines` to `ToolDefinition`.
- Skipped (not ported): `export-html/tool-renderer.ts`, `tools-manager.ts`, `mom/`, `tui/terminal.ts` (koffi ESM loading, Node-specific), `package-manager.ts`.

## 2026-02-25 — Sync to commit 5c0ec26c

- `coding-agent/src/core/extensions/loader.ts` → `pkg/extension/registry.go`: Discovery order flipped — project-local extensions now load before global ones (first registration wins). Flag defaults no longer overwrite CLI-set values.
- `coding-agent/src/core/extensions/runner.ts` → `pkg/extension/runner.go`: Conflict resolution changed from "last wins" to "first wins" for tools, commands, and flags. Duplicate command registration now logs a warning and skips instead of silently overwriting.
- `coding-agent/src/core/resource-loader.ts` → `pkg/core/resourceloader.go`: Conflict handling for extensions changed — conflicting extensions are now kept loaded; conflicts reported as diagnostics only (not removed). Hash updated.
- `coding-agent/src/modes/interactive/interactive-mode.ts` → `pkg/modes/interactive/mode.go`: Scope group display order changed to `[project, user, path]` (was `[user, project, path]`). Hash updated.
- `ai/src/models.generated.ts` → `pkg/ai/models_generated.go`: Regenerated — 752 total models.
- `coding-agent/src/core/package-manager.ts` → not ported (TS/npm-specific); added to UPSTREAM_MAP.md as ❌.

## 2026-02-22 — Sync to commit 380236a0

- `ai/src/models.generated.ts` → `pkg/ai/models_generated.go`: Regenerated — updated MaxTokens for some models, pricing update for openrouter model.
- `coding-agent/src/core/model-resolver.ts` → `pkg/core/modelresolver.go`: Added `ResolveCliModel` with provider/model slash inference, fuzzy matching, strict thinking-level parsing, and OpenRouter-style ID fallback; added strict `parseModelPatternStrict`; updated `cmd/fir/app.go` to use it.
- `coding-agent/src/core/settings-manager.ts` → `pkg/core/settings.go`: Lazy directory creation — `~/.fir/agent` only created when actually writing (fixes #1588).
- `coding-agent/src/modes/interactive/components/tool-execution.ts` → `pkg/modes/interactive/components/tool_execution.go`: Added `strArgChecked` helper; updated all tool formatters to show `[invalid arg]` for wrong-type args; incremental write highlight cache N/A (no syntax highlighting in Go).
- `coding-agent/src/modes/interactive/interactive-mode.ts` → `pkg/modes/interactive/mode.go`: Extension `setTheme` persistence N/A (no extension setTheme API in Go); hash updated to 380236a0.
- `packages/tui/src/terminal.ts` → `pkg/tui/terminal.go`: Koffi dynamic require N/A (Go already uses platform-specific build tags); hash updated to 380236a0.

## 2026-02-17 — Sync to commit 4ba3e5be

- `auth-storage.ts` → `pkg/core/authstorage.go`: Major refactor — added `AuthStorageBackend` interface with `FileAuthStorageBackend` and `InMemoryAuthStorageBackend` implementations; changed `AuthStorage` to use factory methods (`NewAuthStorage`, `NewInMemoryAuthStorage`); added `DrainErrors()` for error accumulation; persistence now goes through backend
- `settings-manager.ts` → `pkg/core/settings.go`: Major refactor — added `SettingsStorage` interface with `FileSettingsStorage` and `InMemorySettingsStorage` implementations; added `SettingsScope` / `SettingsError` types; `SettingsManager` now uses storage backend abstraction; added `DrainErrors()`, `Flush()` (no-op in sync Go implementation), `markProjectModified()`, project setter methods (`SetProjectPackages`, `SetProjectExtensionPaths`, `SetProjectSkillPaths`, `SetProjectPromptTemplatePaths`, `SetProjectThemePaths`); project settings cached in-memory as `projectSettings` field
- `sdk.ts` → `pkg/core/sdk.go`: Updated `AuthStorage` creation to use `NewAuthStorage` factory
- `main.ts` → `cmd/fir/app.go`: Added `reportSettingsErrors()` helper and calls at startup for settings load error reporting
- `models.generated.ts` → `pkg/ai/models_generated.go`: Added 34 new models (deepseek.v3.2-v1:0 bedrock, claude-sonnet-4-6 anthropic, MiniMaxAI/MiniMax-M2.5 huggingface, Qwen3-Coder-Next huggingface, Qwen3.5-397B-A17B huggingface, MiniMax-M2.5-highspeed minimax/minimax-cn, glm-5/glm-5-free/minimax-m2.5/minimax-m2.5-free opencode, anthropic/claude-sonnet-4.6 openrouter/vercel-ai-gateway, qwen3.5-397b-a17b/qwen3.5-plus-02-15/z-ai/glm-5/openrouter/aurora-alpha/stepfun openrouter, alibaba/qwen3.5-plus/minimax/minimax-m2.5/zai/glm-5 vercel-ai-gateway, gpt-5.3-codex-spark azure/openai/openai-codex, llama3.1-8b cerebras, minimax.minimax-m2.1/moonshotai.kimi-k2.5/zai.glm-4.7/zai.glm-4.7-flash/deepseek.v3.2-v1:0 bedrock, zai-org/GLM-5 huggingface, glm-5 zai); updated pricing for deepseek/deepseek-v3.2, moonshotai/kimi-k2-0905, moonshotai/kimi-k2.5, qwen/qwen3-coder-next, z-ai/glm-4.6, z-ai/glm-4.7, zai-glm-4.7 (cerebras); added `boolRef()` helper to `pkg/ai/models.go`

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

## 2026-02-19 — Sync to commit 3a3e37d3

- `ai/scripts/generate-models.ts` → `cmd/generate-models/main.go`: Added claude-opus-4-6-thinking (Antigravity) and gemini-3.1-pro-preview (Vertex) models.
- `ai/src/models.generated.ts` → `pkg/ai/models_generated.go`: Regenerated with 2 new models.
- `coding-agent/src/core/package-manager.ts`: Skipped — not in scope for Go port.
