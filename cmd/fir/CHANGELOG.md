# Changelog

## [Unreleased]

### Changed

- `plan-nudger`: escalate from plain reminder → warning steer → `[SYS_EXT]` prepend when `plan_completed` stalls across nudge cycles; ignore `next_update_in` hint while stagnating so the agent cannot delay its own intervention.

### Added

- `AGENTS.md`: explicit stuck-loop self-check rule (same command >5× without a file edit = rewrite from scratch).
- `shepherd/SKILL.md`: read-without-edit loop as a named intervention pattern.

## [0.21.0] - 2026-03-16

### Fixed

- `/schedule` entries now survive `/reexec`: `handleReexecCommand` previously called `CollectSessionData()` before emitting `session_shutdown`, so extension handlers that store data on shutdown (like the schedule extension) never ran in time. The new `ShutdownAndCollect()` on `Manager` fires `session_shutdown` first, waits for extensions to respond, then collects — matching the order extensions expect.

### Changed

- Renamed `batch_run` tool and `/batch` command → `aside` tool and `/aside` command. The new name reflects the unified concept: everything happens *off to the side*, ephemerally. Merged the old `/btw` side-question command into `/aside` (no tools = pure side query). Removed `batch.py` and `btw.py`, replaced with `aside.py`.

### Added

