# Review Backlog — 2026-02-10

**Last reviewed:** Full repo review, 2026-02-09 20:08 PST — all 230 `.go` files (116 source + 114 test).
**Build status:** ✅ PASSING — `go build ./...` clean, `go vet ./pkg/...` clean, all 14 testable packages pass, `-race` clean on agent+core.
**Coverage:** ai 96.6%, core/tools 86.4%, agent 82.5%, core 81.8%, print 73.3%, tui/components 71.4%, compaction 69.1%, tui 65.8%, interactive/components 58.9%, theme 53.1%, **providers 33.7%**, **rpc 13.2%**, **interactive 2.3%**.

---

## E2E Test Results — 2026-02-10 22:02 PST (Cycle 3)

**10 tests run, 10 passed, 0 failed.** No API keys available — LLM tests skipped.

### Passing
- `3a` — `--help`: exit 0, contains Usage, --provider, --model ✅
- `3b` — `--version`: exit 0, "pi-go dev" ✅
- `3c` — `--list-models`: exit 0, 715 models in provider/id format ✅
- `1d` — Print mode no API keys: exit 1, error "Forbidden", no panic ✅
- `2a` — RPC `get_state`: valid JSON, success:true, data has model/thinkingLevel/isStreaming ✅
- `2c` — RPC `get_available_models`: success:true, data.models is array (37 models) ✅
- `2d` — RPC `set_thinking_level`: set success, get_state confirms thinkingLevel:"high" ✅
- `2e` — RPC unknown command: success:false, error "Unknown command: bogus_command" ✅
- `2f` — RPC malformed JSON: success:false, parse error, no crash ✅
- `2g` — RPC prompt+EOF: prompt accepted, clean exit 0 on stdin close ✅

### Previously Fixed (all confirmed working)
- ~~`3c` — `--list-models`: hangs~~ ✅ FIXED
- ~~RPC mode not wired up~~ ✅ FIXED
- ~~`2a`–`2f` — RPC stdin consumed by `readPipedStdin()`~~ ✅ FIXED — guarded with `ModeRPC` check

### Skipped (no API keys)
- `1a`, `1b`, `1c`, `1e` — Print mode with LLM
- `2b` — RPC prompt with LLM streaming
- `4a`, `4b`, `4c` — Tool execution (read, write, bash)

---

## Simplification

<!-- Skipped: needs design discussion — agent.ThinkingLevel vs ai.ThinkingLevel touches 9+ files across cmd/ and pkg/ -->
### `pkg/agent/types.go` — Duplicate `ThinkingLevel` type
`agent.ThinkingLevel` duplicates `ai.ThinkingLevel` with an added `"off"` value. Consider using `ai.ThinkingLevel` directly with an empty string representing "off", or a single shared type. The `ToAIThinkingLevel()` method is a code smell indicating the duplication.

<!-- Fixed: removed assets/themes/ — canonical location is pkg/modes/interactive/themes/ -->

<!-- Fixed: fuzzyFindText now returns occurrence count in fuzzyMatchResult, eliminating redundant normalizeForFuzzyMatch call -->

<!-- Fixed: rewriteFile and persistEntry now log errors to stderr instead of silently discarding them -->

<!-- Fixed: Subscribe unsub now compacts trailing nil entries from the listeners slice -->

<!-- Removed: jsonString is used by openai_responses.go — it's a package-level helper, not unused -->

<!-- Fixed: ListSessions now uses sort.Slice instead of insertion sort -->

<!-- Removed: context.Context is already threaded properly — all providers pass ctx to http.NewRequestWithContext or SSEClient.Stream. The cited line numbers (anthropic.go:168, openai.go:89) don't exist in the current code. -->

<!-- Fixed: UUID prefix increased from 8 to 12 chars (48-bit entropy, birthday collision at ~16M), fallback also truncated to 12 for consistency -->

<!-- Fixed: deepMergeSettings replaced with direct field-by-field merge — no more JSON round-trip -->

---

## Security

<!-- Removed: google.go API key leak — fixed, error is now wrapped without URL -->

<!-- Skipped: needs design discussion — Claude Code impersonation is a product decision, not a code bug -->
### `pkg/ai/providers/anthropic.go:22-28` — Claude Code impersonation (see URGENT.md)

<!-- Skipped: by design, matches upstream TS behavior -->
### `pkg/core/tools/bash.go` — No command sanitization or sandboxing

<!-- Skipped: by design, matches upstream TS behavior -->
### `pkg/core/tools/write.go:64` — Path traversal: write accepts absolute paths

<!-- Fixed: session.go and settings.go now use 0600 instead of 0644, matching authstorage.go -->

---

## Test Coverage

<!-- Fixed: providers coverage 26.9% → 33.7%. Deduplicated anthropic_test.go (removed ~800 lines of duplicates), added 25+ tests for convertAnthropicMessages, updateAnthropicUsage, toolResultContentToString, supportsAdaptiveThinking, jsonInt/jsonString, buildAnthropicParams (thinking/adaptive/temperature/OAuth system prompt), buildAnthropicHeaders (model/option headers, internal header stripping), convertAnthropicTools, resolveCacheRetention, StreamSimpleAnthropic (no reasoning, nil options, adaptive, budget), user image content. -->

<!-- Fixed: Added 15 Google provider tests — buildGoogleRequestBody (basic, tools, maxTokens override, temperature, no system prompt, assistant+tool result messages), parseGoogleResponse (text merging, mixed content, empty data lines), mapGoogleStopReason (all values), streaming (tool call, thinking, HTTP error, context cancellation, request headers), StreamSimpleGoogle (no key, with key). Added 3 test fixtures. -->

<!-- Fixed: Added server_handlecommand_test.go with 24 tests covering all command types in handleCommand switch, error paths, and response structure -->

<!-- Fixed: compaction coverage 69.1% → 78.8%. Added 19 tests for custom message types, getMessageFromEntry, extractFileOperations, extractTextFromResponse, empty entries, single entry, and FindTurnStartIndex edge cases. -->

<!-- Fixed: Added 15 tests for checkAutoCompaction (nil runner, no messages, no assistant, below threshold, threshold trigger, overflow trigger, runner error, overflow error WillRetry, nil model), SwitchSession, GetAvailableThinkingLevels (nil/non-reasoning/reasoning model), and persistMessage (user/assistant). Fork was already tested. ResumeSession doesn't exist as a method. -->

<!-- Fixed: Added tests for corrupt session file recovery (TestSessionManagerCorruptFileRecovery) and empty file handling (TestSessionManagerEmptyFile). ContinueRecentSession and ListSessions were already tested. -->

<!-- Fixed: Added tests for command timeout, output trimming, empty output, failure caching, and command-in-headers -->

<!-- Fixed: Added 6 tests for createSessionManager (default, no-session, named session, custom dir, continue, continue with no existing). readPipedStdin not testable without mocking os.Stdin; model resolution is integration-level. -->

---

## Correctness (TS→Go Port)

<!-- Removed: read.go FormatSize — not a bug, reviewer noted this -->

<!-- Removed: AgentLoop return values — results communicated via events channel, discarding return is intentional -->
