# Changelog

## [Unreleased]

### Added
- ACP mode: `/share` command creates a secret GitHub Gist and returns both the raw gist URL and a `https://gistpreview.github.io/?{id}` preview link.
- ACP mode: `/export` command exports the session to an HTML file (usage: `/export [path]`).

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