- `call_tool` bridge method: extensions can now call any registered tool (built-in, extension, or MCP) programmatically via `ctx.call_tool(name, params)` — results are returned directly and never enter conversation history
- `aside` builtin extension (`aside.py`): `aside` tool and `/aside` slash command for ephemeral side queries and multi-tool orchestration — uses `ctx.call_tool()` + `ctx.side_query()` to execute tools, collect outputs, and synthesise via a one-shot LLM call (replaces `batch.py` and `btw.py`)
- `pkg/pkg` package: `ParseSource`, `Clone`/`CloneRef`/`Pull`/`CurrentRef`, `ScanPackageResources`, and `Manager` (Install/Uninstall/Update/List/Resolve) for git and local package management
- `fir install <source> [--local]`, `fir uninstall <source> [--local]`, `fir packages [list|update]` CLI subcommands for external package management
- `GetGlobalPackages`, `GetProjectPackages`, `SetGlobalPackages` methods on `SettingsManager` for package list persistence
- `ResourcePackageResolver` interface in `pkg/resources`; installed packages now contribute skills, prompts, extensions, and themes automatically
- `GetPackageExtensionPaths`/`GetPackageThemePaths` on `ResourceLoader`; package extensions wired into extension discovery, package themes into theme search dirs
- `ExtraExtensionDirs`/`ExtraExtensionFiles` on `extension.SetupOptions`; `ConfigsFromFiles`/`DiscoverExtra` in `pkg/extension/discovery`
- `fir.json` package manifest format for declaring which resources a package exports
- `/btw <question>` slash command — ask a side question using the current session context; the Q&A is never added to history and works even while the agent is streaming
- `autoresearch` builtin extension: `run_experiment` tool (runs `autoresearch_bench.sh`, parses `METRIC name=value` lines), `log_experiment` tool (appends to `autoresearch.jsonl`), and `/autoresearch` slash command (experiment log summary)
- `autoresearch-create` builtin skill: sets up and drives an autonomous optimisation loop — benchmark, hypothesis, commit, run, keep/revert, repeat (inspired by [pi-autoresearch](https://github.com/davebcn87/pi-autoresearch))
- `/simplify [focus]` builtin extension — reviews recent git changes (staged, unstaged, or last commit) and asks the agent to apply simplifications across code reuse, quality, and efficiency; optional focus text narrows the review

### Changed

- Rename `ctx.btw()` → `ctx.side_query()` in the Python extension SDK — `side_query` matches the underlying RPC method name
- Freeze system prompt date per session — the `Current date:` line is now set once when the session starts, preventing cache invalidation at midnight
- Synthetic tool results for orphaned tool calls now use the assistant message's timestamp instead of `time.Now()`, making `TransformMessages` deterministic
- User text messages are now always serialized in block form for Anthropic, so `cache_control` breakpoints can be attached to all user messages (not just multi-block ones)
- Add `PrefixGuard` to the Anthropic provider — logs a debug warning when the serialized prefix changes between turns, helping detect cache-breaking regressions
- System prompt is no longer rebuilt before every turn — only on session creation and explicit actions (`/reload`, `/skill`), improving LLM provider prompt cache hit rates
- Extension auto-reload file watcher removed — use `/reload` to pick up extension changes explicitly

### Fixed

- Notify extension now includes the session name in the notification title (e.g. "fir — my-session")
- Extension manager re-emits `session_named` after `/reload` so reloaded extensions pick up the current session name
- OAuth token refresh errors are now propagated instead of silently swallowed — expired tokens produce actionable error messages (e.g. "OAuth token refresh failed for anthropic: …") instead of the generic "no API key for provider"
- All "no API key" error messages now include the underlying cause and guidance to run `fir login <provider>`
- Anthropic OAuth token refresh no longer sends `scope` parameter, which was causing `invalid_scope` errors and preventing auto-refresh
- ACP mode (Zed): auto-compaction failures are now shown to the user instead of being silently dropped
- builtin-skills test: add `autoresearch-create` to expected-skills allowlist
- Anthropic: `redacted_thinking` blocks (safety-filtered thinking, returned by Sonnet 4.6 and later) are now stored and passed back verbatim in multi-turn conversations, fixing a 400 error ("thinking blocks cannot be modified")
- `/schedule` countdown no longer disappears from the footer when other extensions call `set_status` — each extension now has its own status slot keyed by name
- ACP mode (Zed): inference errors (e.g. Bedrock API failures) are now shown to the user instead of being silently dropped

## [0.20.0] - 2026-03-13

### Added

- `Ctrl+N` keyboard shortcut for starting a new session (equivalent to `/new`)
- `OnPayload` hook on `StreamOptions` and `AgentLoopConfig` — lets callers intercept and mutate the raw request payload before each LLM call (all providers: Anthropic, Bedrock, OpenAI, Responses, Azure, Codex, Google, Gemini CLI, Vertex)
- Vertex AI API-key authentication: set `GOOGLE_CLOUD_API_KEY` to skip ADC and use the global Vertex AI Express endpoint
- Anthropic OAuth now uses a local callback server (port 53692) so the browser redirect is captured automatically; falls back to manual URL/code paste if the port is unavailable
- Anthropic OAuth scopes expanded: `user:sessions:claude_code`, `user:mcp_servers`, `user:file_upload`
- GitHub Copilot OAuth device-flow poll interval now applies a 1.2× initial multiplier and 1.4× slow-down multiplier; respects server-suggested intervals and surfaces descriptive errors
- Context overflow detection now recognises `model_context_window_exceeded` (z.ai non-standard finish reason)
- `claude-opus-4-6` and `claude-sonnet-4-6` context window corrected to 1 M tokens (was 200 K) across Anthropic, Bedrock, OpenAI-Codex, and OpenCode providers

### Changed

- System prompt now shows ISO date only (`Current date: YYYY-MM-DD`) instead of full local date-and-time string
- Anthropic OAuth token URL updated to `https://platform.claude.com/v1/oauth/token`
- GitHub Copilot OAuth device-flow and token-poll requests now use `application/x-www-form-urlencoded` bodies (was JSON)
- `claudeCodeVersion` bumped to `2.1.75`
- OpenAI Responses provider: `response.failed` error messages now include error code and detail fields
- OpenAI Responses provider: tool-result images are now inlined in `function_call_output` (not sent as a separate user message)
- OpenAI completions provider: assistant content is always a plain string (removed GitHub Copilot special-case array handling)

### Removed

- Fork feature (`/fork`, double-escape fork action, `ActionFork` keybinding)
- `doubleEscapeAction` setting — double-escape now always opens the tree selector
- `ActionToggleSessionNamedFilter` — dead action with no handler

### Fixed

- `/schedule` now uses `send_user_message("continue")` instead of `continue_session` to avoid errors on models that don't support assistant message prefill
- No longer creates `.fir/extensions/` directory in every project on startup

### Changed

- `make all` now runs `gofmt -s -w .` first, auto-formatting and simplifying all Go files in-place
- Extension status (e.g. `/schedule` countdown) now displays right-aligned on the pwd line instead of adding a third footer row
- `/session` command now shows current model and configured MCP servers
- Bash tool description now includes the current OS/arch (e.g. `darwin/arm64`) so the model knows the platform
- **Breaking:** `pkg/core` renamed to `pkg/session`; old `pkg/session` (persistence) moved to `pkg/session/store`
- **Breaking:** `pkg/core/compaction` moved to `pkg/compaction` (top-level)
- Split `pkg/ai` into sub-packages: `ai/jsonparse`, `ai/ratelimit`, `ai/overflow`, `ai/envkeys`
- Moved bash execution to `pkg/exec` (was `pkg/core/bashexec.go`)
- Moved clipboard image reading to `pkg/resources/clipboard` (was `pkg/core/clipboardimage.go`)
- Fixed `pkg/tui` test-time dependency on `pkg/modes/interactive/components`

### Fixed

- ACP: advertise `loadSession: true` in agent capabilities so Zed shows session history for fir

### Added

- `/session` now shows the current mode, model, and configured MCP servers
- Built-in PTY driver (`pkg/ptydriver`) — Go-native terminal multiplexer for agent-to-agent orchestration without requiring tmux
- `fir pty` subcommand — CLI for managing PTY sessions (serve, new, send, capture, wait, list, kill, etc.)
- `auto-helpers.sh` for shepherd and tmux-driver skills — auto-selects tmux or built-in PTY driver
- `pty-helpers.sh` — drop-in tm-* command replacements using `fir pty` backend
- Read tool: stream only needed lines when offset/limit are set instead of reading entire file into memory
- Compaction: `maxContextTokens` setting — hard token cap that triggers compaction regardless of fill ratio
- Plan nudger reads `next_update_in` from plan metadata — lets the LLM hint how many turns until its next plan update instead of the fixed 5-turn default
- Forward plan metadata to extensions via `session_update` events

### Removed

- Remove unused `CopyToClipboard` and helpers (dead code)

### Fixed

- Clear fully-completed plans immediately at the start of the next turn instead of waiting until the turn finishes
- Fix data race in `Editor.SetFocused` — acquire mutex to prevent concurrent read from `Render`

## [0.19.3] - 2026-03-09

### Fixed

- Fix plan not being cleared when starting a new session with `/new`

### Fixed

- Fix `fir update` downloading wrong platform binary — remove overly broad filter that bypassed OS/arch matching

## [0.19.2] - 2026-03-09

### Fixed

- Fix `fir update` on ARM devices (RPi) — rename asset from `arm6` to `armv6` to match go-selfupdate's expected naming convention

## [0.19.1] - 2026-03-09

### Changed

- `make publish` no longer builds or uploads assets locally — just pushes the tag and lets GoReleaser CI handle builds and releases

### Fixed

- Fix `TestSSEClient_Stream_ContextCancellation` hanging in CI by calling `CloseClientConnections` before `Close`

## [0.19.0] - 2026-03-09

### Added

- Extension frontmatter supports `events` and `commands` fields for lazy loading — extensions declare what they subscribe to without being started
- Test validates that each builtin extension's frontmatter declares all its `@fir_ext.on()` events and `@fir_ext.command()` commands
- Runtime frontmatter validation: after handshake, extensions with mismatched frontmatter get a warning and an offer to auto-fix the file

### Changed

- Parallelize extension process startup and handshakes, reducing startup time by ~50% when multiple extensions are active
- Add mutex to `SessionBridge` to protect concurrent tool registration during parallel extension startup
- Extensions that declare `events` in frontmatter are now started lazily on first matching event, reducing startup from ~760ms to ~30ms with default extensions

## [0.18.0] - 2026-03-09

### Added

- `/schedule` extension (`.fir/extensions/schedule.py`): defer agent continuation to a future time with live countdown status (`/schedule 45m`, `/schedule 2pm`, `/schedule cancel`)
- `/schedule` now supports multiple concurrent scheduled tasks, each with a unique ID (e.g. `[s1]`, `[s2]`); use `/schedule cancel <id>` to cancel a specific task or `/schedule cancel all` to cancel all
- `/schedule` now accepts an optional user message after the time argument (e.g. `/schedule 45m run the tests`); when present, the message is sent as a user turn instead of a bare continue
- `/new` command now accepts an optional `<name>` parameter to name the session on creation (e.g. `/new my-feature`)
- Demo/test extensions (`hello`, `demo`) are now marked with `demo: true` frontmatter and skipped during discovery unless explicitly listed in the extension allowlist
- Extensions auto-reload when their files are created, modified, or removed; interactive mode shows a status message on reload
- Extension bridge: `continue_session` method lets extensions call `session.Agent.Continue()` without injecting a message (`ctx.continue_session()` in Python SDK)
- Extensions can now use `deliver_as` option in `send_message` and `send_user_message` to steer or follow-up the agent directly
- New `session_update` event emitted to extensions on session state changes (plan updates, session naming)
- Plan nudger extracted from core into built-in extension (`plan_nudger.py`)
- Plan metadata: optional `metadata` key-value pairs (max 5 keys, 80-char values) shown in plan header — useful for fleet access info like session name, worktree path, attach command
- Plan title: optional `title` parameter on the plan tool, shown in the plan header and footer status bar
- `pkg/ai/ratelimit.go`: shared rate-limit detection utility — `RateLimitInfo`, `DetectRateLimit(msg)`, `IsRateLimitText(text)`, `ExtractRetryDelayFromText(text)`; `google_gemini_cli.go` refactored to delegate to the shared helpers
- Session listing speed: metadata sidecar cache (.jsonl.meta.json) eliminates full file reads on warm runs; parallel 8-worker loading; top-200 filename pre-sort caps cold-cache I/O; dropped AllMessagesText from search (ID + Name + FirstMessage + Cwd)
- `make publish` target to create GitHub releases marked as latest with cross-compiled binaries
- `make deploy` target to push binaries directly to Tailscale hosts via scp

### Changed

- Replace `Tools []AgentTool` with `*ToolSet` (ordered map keyed by name) in `AgentState` and `AgentContext`, making duplicate tool names structurally impossible
- Removed unused slash commands: `/hotkeys` (alias for `/help`), `/fork` (available via keybinding), `/copy` (copy last message to clipboard)
- Removed `/clear` slash command (use `/new` instead)
- Plan nudger now triggers on: 20 turns without an update, 2 minutes without an update, or when the agent stops with incomplete plan entries (compels continuation)
- Upstream sync to c99b9940: Mistral migrated to `mistral-conversations` API; added `opencode-go` provider; Sonnet 4.6 adaptive thinking in Bedrock; `skip_thought_signature_validator` for Gemini 3 unsigned tool calls; TextSignatureV1 with phase support; `ReasoningEffortMap` for per-model effort mapping; Groq qwen3-32b reasoning effort clamping; gpt-5.4 models; claude-sonnet-4-6 Antigravity; gemini-3.1-flash-lite-preview; provider fallback for unknown model IDs; `UnregisterProvider` and `ResetProviders` for dynamic provider lifecycle
- Upstream sync: trimmed tracked files from 580 to 48 (AI layer, providers, oauth, agent loop, model/prompt core only); dropped TUI, tools, interactive components, and other diverged layers
- Added PROJECT_WATCH.md for lightweight tracking of aider, goose, cline, and claude-code

### Removed

- Dead `EventBus` code from `pkg/core`

### Fixed

- Extension discovery now requires comment frontmatter (`# ---`); files without it (e.g. test scripts) are skipped, fixing a 5-second startup delay caused by `schedule_test.py` handshake timeout
- Move `schedule_test.py` out of `builtin_extensions/` to `pkg/resources/testdata/` so it is not embedded or discovered at runtime
- Fix ACP test `greet-ext.py` missing comment frontmatter, causing discovery to skip it after the frontmatter-required change
- Fix duplicate tool registration on extension reload (`/reload` and auto-reload) causing API "tools: Tool names must be unique" errors
- `Agent.Continue()` now resumes after an Escape-interrupted assistant response by injecting a synthetic "continue" user message, instead of returning an error
- Share single `AuthStorage` instance across all ACP sessions instead of creating duplicates per session; login/logout changes are now immediately visible without `Reload()`
- Fix MCP server subprocess leak when resuming an ACP session with a duplicate ID — `mcpManager.Close()` was missing from cleanup
- Handle `filepath.Abs` error in ACP `ResumeSession` instead of silently discarding it
- Plan tool no longer rendered as raw JSON in the TUI — shows a compact summary (title + done/in-progress/pending counts) instead of the full argument dump
- Plan tool: stale plans no longer cleared after a turn where the model updated the plan
- tmuxspinner now clears appended session name on `/new`, session switch, and `/reexec`

### Refactored

- Phase 7: split `pkg/modes/interactive/mode.go` (3308 lines) into `mode.go` (lifecycle), `commands.go` (slash commands + handlers), `selectors.go` (selector overlays), `events.go` (agent events + chat)
- Phase 7: split `pkg/modes/acp/acp.go` (1690 lines) into `acp.go` (core), `methods.go` (RPC handlers), `tools.go` (tool builders)
- Extract `resolveAgentDir()` helper in ACP mode — replaces 5 duplicated `DefaultAgentDir` + env-override blocks
- Move `pkg/core/tools` to `pkg/agent/tools` — tool implementations depend only on `pkg/agent`, `pkg/ai`, `pkg/log`; no core dependency
- Extract `footerdataprovider.go` from `pkg/core` to `pkg/modes/interactive/`
- Extract `bashexec.go` from `pkg/core` to `pkg/platform/`
- Extract `pkg/auth` from `pkg/core` — credential/OAuth storage as a standalone leaf package
- Extract `pkg/config` from `pkg/core` — settings, config value resolution, and defaults as a standalone leaf package
- Extract `pkg/models` from `pkg/core` — model registry and resolver as a standalone package
- Extract `pkg/session` from `pkg/core` — session persistence (save/load/list/tree) as a standalone package
- Extract `pkg/msg` from `pkg/core` — shared message types (branch summary, compaction summary, custom messages)
- Extract `pkg/resources` from `pkg/core` — resource loader, skills, builtin extensions, frontmatter, prompt templates, system prompt, slash commands

## [0.17.0] - 2026-03-06

### Added

- `/update` slash command: update fir to the latest version in-place and automatically restart the session
- OpenAI Responses API: `code_execution` server tool setting now enables the hosted shell tool (`container_auto`), with streaming support and multi-turn replay

### Fixed

- Skill commands setting now actually works: toggling it off hides `/skill:*` commands from autocomplete and prevents expansion
- 256-color fallback: dark background colors (toolPendingBg, customMessageBg, etc.) no longer map to saturated blue/red/green; they correctly fall back to dark grays

## [0.16.2] - 2026-03-06

### Changed

- Plan tool prompt rewritten to be directive ("MUST") with a concrete 3-step threshold

## [0.16.1] - 2026-03-06

### Changed

- Plan tool description now instructs the agent to create plans proactively at the start of multi-step tasks

## [0.16.0] - 2026-03-06

### Added

- Hot-reload via `/reexec` now preserves message queue and pending editor input
- Models ordered by SWE-bench Verified score in the model selector; scores are fetched live from the official leaderboard during `make generate-models` with curated baselines as fallback; unbenched models inherit scores from their family lineage (+0.1 bump, shown as `[SWE:~N%]`); `[SWE:N%]` badge shown per model in the selector list
- Session files now persist plan state (`plan_update` entries in JSONL); plan is restored automatically on session resume
- Auto-clear plan after the next user interaction completes, preventing stale plans from persisting across turns
- Plan visualization in TUI mode: plan entries are shown inline and updated live; toggle with `Ctrl+R` or `/plan` command (starts hidden, footer always shows progress)
- GPT-5.4 and GPT-5.4 Pro model support for OpenAI and Azure providers

### Removed

- Removed RPC mode (`--output-format rpc`); use ACP mode for programmatic integration

### Fixed

- `TestLoadBuiltinSkills_ReturnsExpectedSkills` now includes `skill-updater` in the expected set

## [0.15.0] - 2026-03-06

### Added

- Plan progress tracking in ACP mode: clients (e.g. Zed) see live tool execution status as plan entries
- Individual server tool toggles in `/settings`: web search, web fetch, code execution

### Changed

- Auto-compact is now a 3-way toggle: `client` → `server` → `off` (merges server compaction into existing setting)

## [0.14.1] - 2026-03-05

### Fixed

- Deduplicate OAuth auth URL display so it renders once instead of twice

## [0.14.0] - 2026-03-05

### Added
- Gemini 3.1 Flash Light enabled in Google, Google Vertex, Google Cloud Code Assist, and Google Antigravity OAuth providers
- Extensions can now constrain where they run via comment frontmatter (`# mode:` / `# modes:`), e.g. `tui`, `text`, `json`, `rpc`, and `acp`
- Updated notify and tmuxspinner extensions to be constrained to TUI mode
- Anthropic server-side tool/compaction capabilities are now model metadata (`Model.ServerTools`, `Model.Compaction`) populated by model generation and configurable in custom `models.json` definitions/overrides
- Anthropic server-side tools support: `web_search`, `web_fetch`, and `code_execution` can be enabled via `serverTools` setting; also supports raw type identifiers for newer versions with dynamic filtering
- Anthropic server-side context compaction via `serverCompaction` setting for long-running conversations
- `fir extensions [list]` and `fir extensions install <name>` CLI subcommands to list and install builtin extensions, mirroring the existing `fir skills` commands

### Changed
- Bedrock provider now uses the AWS SDK for Go v2 (`BedrockRuntime.ConverseStream`) instead of raw HTTP+SSE, enabling proper SigV4 signing, credential resolution (profiles, IAM, IRSA, ECS), and region handling

### Fixed
- Bedrock bearer token auth now sends the AWS_BEARER_TOKEN_BEDROCK value instead of the "<authenticated>" placeholder
- `/reload` now re-applies extension allowlists from current settings and `--extension` flags before restarting extensions, so removed names are actually disabled without restarting fir

### Changed
- `/reexec` now accepts an optional binary path (`/reexec [path]`) to exec a specific build while preserving the active session
- `/session` now includes the list of enabled extensions in both interactive and ACP modes

## [0.13.0] - 2026-03-03

### Added
- Local usage tracking: CLI flags, slash commands, tool use, session types, and modes are tracked in `~/.fir/agent/usage.json` (new `pkg/usage` package)

### Changed
- Rename `overseer` skill to `shepherd`
- Adopt `creativeprojects/go-selfupdate` for release detection and binary self-update, replacing custom HTTP download, checksum verification, and platform-matching code

### Fixed
- Fix ANSI escape sequence corruption in tmux: editor border lines no longer repeat per-character SGR color escapes; the entire border is now colored as a single string, matching DynamicBorder behavior

## [0.12.0] - 2026-03-03

### Added
- `--login <provider>` CLI flag for interactive OAuth login, used by ACP terminal auth so clients like Zed can spawn a terminal for the login flow
- ACP auth: terminal auth methods for all OAuth providers; when the client advertises `_meta["terminal-auth"]` capability (like Zed), auth methods include `_meta["terminal-auth"]` with command/args so the client spawns `fir --login <provider>` in an interactive terminal (matches Claude Agent's pattern)
- Local copy of ACP auth-methods RFD spec in `docs/acp-spec/rfd-auth-methods.md`
- Builtin `self` skill documenting fir configuration, modes, auth, extensions, skills, and full `settings.json` reference; kept in sync by tests that reflect on the `Settings` struct and parse the example JSON
- Builtin extensions: extensions in `builtin_extensions/` with `# --- / builtin: true / # ---` comment frontmatter are embedded in the binary and auto-discovered at lowest priority (shadowed by global/project extensions)

### Fixed
- Fix ANSI escape sequence corruption in tmux: only emit OSC 8 hyperlink reset on lines that contain hyperlinks, avoiding tmux OSC parsing issues that caused `8;2;R;G;Bm` fragments to appear as visible text in border lines
- ACP OAuth: GitHub Copilot and other device-code providers now work via `authenticate` — removed incorrect `UsesCallbackServer()` gate that blocked non-callback providers, added `OnPrompt` callback for providers that need it
- ACP OAuth: all OAuth providers now offered as `agent` type (not `terminal`) so clients without terminal-auth can still trigger the flow
- ACP auth: model registries are now refreshed after successful OAuth or env var authentication so newly available models appear immediately
- ACP auth: provider list is now stable-ordered (OAuth providers sorted by ID); previously map iteration caused random reordering on each initialize
- ACP auth: env_var auth methods hidden when client supports terminal-auth (e.g. Zed) since Zed renders them as non-functional buttons
- ACP auth: prompt errors now surface as JSON-RPC errors (AUTH_REQUIRED -32000 for auth failures) instead of being silently swallowed
- ACP auth: tests no longer trigger real Anthropic OAuth network calls
- ACP OAuth authentication: `--login` now saves credentials to the correct path (`auth.json`) so ACP mode can find them after terminal auth completes
- Google OAuth (Antigravity/Gemini CLI) and OpenAI Codex: no longer shows "Paste the redirect URL" prompt when browser callback succeeds; manual input is deferred 3 seconds, giving the browser flow time to complete first

## [0.11.0] - 2026-03-02

### Added
- Session `command` entry type records every slash command (and bash invocation) for audit/metering; entries are written to the JSONL session file but never included in the LLM context
- `AgentSession.RecordCommand` / `SessionManager.AppendCommandEntry` — new API to persist command audit entries
- `ModelRegistry.AddModel` — inject a model by value (useful for tests and dynamic registration)
- Extensions can now register slash commands via `InitResult.Commands`; users type `/name [args]` in the TUI and fir dispatches `hook/command` to the owning extension, showing any returned message; Python SDK gains a `@fir_ext.command()` decorator
- ACP session resume now replays full conversation history (user messages, assistant text/thinking, tool calls with results) to the client via session update notifications, so past sessions are fully visible in the client UI

### Fixed
- Bump `SDKVersion` to 2 so `fir_ext.command()` is actually extracted to the cache; without this, extensions crashed with `AttributeError` and extension commands were silently skipped
- ACP `session/resume` response now includes `configOptions` (thinking level, model selectors) matching `session/new` behavior
- `session_named` extension event emitted when session name changes or a named session is loaded; tmuxspinner extension now updates the tmux window name to match the session name
- ACP mode: fix race where `available_commands_update` notification was sent before the `session/new` response, causing clients to drop all slash commands; use a `writeNotifier` to defer notifications until after the response is flushed
- ACP mode: restore extension slash commands — `sendAvailableCommands` now includes extension-registered commands and `handleSlashCommand` dispatches them via `Manager.DispatchCommand`
- `SwitchSession` (`/resume`) now restores the model recorded in the resumed session, not just the thinking level
- tmuxspinner: session name now appended to the original window name rather than fully replacing it (e.g. `bash fix-bug ⠋` instead of `fix-bug ⠋`)
- tmuxspinner: spinner not always stopped on exit — restore window name immediately in `stop()`, add `atexit`/`SIGTERM` handlers as safety net, and add 250ms grace period in `Manager.Stop()` so extensions can process `session_shutdown` before being killed
- tmuxspinner: no longer renames the tmux window when fir runs as a subprocess (e.g. ACP mode) — checks for a controlling terminal via `/dev/tty` before activating

## [0.10.0] - 2026-03-02

### Added
- Restore `--extension <name>` / `-e <name>` CLI flag for filtering extensions by name (repeatable; merged with `settings.json` "extensions" list)
- `SetupOptions.EnabledNames` — allowlist of extension names; when non-empty only matching extensions are started
- `Manager.AllowedNames` — allowlist checked in `startOne`; skips extensions not in the list
- Sub-directory support for extension discovery: a directory inside `.fir/extensions/` (or `~/.config/fir/extensions/`) is treated as an extension whose name is the directory name; entry point is resolved as `main`, `main.py`, `main.sh`, `<dirname>`, `<dirname>.py`, `<dirname>.sh`, or the first executable found alphabetically
- `resolveEnabledExtensions()` in `cmd/fir/app.go` and `pkg/modes/acp/acp.go` merges settings + CLI flags into the enabled-names list
- `EnabledExtensions []string` restored to `acp.Options`; wired through to `extproc.Setup()`

### Removed
- Go-based compiled-in extension system (`pkg/extension/`, `pkg/extensions/`) — replaced by the stdio-based extproc system exclusively
- `pyrightconfig.json` — replaced by ty configured in `pyproject.toml`
- `sandbox` extension (incomplete framework with no real OS-level enforcement)

### Added
- `SetupOptions.TrustStorePath` — overrides default trust-store path; avoids polluting `~/.config/fir/` in tests
- `TestSetupHookToolCall` — Go integration test for `hook/tool_call` through `extproc.Setup()`
- `pkg/extproc/session_bridge.go` — `SessionBridge` implements `BridgeAPI` on `*core.AgentSession`, replacing the removed `ExtProcAdapter`
- `pkg/extproc/setup.go` — `Setup()` + `SetupResult` wire extproc extensions into a session without the old Go extension layer
- `Manager.SetNotifyFn` / `Manager.SetSetStatusFn` — lets modes hook UI callbacks into extproc bridges
- `demo.py` example extension covering every extproc API method and all ten lifecycle events; blocks tools prefixed `"blocked:"` via `hook/tool_call`
- `demo_ext_test.py` — 27 protocol-level Python tests for `demo.py` driven by `FakeFir` in-memory JSON-RPC server
- `demo_integration_test.go` — 19 Go e2e tests spawning the real `demo.py` via a background pump goroutine
- `Context.send_user_message()` added to the Python SDK (was missing; bridge already handled it)
- Python static checks: `make lint-python` runs ruff + ty; `make test-python` runs all Python tests
- Python SDK: `ReadStream`/`WriteStream` protocols replacing `IO[str]`, enabling simple test fakes
- Unified Python tooling config in `pyproject.toml` (ruff + ty) — removed separate `ruff.toml` and `ty.toml`
- Python extension tests moved from `.fir/extensions/` to `pkg/extproc/sdk/python/`
- `/reload` now reloads extproc extensions — re-discovers `.py`/`.sh` files without restarting the session
- External process extensions: write extensions in any language communicating via JSON-RPC 2.0 on stdio
- External process extensions: `notify` and `set_status` calls configurable `NotifyFn`/`SetStatusFn` on Bridge
- External process extensions: `Manager.ConfirmFn` for interactive trust prompts
- External process extensions: `CallHook` fans out concurrently across all bridges
- E2E: proper Go test suite in `tests/e2e/` — 48 test cases with embedded mock OpenAI SSE server
- ACP: `/skills` slash command — list and install builtin skills
- Skills: `builtin: true` frontmatter — only embedded skills get binary distribution

### Fixed
- Paste image (Ctrl+V): macOS `osascript` clipboard script now works — fixed three bugs: `NSPasteboardTypePNG`/`NSBitmapImageFileTypePNG` AppleScript constants caused "plural class name" syntax errors (replaced with string literals `"public.png"`/`"public.tiff"` and integer `4`); `do shell script "mktemp"` failed inside ASObjC scripts (temp file now created in Go); `properties:` is a reserved AppleScript keyword (escaped as `|properties|:`)
- Python SDK: `_workers` thread list now pruned after every 100 entries to prevent unbounded growth in long-running sessions
- `hook/tool_call` wired for extproc extensions in `pkg/extproc/setup.go`
- `handleOutbound` in demo integration test released `rec.mu` before pipe write (prevented `waitOutbound` timeout)
- `doInit` in demo integration test uses constant ID 0 instead of resetting mutable `nextID`
- `Context.set_model()` now returns `bool` (was `None`); surfaces `ok` field from response
- `Context.send_message()` now uses correct field names (`custom_type`/`content`/`display`); was `role`/`content`
- Event handlers in `fir_ext.py` dispatched in worker threads — prevents deadlock when handlers call `ctx.xxx()`
- `notify.py`: writes to `/dev/tty` directly instead of stderr to avoid capture by the extproc pipe
- Python extensions silently skipped: `ConfirmFn` never wired; now auto-trusts with stderr notice
- Python extensions never received `session_start` / proper `session_shutdown`; fixed via `EmitSessionStart()`/`EmitSessionShutdown()`
- `settings.json` stale `extensions` array removed
- Bash command color: `CLICOLOR=1` added to color-forcing env vars for macOS BSD tools
- `gh api` calls in update check strip `CLICOLOR_FORCE`/`FORCE_COLOR` to prevent ANSI in JSON
- Clear command status TUI when user submits new input
- `Manager.Reload` passes context as parameter instead of storing in struct
- `/reload` no longer calls `addExtensionTools` twice
- ACP: `/skills` output uses markdown table

### Changed
- Renamed `pkg/extproc` → `pkg/extension` — the package name now matches the user-facing concept; import path is `github.com/kfet/fir/pkg/extension`
- Python SDK: `Callable` import moved to `TYPE_CHECKING`; `id` params renamed to `msg_id`; modernised annotations
- E2E skill reduced to ~130 lines running `make test-e2e`
- Skills: `builtin: true` frontmatter distinguishes distributable from project-only skills
- Refactor: `.fir/skills` is a symlink to `pkg/core/builtin_skills` — single source of truth
- Refactor: `StripAnsi`/`AppendColorEnv` consolidated into `pkg/core/tools/ansi.go`
- Theme: dark theme `toolOutput` color brightened from `#808080` to `#b0b0b0`


## [0.9.0] - 2026-02-28

### Added
- Debug logging via `--debug` flag or `FIR_DEBUG=1` env var — writes timestamped debug messages to stderr without interfering with stdout protocols (RPC, ACP, JSON); instrumented in all modes (ACP, RPC, print, interactive)

### Fixed
- ACP: MCP server failures are now non-fatal — session starts without the failed server's tools and the error is reported to the client as an agent message
- ACP: `session/prompt` errors (e.g. no model selected, API key missing) now return JSON-RPC error responses instead of silently returning `end_turn` with no content — fixes "immediate return with no response" in Zed
- MCP: tool names are now sanitized to match LLM provider constraints (`^[a-zA-Z0-9_-]{1,128}$`) — fixes Anthropic 400 errors when MCP server/tool names contain dots or other special characters
- Interactive: Escape key now dismisses command status messages (e.g. `/queue` output)
- ACP: assistant responses now always reach the client — when a provider returns the full response without streaming deltas, the complete text is sent on message_end as a fallback
- ACP: error messages from the LLM (e.g. API failures) are now forwarded to the client instead of silently swallowed

### Changed
- Skills: all skills made project-independent — removed hardcoded paths, use `$PROJECT_ROOT` and `$SKILL_DIR` variables
- Skills: extracted reusable bash snippets into `scripts/` directories (monitor/snapshot.sh, notify/notify.sh)
- Skills: fixed typo in work skill description

### Fixed
- Interactive: split `statusContainer` into separate `activityContainer` (spinners) and `commandStatusContainer` (command results) so they no longer overwrite each other
- MCP: `Manager.Reload` is now safe for concurrent calls — serialised by an internal `reloadMu` mutex
- MCP: `Manager.Status()` now reflects post-startup server disconnections — `Connected` becomes false and any error is recorded via a background `session.Wait()` goroutine
- MCP: `Manager.Reload` now deletes servers from `m.subscribed` to prevent a minor memory leak when servers are removed from the config
- MCP: `WatchConfig` stop semantics documented as best-effort in the function comment

### Added
- Built-in skills: 10 portable skills (overseer, research, review, fix, loop, monitor, notify, tmux-driver, claude-usage, skills) embedded in the binary, loaded as `source=builtin` with lowest priority
- `fir skills [list]` CLI subcommand lists all loaded skills with name, source, and description
- `fir skills install <name> [--user] [--force]` extracts a builtin skill to project or user directory
- `/skills` slash command lists loaded skills inline; `/skills install <name>` extracts to project dir
- `/queue` slash command shows the follow-up message queue with 1-based numbered previews; `/dequeue [N]` removes a single item by index (existing Alt+Up restores all as before)
- MCP: hot reload — `WatchAndReload` watches `mcp.json` for changes and incrementally starts/stops servers; new servers connected, removed servers closed, changed servers restarted, unchanged left alone
- MCP: `NewToolServer` exposes fir `agent.AgentTool` values as an MCP server, enabling external MCP clients to call fir's tools
- MCP: `CompletePromptArg` and `CompleteResourceURI` helpers expose MCP argument completions for interactive use
- MCP: handle `AudioContent`, `ResourceLink`, and `EmbeddedResource` content types from MCP tool results (rendered as structured text)
- MCP: debug wire-level transport logging when `verbose=true` (`LoggingTransport` wraps all transports)
- MCP: `Manager.Status()` reports per-server connection health (connected/disconnected/error)
- MCP: keepalive ping every 30 s to detect dead server connections automatically
- MCP: `MergeConfigs`, `LoadDefaultConfigs`, `DefaultConfigPaths` — load and merge user (`~/.fir/mcp.json`) and project (`.fir/mcp.json`) configs with project taking precedence
- MCP: implement elicitation support; set `Manager.ElicitationFn` to handle `elicitation/create` requests; defaults to graceful decline for headless sessions
- MCP: implement `sampling/createMessage` support; set `Manager.SamplingFn` (or use `NewSamplingFn`) to let MCP servers request LLM calls through fir
- MCP: expose MCP prompt templates as `list_prompts` and `get_prompt` tools per server (text, image, embedded resource content)
- MCP: subscribe to resources on servers that support it; `OnResourceUpdated` callback fires when a subscribed resource changes
- MCP: expose MCP resources as `list_resources` and `read_resource` tools per server (text + blob content supported)
- MCP: support SSE (`"sse"`) and streamable HTTP (`"streamable"`) transports via `transport` and `url` fields in `mcp.json`
- MCP: receive and route server log messages through slog at the appropriate level; request `debug` level when verbose, `warning` otherwise
- MCP: advertise filesystem roots to MCP servers via `roots` field in `mcp.json`; defaults to process working directory
- MCP: handle paginated tool lists via the SDK iterator (fixes silent tool truncation on servers with many tools)
- MCP: handle dynamic tool list changes (`ToolListChangedHandler`) and forward progress notifications to tool callbacks

### Fixed
- MCP: `ToolListChangedHandler` no longer races on `m.OnToolsChanged`; stale-session guard prevents old sessions from overwriting tools after a hot reload
- MCP: removed duplicate `serverConfigEqual` helper; `Reload` and `WatchAnd- Auto-resume agent after both `/compact` (manual) and auto-compaction when there is pending work (unanswered user message, unprocessed tool result, or pending tool executions). Shows "Working..." spinner and resumes seamlessly. Unified handling: if cancelled, show status and stop; if pending work, show "Working..." and resume; otherwise show completion status.
- Overseer skill: each fleet now gets its own git worktree + branch (`fleet/<session>`); all agents work inside it
- Overseer skill: auto-resume mid-task agents after `/compact` or rate-limit pause with `"Continue."`
- Overseer skill: recommend cheap model (Haiku) since it doesn't write code
- Overseer skill: adaptive poll frequency — slows down at 60%+ usage to conserve tokens
- Overseer skill: snapshot each agent before rate-limit escape for clean resume on wakeup
- Overseer skill now watches for both hourly and daily limits

## [0.8.0] - 2026-02-26

### Added
- MCP client support: configure external MCP servers in `.fir/mcp.json`; tools appear alongside built-in tools; ACP mode accepts `mcpServers` in `session/new`

## [0.7.0] - 2026-02-25

### Fixed
- Kitty keyboard protocol now detected inside tmux when outer terminal is Ghostty (via `GHOSTTY_BIN_DIR`) or WezTerm (via `WEZTERM_EXECUTABLE`), enabling shift+enter for newline insertion in tmux sessions. modifyOtherKeys is skipped in this case because tmux mangles shift+enter into ctrl+j in modifyOtherKeys mode.
- Shift+Enter no longer fires the "follow-up" action on terminals that send `\x1b\r` for shift+enter (legacy terminals and tmux without modifyOtherKeys pass-through). `CustomEditor` was consuming `\x1b\r` as `alt+enter` (ActionFollowUp) before the editor's built-in legacy newline handler could see it; the fix passes `\x1b\r` directly to the editor in both Kitty and non-Kitty modes. Terminals with modifyOtherKeys support continue to use the unambiguous `\x1b[27;3;13~` sequence for alt+enter.
- Ctrl+C and Ctrl+D now work correctly inside tmux with `extended-keys always` — tmux's default `extended-keys-format xterm` sends modifier keys as `\x1b[27;mod;key~` (modifyOtherKeys) for plain letters too, which `MatchesKey` now handles.
- Shift+Enter now correctly inserts a newline again. The fix is two-pronged:
  - `modifyOtherKeys` level 2 (`\x1b[>4;2m`) is enabled for xterm/iTerm2-compatible terminals, sending `\x1b[27;2;13~` for Shift+Enter.
  - The Kitty keyboard protocol (`\x1b[>1u`) is enabled for Ghostty, WezTerm and Kitty terminals (detected via `TERM_PROGRAM`/`KITTY_WINDOW_ID`), sending `\x1b[13;2u` for Shift+Enter.
- `MatchesKey` now correctly parses `\x1b[13;2~` (CSI-tilde shift+enter variant) via the Kitty sequence parser (`funcCodes[13]=cpEnter`); the editor's hardcoded special-case for that sequence is removed.

### Changed
- Extension conflict resolution: tools, commands, and flags now use first-registration-wins (project-local extensions take precedence over global ones).
- Duplicate extension commands now log a warning and are skipped instead of silently overwriting the previous registration.

### Added
- New skill - shepherd (formerly overseer) - shepherds a fleet of agents to perform a task

## [0.6.0] - 2026-02-24

### Added
- Dev version tagging: non-release builds now show `0.5.0-dev+abc1234.dirty` instead of bare version, making it easy to identify exact commit and dirty state.
- `/changelog` now includes unreleased changes at the bottom.
- `/reexec` slash command: re-execs into the current binary preserving the active session, useful for picking up a rebuilt binary or testing a different branch.
- Queue indicator in footer: shows `📬 N queued` when follow-up messages are waiting while the agent is streaming.
- Line prompt `⟩ ` displayed at the start of the editor input box.
- `/changelog` output now uses theme colors: version headers in `mdHeading`, section names colored by type (Added=success, Fixed=accent, Changed=warning, Removed=error), styled `•` bullets, and dim rule borders at start/end.

### Fixed
- Messages submitted (Enter) while the agent is streaming are now queued as follow-ups instead of being silently dropped.
- `fir update` now self-updates on macOS (previously printed a "use `brew upgrade fir`" stub and exited).
- Update notice no longer suggests `brew upgrade fir` on macOS; shows `fir update` on all platforms.

## [0.5.0] - 2026-02-24

### Added
- `fir update` subcommand: on Linux/RPi downloads the latest release binary from GitHub and atomically replaces itself; on macOS prints instructions to use `brew upgrade fir`. For private repos, falls back to the `gh` CLI for authentication.
- Startup version check: async background check against GitHub Releases (24-hour cache); in interactive mode the notice appears inside the TUI at startup; in print mode it prints to stderr after the response — `brew upgrade fir` on macOS, `fir update` on Linux.
- `pkg/update`: new package with `CheckLatest` (cached), `FetchLatest` (HTTPS), `FetchLatestOrGH` (HTTPS → gh fallback), `SelfUpdate` (HTTPS → `gh release download` fallback), `CurrentPlatform`, `UpdateNotice`, `IsNewer`, and `HasGH`.
- GoReleaser config (`.goreleaser.yaml`): cross-compiles for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, linux/arm6; generates `checksums.txt`; pushes Homebrew formula to `kfet/homebrew-fir` tap.
- GitHub Actions: `release.yml` runs GoReleaser on `vX.Y.Z` tag push; `ci.yml` runs vet + tests on push/PR.
- `install.sh`: platform-detecting install script; tries `gh release download` first (private repo support), falls back to `curl`/`wget`.
- Help text: `fir update` now shown in usage and examples.
- ACP mode: `/share` command creates a secret GitHub Gist and returns both the raw gist URL and a `https://gistpreview.github.io/?{id}` preview link.
- ACP mode: `/export` command exports the session to an HTML file (usage: `/export [path]`).
- Built-in Dracula theme (`--theme dracula` or `/theme` → dracula), using the canonical Dracula palette.
- Built-in Gruvbox theme (`gruvbox`): retro earthy dark — warm yellows, greens, orange on charcoal.
- Built-in Nord theme (`nord`): arctic minimal — cool blue-grays from Polar Night through Frost.
- Built-in Catppuccin Mocha theme (`catppuccin-mocha`): modern pastel dark — soft mauve, sky, green on deep navy.
- Bundled themes (`dark.json`, `light.json`, `dracula.json`) are now embedded in the binary via `//go:embed`; `GetAvailableThemes` discovers them automatically without needing files on disk.

### Fixed
- TUI: `fir -c` / `fir -r` now displays the full previous conversation on startup instead of an empty chat.
- Input box: buffered keystrokes (e.g. multiple rapid backspaces arriving in one OS read) no longer silently dropped — `SplitKeySequences` splits the raw buffer into individual sequences before dispatch, fixing remaining delete lag.
- Input box: holding backspace no longer causes TUI to struggle — consecutive backspaces are now batched into a single undo entry (like typed words), and the undo stack eviction now correctly releases evicted string copies immediately.
- Input box: `Render()` scrolling now uses display-column widths instead of byte offsets, fixing garbled output when emoji or CJK characters exceed the terminal width.
- `formatDiagnostics` in interactive mode now outputs collision groups in deterministic (sorted) order.
- `TestGetTheme_LazyInit` now correctly saves/restores `globalTheme` under `globalThemeMu` to avoid data races under `-race`.
- Removed dead `OnThemeChange`/`onThemeChangeCb` code from the theme package (was exported but never called).
- Added tests for embedded theme functions: `TestEmbeddedThemeNames`, `TestLoadEmbeddedTheme_Valid/Invalid`, `TestGetAvailableThemes_IncludesEmbedded`, `TestInitTheme_EmbeddedTheme`.
- `/theme` (and `/thinking`, `/help`, `/clear`) now appear in autocomplete; they were missing from `BuiltinSlashCommands` despite being valid commands.
- `--no-themes` now disables all theme discovery (was only suppressing `--theme` CLI flag paths; `agentDir/themes` and settings paths were still searched).
- `get_messages` RPC command now returns `[]` instead of `null` for a fresh session.
- `--export <file>` CLI flag now exports the session to HTML and exits (was silently ignored, starting the TUI instead).
- Theme directories from `settings.json` (`themes` field) are now included in the theme search path (were previously silently ignored).
- Theme name is now read from settings (`GetTheme()`) instead of being hardcoded to `"dark"`.
- `~/.fir/agent/themes/` is now automatically searched for custom theme JSON files.
- `--theme <file|dir>` CLI flag is now wired through to the theme search dirs (was parsed but silently ignored).
- `/theme` selector and settings selector now show all discovered custom themes, not just `dark`/`light`.
- `/theme` live-preview now repaints the full TUI (messages, tool output, markdown) as you navigate, not just the selector panel itself.
- Dracula `dim` colour changed from `currentLine` (`#44475a`, contrast 1.56:1) to `comment` (`#6272a4`, contrast 3.03:1) — the status line was unreadable.

## [0.4.0] - 2026-02-22

### Added
- `ResolveCliModel` for sophisticated CLI model resolution: supports `--model provider/model`, fuzzy matching, thinking-level suffix (`--model claude:high`), and OpenRouter-style IDs.

### Fixed
- `settings.go`: `~/.fir/agent` is no longer created unconditionally on startup — only created when settings actually need to be written.
- Tool execution UI now shows `[invalid arg]` instead of `...` when the LLM sends a non-string value for a tool argument (path, pattern, content, etc.).
- `models_generated.go`: Updated MaxTokens and pricing for several models (openrouter, gemini).

## [0.3.1] - 2026-02-22

### Fixed

- Auto-compaction now retries the original user message instead of attempting a retry with the compacted history, which could cause the message to be lost in certain overflow scenarios.

## [0.3.0] - 2026-02-22

### Added

- E2E test coverage for 27 new scenarios: gemini-2.5-pro model lookup, all previously-untested RPC commands (set_model, cycle_model, cycle_thinking_level, bash, get_session_stats, get_messages, get_commands, get_last_assistant_text, set_session_name, get_fork_messages, new_session, set_auto_compaction, set_steering_mode, set_follow_up_mode, abort_bash, abort_retry, export_html) and their error paths, plus ErrAgentAborted non-zero exit and bad-provider-config no-panic regression tests.

### Fixed

- `pkg/modes/acp/conn.go` — `initialize` handler now falls back to returning the plain struct on marshal/unmarshal error instead of sending a `null` ACP handshake response.
- `/export [path]` command exports the current session to an HTML file (temp file if no path given).
- `/share` command creates a secret GitHub gist from the exported session (requires `gh` CLI).
- `/scoped-models` command now opens the interactive scoped-model selector (was "not yet implemented").
- `Ctrl+G` (`externalEditor` action) opens `$VISUAL` or `$EDITOR` to edit the current input.
- `ActionFollowUp` queues the current editor text as a follow-up message delivered after the ongoing agent turn.
- `ActionDequeue` restores any queued follow-up messages back to the editor.
- `SetScopedModels()` on `AgentSession` — session-level scoped model override used by `/scoped-models`.
- `AgentSession.ExportToHTML(path)` and `core.WriteConversationHTML` shared by interactive and RPC modes.
- `TreeSelectorComponent.SetOnLabelEdit()` and `SetInitialSelection()` for label editing and pre-selection.
- `Agent.GetAndClearFollowUpQueue()` for atomic queue inspection+clear.
- `AgentSession.ClearFollowUpQueue()` returns and drains queued follow-up message texts.
- Clipboard image paste (`handleClipboardImagePaste`) now wired via `editor.OnPasteImage` — writes image to temp file and inserts the path at cursor.
- Include agent version in `/session` slash command output (ACP and TUI modes).

### Fixed

- `/resume` session selector: `SortRelevance` mode now uses the rich search engine (`FilterAndSortSessions` with fuzzy/phrase/regex token support) instead of silently falling through to threaded; all sort modes (`threaded`, `recent`, `relevance`) now call `FilterAndSortSessions` for consistent search behaviour.
- `/resume` session selector: strip ASCII control characters (newlines, carriage returns, etc.) and Unicode line/paragraph separators (U+2028, U+2029) from session names AND working-directory paths before rendering; a session whose name or `cwd` contained an embedded newline caused each such session in the visible window to write an extra terminal row, shifting all subsequent rows down — the "items 73-83 (or 83+) shift down one line" visual glitch. Also sanitize DEL (0x7F) and C1 control codes (U+0080–U+009F).
- `/resume` session selector: increased visible window from 12 to 20 sessions (scroll pane is taller, easier to navigate large session lists).
- `/changelog` command now displays newest version last (at the bottom of the terminal) so it's most visible in both TUI and ACP modes.
- `/tree` command now shows the full interactive `TreeSelectorComponent` overlay instead of a static text dump — users can navigate, switch branches, and edit labels.
- `/fork` command now shows the interactive `UserMessageSelectorComponent` overlay instead of a static text list — users can navigate and select a message to branch from.
- Double-escape action now respects the `doubleEscapeAction` setting (`"tree"`, `"fork"`, or `"none"`); previously always opened the session selector.
- `ActionTree` and `ActionFork` keybindings now correctly call `showTreeSelector` and `showUserMessageSelector` (were not wired to any handler).
- `cycleModel` (Ctrl+P) now cycles only within the configured scoped model set when one is active; falls back to all available models otherwise.
- `Prompt()` with `StreamingBehavior = "followUp"` now correctly enqueues the message via `Agent.FollowUp()` instead of returning an error.
- Settings selector now reads `DoubleEscapeAction` from settings instead of hardcoding `"tree"`.
- RPC `export_html` now delegates to `AgentSession.ExportToHTML()` (eliminates code duplication).
- `OnPasteImage` (Ctrl+V) now wired in interactive mode — reads image from clipboard, writes to temp file, inserts path in editor.
- `pkg/modes/acp/terminal.go`: `AcpBashExec` now removes the terminal from `pendingBashTerminals` immediately after `ReleaseTerminal` in all exit paths, eliminating a double-release race with `CleanupPendingBashTerminals`.
- `pkg/modes/acp/acp.go`: `createSession` now propagates the caller's context to `core.CreateAgentSession` instead of using `context.Background()`.

### Changed

- Changelog embed: replaced `main.changelogB64` ldflags trick with `//go:embed CHANGELOG.md` in `cmd/fir/changelog_init.go`; `cmd/fir/CHANGELOG.md` is the embedded source.

## [0.2.0] - 2026-02-19

### Added

- ACP (Agent Client Protocol) mode over stdio JSON-RPC 2.0 for IDE integration.

### Changed

- `/changelog` command now uses embedded changelog content instead of looking for a file next to the binary.

## [0.1.0] - 2026-02-19

### Added

- Initial release of fir, a Go port of the pi-mono coding agent.
- Full agent loop with streaming LLM support (Anthropic, OpenAI, Google, Poe).
- TUI with fuzzy autocomplete, `/`-menus, and model picker.
- RPC mode over stdio for programmatic integration.
- Extension system with sandbox support and init()-based registration.
- Session management with resume, continue, and export.
- Tool execution framework with bash, read, write, edit, and glob.
- OAuth callback server for provider authentication.
- Compaction with progress UX.
- Model generation from upstream TypeScript definitions.
- Cross-compilation for darwin/linux (arm64, amd64, arm6).
- PGO-optimized build support.
- Thinking level control (off, minimal, low, medium, high, xhigh).
- Skill and prompt template system.
- HTML session export.
