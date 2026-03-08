# Upstream Map: pi-mono TS → fir Go

Tracks only high-value files where upstream changes directly affect fir.
Low-value layers (TUI, interactive components, tools, extensions) are no longer tracked —
fir has diverged enough that syncing them costs more than it saves.

## Generators

| TS Source | Go Generator | Output | Make target |
|---|---|---|---|
| `ai/scripts/generate-models.ts` | `cmd/generate-models/main.go` | `pkg/ai/models_generated.go` | `make generate-models` |

## AI Types & Streaming (`pkg/ai/`)

| TS Source (relative to `pi-mono/packages/`) | Go File | Status |
|---|---|---|
| `ai/src/types.ts` | `pkg/ai/types.go` | ✅ |
| `ai/src/stream.ts` | `pkg/ai/stream.go` | ✅ |
| `ai/src/utils/event-stream.ts` | `pkg/ai/eventstream.go` | ✅ |
| `ai/src/env-api-keys.ts` | `pkg/ai/envkeys.go` | ✅ |
| `ai/src/api-registry.ts` | `pkg/ai/registry.go` | ✅ |
| `ai/src/utils/overflow.ts` | `pkg/ai/overflow.go` | ✅ |

## AI Providers (`pkg/ai/providers/`)

| TS Source | Go File | Status |
|---|---|---|
| `ai/src/providers/anthropic.ts` | `pkg/ai/providers/anthropic.go` | ✅ |
| `ai/src/providers/openai-completions.ts` | `pkg/ai/providers/openai.go` | ✅ |
| `ai/src/providers/openai-responses.ts` | `pkg/ai/providers/openai_responses.go` | ✅ |
| `ai/src/providers/openai-responses-shared.ts` | `pkg/ai/providers/openai_responses_shared.go` | ✅ |
| `ai/src/providers/azure-openai-responses.ts` | `pkg/ai/providers/azure_openai_responses.go` | ✅ |
| `ai/src/providers/openai-codex-responses.ts` | `pkg/ai/providers/openai_codex_responses.go` | ✅ |
| `ai/src/providers/google-vertex.ts` | `pkg/ai/providers/google_vertex.go` | ✅ |
| `ai/src/providers/google-shared.ts` | `pkg/ai/providers/google_shared.go` | ✅ |
| `ai/src/providers/google-gemini-cli.ts` | `pkg/ai/providers/google_gemini_cli.go` | ✅ |
| `ai/src/providers/google.ts` | `pkg/ai/providers/google.go` | ✅ |
| `ai/src/providers/amazon-bedrock.ts` | `pkg/ai/providers/bedrock.go` | ✅ |
| `ai/src/providers/simple-options.ts` | `pkg/ai/providers/options.go` | ✅ |
| `ai/src/providers/transform-messages.ts` | `pkg/ai/providers/transform.go` | ✅ |
| `ai/src/providers/register-builtins.ts` | `pkg/ai/providers/register_builtins.go` | ✅ |

## OAuth (`pkg/ai/oauth/`)

| TS Source | Go File | Status |
|---|---|---|
| `ai/src/utils/oauth/types.ts` | `pkg/ai/oauth/types.go` | ✅ |
| `ai/src/utils/oauth/pkce.ts` | `pkg/ai/oauth/pkce.go` | ✅ |
| `ai/src/utils/oauth/anthropic.ts` | `pkg/ai/oauth/anthropic.go` | ✅ |
| `ai/src/utils/oauth/github-copilot.ts` | `pkg/ai/oauth/github_copilot.go` | ✅ |
| `ai/src/utils/oauth/google-antigravity.ts` | `pkg/ai/oauth/google_antigravity.go` | ✅ |
| `ai/src/utils/oauth/google-gemini-cli.ts` | `pkg/ai/oauth/google_gemini_cli.go` | ✅ |
| `ai/src/utils/oauth/openai-codex.ts` | `pkg/ai/oauth/openai_codex.go` | ✅ |
| `ai/src/utils/oauth/index.ts` | `pkg/ai/oauth/registry.go` | ✅ |

## Agent Loop (`pkg/agent/`)

| TS Source | Go File | Status |
|---|---|---|
| `agent/src/agent-loop.ts` | `pkg/agent/loop.go` | ✅ |
| `agent/src/agent.ts` | `pkg/agent/agent.go` | ✅ |
| `agent/src/types.ts` | `pkg/agent/types.go` | ✅ |

## Core — Model & Prompt (cherry-pick only)

| TS Source | Go File | Status |
|---|---|---|
| `coding-agent/src/core/system-prompt.ts` | `pkg/resources/systemprompt.go` | ✅ |
| `coding-agent/src/core/model-resolver.ts` | `pkg/models/modelresolver.go` | ✅ |
| `coding-agent/src/core/model-registry.ts` | `pkg/models/modelregistry.go` | ✅ |

## No Longer Tracked

These layers were previously tracked but fir has diverged enough that
file-level sync is no longer worth the effort. Review upstream releases
for ideas instead.

- TUI base (`packages/tui/`) — fir's Go TUI is architecturally different
- Interactive mode & all components — fir has custom themes, components
- Tools (read, bash, edit, write, grep, find, ls) — stable, fir has its own variants
- Extensions (loader, runner, types) — different architecture
- Core infra (settings, auth, session, keybindings, slash commands, etc.)
- CLI entry point
- Print/RPC modes
