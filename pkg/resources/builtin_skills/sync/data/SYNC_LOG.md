# Sync Log

## 2026-04-18 — Sync to v0.67.68 (commit a1edb8a4)

- `ai/src/utils/overflow.ts` → `pkg/ai/overflow/overflow.go`: Added `request_too_large` (HTTP 413) Anthropic pattern; added Cerebras `400/413 (no body)` pattern directly to `overflowPatterns`; added `nonOverflowPatterns` exclusion list so Bedrock throttling and generic rate-limit messages no longer match `too many tokens`.
- `ai/src/models.ts` → `pkg/ai/models.go`: `SupportsXhigh` now also accepts `gpt-5.4`. (Anthropic opus-4.6 handling left as fir-specific: fir intentionally restricts xhigh to Opus 4.7.)
- `ai/src/types.ts` → `pkg/ai/types.go`: Added `ProviderResponse` struct and `OnResponse` callback on `StreamOptions`; added `ZaiToolStream` field on `OpenAICompletionsCompat`; expanded `OpenRouterRouting` with full routing surface (`allow_fallbacks`, `require_parameters`, `data_collection`, `zdr`, `enforce_distillable_text`, `ignore`, `quantizations`, `sort`, `max_price`, `preferred_min_throughput`, `preferred_max_latency`).
- `ai/src/providers/simple-options.ts` → `pkg/ai/providers/options.go`: Propagate `OnResponse` through `BuildBaseOptions`.
- `ai/src/providers/anthropic.ts` → `pkg/ai/providers/anthropic.go`: `supportsAdaptiveThinking` now includes Opus 4.7. (`thinkingDisplay` and `CacheControlEphemeral` tool caching not wired — fir has a hand-rolled provider and this remains a known divergence.)
- `ai/src/providers/amazon-bedrock.ts` → `pkg/ai/providers/bedrock.go`: `supportsBedrockAdaptiveThinking` now includes Opus 4.7. (`bearerToken`, `thinkingDisplay`, and structured SDK-exception prefix formatting not ported — the Go port uses a different SDK and error path.)
- `ai/src/providers/google-vertex.ts` → `pkg/ai/providers/google_vertex.go`: Treat literal `gcp-vertex-credentials` as the "use ADC, not API key" sentinel.
- `ai/src/providers/google-gemini-cli.ts` → `pkg/ai/providers/google_gemini_cli.go`: Bumped default Antigravity client version to `1.21.9`.
- `ai/src/providers/google.ts` → `pkg/ai/providers/google.go`: Added Gemma-4 family detection (`MINIMAL`/`HIGH` thinking-level model); added distinct 2.5-flash-lite budget schedule; gated adaptive thinking for Gemma 4.
- `ai/src/providers/openai-completions.ts` → `pkg/ai/providers/openai.go`: Usage parser now honours OpenRouter `cache_write_tokens` and subtracts it from reported `cached_tokens` so `cacheRead`/`cacheWrite` are reported correctly; `qwen-chat-template` thinking now sets `preserve_thinking: true`; added z.ai `tool_stream: true` when `compat.zaiToolStream` is on; `resolvedCompat.ZaiToolStream` wired through `detectCompat`/`getCompat`.
- `ai/src/providers/openai-responses.ts` → `pkg/ai/providers/openai_responses.go`: When caching is not disabled, set `session_id` + `x-client-request-id` request headers so session-keyed cache providers pick up the session ID.
- `ai/src/providers/openai-codex-responses.ts` → `pkg/ai/providers/openai_codex_responses.go`: Codex SSE and WebSocket now send both `session_id` and `x-client-request-id`. (Service-tier pricing multipliers/`resolveServiceTier` not ported — no codex flex/priority cost surface in fir yet.)
- `coding-agent/src/core/model-resolver.ts` → `pkg/models/modelresolver.go`: Kimi-coding default model renamed from `kimi-k2-thinking` to `kimi-for-coding`.
- `coding-agent/src/core/model-registry.ts` → `pkg/models/modelregistry.go`: Built-in providers with custom models now inherit `api` and `baseUrl` from the first built-in model for the provider; `validateConfig` skips the `baseUrl`/`apiKey`/`api` requirement for built-in providers.
- `coding-agent/src/core/system-prompt.ts` → `pkg/resources/systemprompt.go`: Upstream switched to a local-timezone `YYYY-MM-DD` date; fir already uses `time.Now().Format("2006-01-02")` — behaviour matches.
- `agent/src/types.ts` → `pkg/agent/types.go`: Added `ToolExecutionMode` type (`sequential`/`parallel`) and `ExecutionMode` field on `AgentTool` so tools can force sequential execution. (No-op in fir today because fir's agent loop is already fully sequential.)
- `agent/src/agent.ts`, `agent/src/agent-loop.ts`: Only hash bumped. Upstream did a large rewrite around `AgentState` (accessor properties, renamed fields, awaited subscribers, `prepareArguments` hook, per-tool sequential forcing, `afterToolCall` error guard). fir's Go agent already diverged from this surface and ports these features on demand.
- `ai/scripts/generate-models.ts` → `cmd/generate-models/main.go`: Switched z.ai source key to `zai-coding-plan` (fallback to `zai`); mark `zaiToolStream: true` on all z.ai models except the four known-unsupported ones (`glm-4.5`, `glm-4.5-air`, `glm-4.5-flash`, `glm-4.5v`); normalize deprecated `k2p5` → `kimi-for-coding`; add static fallback for `claude-opus-4-7`; removed the static kimi-coding fallback block (`kimi-k2-thinking` / `k2p5`) now that models.dev covers it.
- `ai/src/models.generated.ts` → `pkg/ai/models_generated.go`: Regenerated (845 models).

### Notable changes

- **Context-overflow detection refined**: Anthropic HTTP 413 (`request_too_large`) is now recognised as overflow, while Bedrock throttling (`ThrottlingException: Too many tokens…`) and generic rate-limit messages are explicitly excluded. This unblocks automatic compaction on byte-size-over-limit requests and stops rate-limit errors from triggering compaction retries.
- **Claude Opus 4.7 support**: Recognised as adaptive-thinking across Anthropic, Bedrock, and (via xhigh-effort mapping) providers that re-expose Opus. Static fallback model added to the generator so it appears even when models.dev lags.
- **z.ai streaming tool calls**: New `zaiToolStream` compat flag emits top-level `tool_stream: true` for z.ai models that support it, delivering streaming tool-call deltas instead of one blob at end-of-response. Glm-4.5 family is excluded.
- **OpenAI Responses session cache**: `session_id` and `x-client-request-id` headers are now sent for OpenAI Responses and Codex (SSE + WebSocket), matching upstream's fix so providers that key prompt-cache on session ID actually get hits.
- **OpenRouter cache-write accounting**: Completion parser now honours `prompt_tokens_details.cache_write_tokens` and subtracts it from reported `cached_tokens`, so downstream cost reports stop double-counting OpenRouter cache writes as reads.
- **OpenRouter routing surface expanded**: `OpenRouterRouting` now matches upstream's full API (`allow_fallbacks`, `require_parameters`, `data_collection`, `zdr`, `enforce_distillable_text`, `ignore`, `quantizations`, `sort`, `max_price`, `preferred_min_throughput`, `preferred_max_latency`). Existing configs with only `only`/`order` keep working.
- **Built-in provider + custom models inheritance**: `models.json` entries for a built-in provider (e.g. `anthropic`, `openai`) can now list custom model IDs without having to re-specify `baseUrl`, `apiKey`, or `api` — those inherit from the first built-in model for that provider.
- **Kimi-coding default rename**: The `kimi-coding` provider's default model moved from `kimi-k2-thinking` to `kimi-for-coding`. Sessions that hard-coded the old ID will need to update or re-resolve.
- **Gemma 4 thinking levels**: Gemma 4 family detection added; uses Gemini thinking-level semantics (`MINIMAL`/`HIGH`) rather than Gemini 2.x budget-based thinking.
- **Antigravity client pin bumped**: Default Antigravity client version bumped from `1.18.4` to `1.21.9`; set `PI_AI_ANTIGRAVITY_VERSION` to override.
- **Gemini 2.5 Flash Lite budgets**: Separate token-budget schedule so low/medium effort isn't clipped against the full flash budgets.

### Deferred / known divergence
- `ProviderResponse` / `OnResponse` are plumbed through the type surface and `BuildBaseOptions` but are **not yet invoked by any fir provider call path**. Wiring the HTTP/SSE dispatch callers is a follow-up.
- Anthropic `thinkingDisplay` (`summarized`/`omitted`) and last-tool `cache_control` are not ported; fir's hand-rolled provider keeps its existing cache strategy.
- Bedrock `bearerToken`, `thinkingDisplay`, and `BedrockRuntimeServiceException`-prefixed error formatting are not ported.
- OpenAI Codex service-tier pricing multipliers (`flex`/`priority`) are not ported.
- The upstream Agent state rewrite (accessor properties, `prepareArguments`, awaited-subscriber settlement, `afterToolCall` try/catch) is not ported — fir's agent loop is already architecturally different.

## 2025-07-18 — Sync to v0.63.2 (commit 41039e8d)

- `ai/src/types.ts` → `pkg/ai/types.go`: Added `ResponseID` field to `AssistantMessage`; expanded `ThinkingFormat` with `openrouter` and `qwen-chat-template` variants; added contract docs to `StreamFunction` and `AssistantMessageEventType`.
- `ai/src/providers/anthropic.ts` → `pkg/ai/providers/anthropic.go`: `supportsAdaptiveThinking` now includes `sonnet-4-6`/`sonnet-4.6`; added `ResponseID` extraction from `message_start`; added `thinking: disabled` support for reasoning models when thinking is explicitly off.
- `ai/src/providers/openai-completions.ts` → `pkg/ai/providers/openai.go`: `mapOpenAIStopReason` now returns struct with `ErrorMessage`; added `ResponseID` tracking from chunk ID; added `"end"` as stop reason alias for `"stop"`; unknown finish reasons now produce error with diagnostic; added `openrouter`, `qwen-chat-template` thinking formats.
- `ai/src/providers/openai-responses.ts` → `pkg/ai/providers/openai_responses.go`: Replaced GPT-5 "Juice: 0" hack with proper `reasoning: {effort: "none"}`; GitHub Copilot excluded from reasoning disable.
- `ai/src/providers/openai-responses-shared.ts` → `pkg/ai/providers/openai_responses_shared.go`: Added `response.created` event for `ResponseID`; improved tool call ID normalization with `normalizeIdPart` and `buildForeignResponsesItemId` for cross-provider tool call replay; added `ResponseID` to `response.completed`.
- `ai/src/providers/azure-openai-responses.ts` → `pkg/ai/providers/azure_openai_responses.go`: Replaced GPT-5 "Juice: 0" hack with `reasoning: {effort: "none"}`.
- `ai/src/providers/openai-codex-responses.ts` → `pkg/ai/providers/openai_codex_responses.go`: Hash updated (upstream split SSE/WebSocket headers, N/A in Go SSE-only impl).
- `ai/src/providers/amazon-bedrock.ts` → `pkg/ai/providers/bedrock.go`: Cache detection now uses `AWS_BEDROCK_FORCE_CACHE=1` env for application inference profiles; thinking signature fallback to plain text when signature is missing (prevents Bedrock rejection).
- `ai/src/providers/google.ts` → `pkg/ai/providers/google.go`: Subtract cached tokens from input usage; added disabled thinking config (`thinkingBudget: 0` or lowest `thinkingLevel`); added `isGemini3ProModel`/`isGemini3FlashModel` helpers.
- `ai/src/providers/google-shared.ts` → `pkg/ai/providers/google_shared.go`: Multimodal function response support now version-based (`getGeminiMajorVersion >= 3`) instead of string check; non-Gemini models also supported.
- `ai/src/providers/google-vertex.ts` → `pkg/ai/providers/google_vertex.go`: Added disabled thinking support.
- `ai/src/providers/google-gemini-cli.ts` → `pkg/ai/providers/google_gemini_cli.go`: Added disabled thinking config; added `getGeminiCLIDisabledThinkingConfig` helper.
- `ai/src/providers/register-builtins.ts` → `pkg/ai/providers/register_builtins.go`: Hash updated (upstream refactored to full lazy loading, N/A in Go).
- `ai/src/utils/overflow.ts` → `pkg/ai/overflow/overflow.go`: Added Ollama explicit overflow error pattern.
- `ai/src/utils/oauth/anthropic.ts` → `pkg/ai/oauth/anthropic.go`: Hash updated (upstream refactored callback server internals, Go already uses proper callback server).
- `ai/src/utils/oauth/openai-codex.ts` → `pkg/ai/oauth/openai_codex.go`: Hash updated (upstream refactored to promise-based waiting, N/A in Go).
- `agent/src/types.ts` → `pkg/agent/types.go`: Added `ToolExecutionMode`, `BeforeToolCallContext`/`Result`, `AfterToolCallContext`/`Result` types; added `ToolExecution`, `BeforeToolCall`, `AfterToolCall` fields to `AgentLoopConfig`; updated `GetSteeringMessages` docs (called after full turn, not per-tool).
- `agent/src/agent-loop.ts` → `pkg/agent/loop.go`: Steering messages now checked after full turn (all tool calls complete) instead of after each tool; tool calls are no longer skipped when steering arrives; added `BeforeToolCall`/`AfterToolCall` hook invocations; removed `skipToolCall`.
- `agent/src/agent.ts` → `pkg/agent/agent.go`: Hash updated.
- `coding-agent/src/core/system-prompt.ts` → `pkg/resources/systemprompt.go`: Removed built-in `ToolDescriptions`; "Available tools" now only shows tools with caller-provided `ToolSnippets`; removed hardcoded edit/write/read-before-edit guidelines; added backslash→forward-slash cwd normalization.
- `coding-agent/src/core/model-resolver.ts` → `pkg/models/modelresolver.go`: Added `FindExactModelReferenceMatch` (strict, rejects ambiguous bare IDs); default model updates: cerebras→`zai-glm-4.7`, zai→`glm-5`, minimax→`MiniMax-M2.7`; `RestoreModelFromSession` uses `HasConfiguredAuth` instead of `GetApiKey`.
- `coding-agent/src/core/model-registry.ts` → `pkg/models/modelregistry.go`: Added `HasConfiguredAuth` method (checks auth storage + custom provider API keys).
- `ai/scripts/generate-models.ts` → `cmd/generate-models/main.go`: Added `gpt-5.4-mini` codex model; github-copilot and dot-notation context window overrides for opus/sonnet-4-6; MiniMax filtering (only M2.7/M2.7-highspeed kept); added Vertex `gemini-3.1-pro-preview-customtools`; google-antigravity opus/sonnet context window overrides.

### Notable changes

- **ResponseID tracking**: All providers now extract and store the upstream response/message ID, enabling response-level tracing and replay.
- **Disabled thinking support**: Reasoning models can now have thinking explicitly disabled (Anthropic: `type: disabled`; Google: lowest `thinkingLevel` or `thinkingBudget: 0`).
- **New thinking formats**: `openrouter` (uses `reasoning: {effort}`) and `qwen-chat-template` (uses `chat_template_kwargs.enable_thinking`).
- **Steering behaviour change**: Steering messages are now delivered after the complete assistant turn (all tool calls finish), not after each individual tool call. Tool calls are no longer skipped.
- **BeforeToolCall/AfterToolCall hooks**: New extension points for blocking tool execution or overriding tool results.
- **FindExactModelReferenceMatch**: Strict model lookup that rejects ambiguous bare IDs across providers.
- **GPT-5 reasoning disable**: Replaced the `Juice: 0` hack with proper `reasoning: {effort: "none"}`.
- **Bedrock thinking signature fallback**: Missing signatures now fall back to plain text instead of being rejected.
- **Bedrock cache for app inference profiles**: `AWS_BEDROCK_FORCE_CACHE=1` env var enables cache points for application inference profiles.
- **MiniMax model pruning**: Only M2.7 and M2.7-highspeed models are kept; deprecated models removed.
- **System prompt simplification**: Built-in tool descriptions removed; only caller-provided tool snippets shown.

## 2026-03-13 — Sync to commit f04d9bc4 (tags v0.57.0, v0.57.1)

- `ai/src/types.ts` → `pkg/ai/types.go`: Added `OnPayload` hook to `StreamOptions`; added protocol doc comment to `AssistantMessageEventType`.
- `ai/src/env-api-keys.ts` → `pkg/ai/envkeys/envkeys.go`: Added `GOOGLE_CLOUD_API_KEY` support for `google-vertex` provider (checked before ADC).
- `agent/src/types.ts` → `pkg/agent/types.go`: Added `OnPayload` field to `AgentLoopConfig`.
- `agent/src/agent.ts` → `pkg/agent/agent.go`: Threaded `OnPayload` through `AgentOptions`, `Agent` struct, `NewAgent`, and `AgentLoopConfig` construction.
- `agent/src/agent-loop.ts` → `pkg/agent/loop.go`: Pass `OnPayload` into `StreamOptions`.
- `ai/src/providers/anthropic.ts` → `pkg/ai/providers/anthropic.go`: `claudeCodeVersion` bumped to `2.1.75`; added `OnPayload` hook.
- `ai/src/providers/amazon-bedrock.ts` → `pkg/ai/providers/bedrock.go`: Added `OnPayload` hook.
- `ai/src/providers/openai-completions.ts` → `pkg/ai/providers/openai.go`: Added `OnPayload` hook; added `Usage` to `openaiChoice`; extracted `parseChunkUsage`; Moonshot `choice.Usage` fallback; assistant content always plain string.
- `ai/src/providers/openai-responses-shared.ts` → `pkg/ai/providers/openai_responses_shared.go`: Improved `response.failed` error with code/detail fields.
- `ai/src/providers/openai-responses.ts` → `pkg/ai/providers/openai_responses.go`: Tool-result images inlined in `function_call_output`; added `OnPayload` hook.
- `ai/src/providers/azure-openai-responses.ts` → `pkg/ai/providers/azure_openai_responses.go`: Added `OnPayload` hook.
- `ai/src/providers/openai-codex-responses.ts` → `pkg/ai/providers/openai_codex_responses.go`: Added `OnPayload` hook.
- `ai/src/providers/google.ts` → `pkg/ai/providers/google.go`: Added `OnPayload` hook.
- `ai/src/providers/google-gemini-cli.ts` → `pkg/ai/providers/google_gemini_cli.go`: Added `OnPayload` hook.
- `ai/src/providers/google-vertex.ts` → `pkg/ai/providers/google_vertex.go`: Added `OnPayload` hook; API-key auth support via `GOOGLE_CLOUD_API_KEY` (uses global Vertex AI Express endpoint, no project/location needed).
- `ai/src/providers/register-builtins.ts` → `pkg/ai/providers/register_builtins.go`: Hash updated (lazy-loading change N/A in Go).
- `ai/src/utils/oauth/anthropic.ts` → `pkg/ai/oauth/anthropic.go`: Token URL → `platform.claude.com`; new local callback server on port 53692; manual redirect URI fallback; expanded scopes; `scope` field added to refresh request.
- `ai/src/utils/oauth/github-copilot.ts` → `pkg/ai/oauth/github_copilot.go`: Device-flow and poll requests use `application/x-www-form-urlencoded`; 1.2× initial / 1.4× slow-down poll multiplier; server-suggested interval on `slow_down`; descriptive error messages.
- `ai/src/utils/overflow.ts` → `pkg/ai/overflow/overflow.go`: Added `model_context_window_exceeded` pattern (z.ai).
- `coding-agent/src/core/system-prompt.ts` → `pkg/resources/systemprompt.go`: Full datetime → ISO date-only (`Current date: YYYY-MM-DD`).
- `ai/scripts/generate-models.ts` → `cmd/generate-models/main.go`: `claude-opus-4-6` and `claude-sonnet-4-6` context window 200K→1M; Bedrock `anthropic.claude-sonnet-4-6` override added; re-ran `make generate-models`.

## 2026-03-07 — Sync to commit c99b9940

- `ai/src/types.ts` → `pkg/ai/types.go`: Added `opencode-go` provider, `TextSignatureV1` type, `Redacted` field on ThinkingContent, `ReasoningEffortMap` on OpenAICompletionsCompat, removed `RequiresMistralToolIds`.
- `ai/src/env-api-keys.ts` → `pkg/ai/envkeys.go`: Added `opencode-go` env key mapping.
- `ai/src/providers/amazon-bedrock.ts` → `pkg/ai/providers/bedrock.go`: Region resolution respects AWS_PROFILE; Sonnet 4.6 added to adaptive thinking; xhigh effort clamped to "high" for non-Opus models.
- `ai/src/providers/google-gemini-cli.ts` → `pkg/ai/providers/google_gemini_cli.go`: Added autopush endpoint fallback; simplified antigravity headers (removed extra X-Goog-Api-Client/Client-Metadata); version bumped to 1.18.4; Claude thinking header now checks provider+prefix+reasoning instead of name pattern; 403/404 now cascade to next endpoint immediately; Gemini 3.1 model matching via regex.
- `ai/src/providers/google-shared.ts` → `pkg/ai/providers/google_shared.go`: Use `skip_thought_signature_validator` sentinel for unsigned Gemini 3 tool calls instead of converting to text.
- `ai/src/providers/google-vertex.ts` → `pkg/ai/providers/google_vertex.go`: Hash updated (Gemini 3.1 regex matching already correct in Go).
- `ai/src/providers/google.ts` → `pkg/ai/providers/google.go`: Hash updated.
- `ai/src/providers/openai-codex-responses.ts` → `pkg/ai/providers/openai_codex_responses.go`: Added gpt-5.4 to minimal→low reasoning clamping.
- `ai/src/providers/openai-completions.ts` → `pkg/ai/providers/openai.go`: Removed all Mistral-specific compat (tool IDs, thinking-as-text, max_tokens, assistant-after-tool-result); unified Z.ai and Qwen to both use `enable_thinking`; added `ReasoningEffortMap` support; added Groq qwen3-32b reasoning effort mapping.
- `ai/src/providers/openai-responses-shared.ts` → `pkg/ai/providers/openai_responses_shared.go`: Added TextSignatureV1 encode/parse with phase support for OpenAI Responses assistant message IDs.
- `ai/src/providers/register-builtins.ts` → `pkg/ai/providers/register_builtins.go`: Added `SetBuiltInRegistrar` for `ResetApiProviders` support; lazy Bedrock loading N/A in Go.
- `ai/src/api-registry.ts` → `pkg/ai/registry.go`: Added `ResetApiProviders` and `SetBuiltInRegistrar` methods.
- `ai/src/utils/oauth/index.ts` → `pkg/ai/oauth/registry.go`: Added `UnregisterProvider` and `ResetProviders` functions.
- `ai/src/utils/overflow.ts` → `pkg/ai/overflow.go`: Added Mistral overflow pattern.
- `ai/src/stream.ts` → `pkg/ai/stream.go`: Hash updated (http-proxy removal N/A in Go).
- `ai/scripts/generate-models.ts` → `cmd/generate-models/main.go`: Mistral API changed to `mistral-conversations`; added opencode-go variant; added claude-sonnet-4-6 Antigravity; added gemini-3.1-flash-lite-preview; added GitHub Copilot gpt-5.3-codex; gpt-5.4 context window fixes; OpenRouter pricing overrides.
- `ai/src/models.generated.ts` → `pkg/ai/models_generated.go`: Regenerated with new models.
- `coding-agent/src/core/model-registry.ts` → `pkg/core/modelregistry.go`: Added per-model `baseUrl` override; added `UnregisterProvider`; refresh now resets API/OAuth registrations; removed `RequiresMistralToolIds` from compat.
- `coding-agent/src/core/model-resolver.ts` → `pkg/core/modelresolver.go`: Updated default models (gpt-5.4, gemini-3.1-pro-high, opencode-go); added `buildFallbackModel` for custom model IDs; `FindInitialModel` now uses `ResolveCliModel`.
- `ai/src/utils/http-proxy.ts` → deleted upstream; was never ported (N/A in Go).
- `ai/src/index.ts`, `ai/src/models.ts` → not tracked (no downstream equivalent).

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
