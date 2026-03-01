# Changelog

## [Unreleased]

### Added
- ACP: auth methods RFD support — `Initialize` now returns typed auth methods (`agent`, `env_var`, `terminal`) per [agentclientprotocol.com/rfds/auth-methods](https://agentclientprotocol.com/rfds/auth-methods); `Authenticate` dispatches by method type
- External process extensions: write extensions in any language (Python, bash, etc.) that communicate with fir over JSON-RPC 2.0 on stdio
- External process extensions: `notify` and `set_status` inbound methods now call configurable callbacks (`NotifyFunc`/`SetStatusFunc`) on Bridge, enabling UI integration
- External process extensions: `Manager.ConfirmFn` callback for interactive trust prompts on untrusted project-local extensions
- External process extensions: `CallHook` now fans out concurrently across all bridges instead of sequentially
- External process extensions: `ProjectDir` and `Cwd` fields passed through to `extproc.Manager` from all `extension.Setup` callers
- E2E: proper Go test suite in `tests/e2e/` — 7 test files (1418 LOC) covering all 48 test cases with embedded mock OpenAI SSE server; `make test-e2e` target; `//go:build e2e` tag keeps them out of regular `make test`
- ACP: `/skills` slash command — list loaded skills (`/skills` or `/skills list`) and install builtin skills (`/skills install <name> [--user] [--force]`)

### Changed
- E2E: skill reduced from 1031 lines of manual test procedures to ~130 lines that run `make test-e2e` and report failures
- Skills: added `builtin: true` frontmatter property to distinguish distributable builtin skills from project-only skills; only skills with this property are embedded in the binary; project-specific skills (e2e, release, sync, work) excluded from distribution
- Refactor: `.fir/skills` is now a symlink to `pkg/core/builtin_skills` — single source of truth, eliminates duplicate copy; `go:embed` reads the real directory

### Fixed
- ACP: `/skills` output now uses a markdown table so it renders correctly in ACP clients instead of collapsing into a wall of text
- TUI: bash output now preserves original ANSI colors from commands (e.g. `git diff`, test runners, `ls --color`) — injects `CLICOLOR_FORCE=1` and `FORCE_COLOR=1` so tools emit colors even through pipes, applies to both `!`/`!!` bash mode and AI-invoked bash tool calls

### Removed
- `sandbox` extension (incomplete framework with no real OS-level enforcement)

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
- MCP: removed duplicate `serverConfigEqual` helper; `Reload` and `WatchAndReload` share a single `configsEqual` function
- MCP: `CompletePromptArg`/`CompleteResourceURI` docs now accurately state that errors are propagated (not silently converted to empty slices)
- MCP: progress notifications no longer silently dropped when the SDK's `handleAsync` goroutine dispatches after `CallTool` returns
- MCP: `ImageContent` with non-image MIME types (e.g. `application/pdf`) no longer tagged as `ContentTypeImage`, preventing API errors on strict providers
- MCP: resource blob content with non-image MIME types returned as base64 text rather than `ContentTypeImage`
- MCP: `file://` root URI now correctly percent-encodes paths with spaces or special characters
- MCP: unknown `transport` values in `mcp.json` now return an error instead of silently falling through to stdio
- Core: `runAutoCompaction` now resumes when `PendingToolCalls` are non-empty, matching `HasPendingWork()` logic

### Changed
- Auto-resume agent after both `/compact` (manual) and auto-compaction when there is pending work (unanswered user message, unprocessed tool result, or pending tool executions). Shows "Working..." spinner and resumes seamlessly. Unified handling: if cancelled, show status and stop; if pending work, show "Working..." and resume; otherwise show completion status.
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
- New skill - overseer - shepherds a fleet of agents to perform a task

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
