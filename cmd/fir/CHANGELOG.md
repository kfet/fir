# Changelog

## [Unreleased]

### Added

- ACP mode now emits the `dev.acp-kit.status-line/v1` extension in `_meta` on every `session/update` notification, carrying the current mood and plan slugs from the observable cards store. ACP relays (poe-acp, slack-acp) use this to render a status header. When no mood/plan cards are present, the `_meta` key is omitted entirely.

### Changed

- `wt` builtin skill now instructs the parent agent to prepend an explicit worktree-discipline pin to the task text when spawning. Without it, spawned agents tend to misread "Repo: ~/dev/ai/foo" in the task as "cd there and commit to main" rather than staying in the dedicated worktree. The skill includes a template pin and calls out the failure mode by name.

## [0.50.1] - 2026-05-26

### Changed

- `tmuxspinner` extension overhauled: the timer-driven braille spinner is replaced with a box-drawing spinner glyph (`│ ╱ ─ ╲`, U+2502/2571/2500/2572) in the tmux window title. Title layout: `{tab} {session} {glyph}` (e.g. `fir mysess │`). The glyph cycles at 1 Hz as a peripheral-vision liveness cue. Box Drawing is the Unicode block screen readers (NVDA/VoiceOver/JAWS/Orca) commonly skip at normal verbosity, so a11y stays quiet. ASCII alternatives with a backslash frame don't survive tmux's `rename-window`, which runs titles through `strvis(3)` for terminal-escape-injection defense (cascades a literal backslash on every format-expand pass — tmux issue #2070). When the composed title exceeds 30 display columns, parts are dropped in priority order: tab first, then session — the glyph is always preserved. Crash-recovery via the stashed `@fir_original_name` window option still works. Pure stdlib — no `wcwidth` dependency, so the extension works on a fresh macOS install with no extra setup.
- TUI loader (`pkg/tui/components/loader.go`) now uses an ASCII spinner (`| / - \`) instead of the braille frames, fixing tofu rendering in terminals/SSH sessions without the U+2800–U+28FF block. The same change is applied to the inline tool-execution spinner. After 30 s of spinning without a `SetMessage` (i.e. no streaming progress), the loader appends a compact elapsed counter (`Inferring... 45s`, `1m05s`, `1h05m`).
- `plan-nudger` builtin extension rewritten as single-trigger, side-query-driven handler. When `metadata.progress_metric` is unchanged across >=2 consecutive plan updates, calls `ctx.side_query(PROMPT)` on the current session model (no advisor escalation, no config) and injects the reply verbatim as a `followUp`. Empty reply = noop. Re-fire guard clears on stagnation reset so oscillating metrics still re-fire. All judgement delegated to the model; extension is just a trigger and transport. Previous hand-rolled body composition (idle-turn / wall-clock backstops) removed.

## [0.50.0] - 2026-05-26

### Added

- Streaming `side_query` flavor across the extension API. New `ctx.side_query_stream(...)` Python SDK method returns an iterable yielding `SideQueryDelta` objects (text/thinking/usage) as the host streams them, with a final `SideQueryResult` on `.result`. Each delta resets the per-RPC deadline, so long-running advisor calls cannot trip the timeout. The blocking `ctx.side_query(...)` flavor keeps its byte-for-byte wire shape, but its default timeout is bumped from 120s to 600s and now honors `FIR_SIDE_QUERY_TIMEOUT` (seconds). Wire protocol uses correlated `side_query/delta` notifications keyed by the originating request id in `params.request_id`; SDK ignores unknown delta `type`s for forward compatibility. Spec: `docs/design/streaming-side-query.md`.
- `aside` extension now publishes one observable card per side query (`source=aside`, `key=query/<unix-ms>`) and updates it in place as the stream runs — slug ticks through `running` → progress (e.g. `2.1kc`) → terminal `stop` / `empty:redacted` / `ERR`. Detail holds the running partial text, then the full text on success or the block summary on failure. The card is not cleared on completion: historical state is the whole point. Card layer is the cards sidecar from `docs/design/observable-cards.md` — no new persistence machinery.
- `SimplePromptStream` on `*agent.Agent` + `SideQueryStream` on `*session.AgentSession`: the streaming primitives the extension wire builds on. Same NO-COMPACTION contract as their blocking counterparts.

### Fixed

- "Response had no usable content" errors from `SimplePrompt` / `SideQuery` now include a per-block summary inline (e.g. `(blocks: [thinking(th=0,sig=940)])`), so callers can classify redacted-thinking failures without keeping the raw response. Previously the raw response was thrown away and the error was uninformative.

## [0.49.0] - 2026-05-25
### Added

- `antigravity-models` skill (non-bundled, global): probes Google Antigravity's `/v1internal:streamGenerateContent` endpoint with stored OAuth creds to discover which model IDs are actually live, plus a companion scraper that pulls the desktop-app fingerprint (version, User-Agent, X-Goog-Api-Client) from `/Applications/Antigravity.app`. Antigravity has no public list-models endpoint and ships its menu via a server-pushed protobuf gRPC stream that isn't trivially replayable, so probing is the only reliable way to keep `pkg/resources/builtin_extensions/antigravity_auth.py` in sync. Status-code classifier: `200/400/429/500` → exists; `404` → missing. Run the probe whenever a new Gemini/Claude release lands or after an Antigravity desktop-app update; diff against `antigravity_auth.py` and apply changes.
- `gemini-3.5-flash-low` model added to the Antigravity provider catalogue (`pkg/resources/builtin_extensions/antigravity_auth.py`). Discovered via the new `antigravity-models` probe skill — the model is live on `cloudcode-pa.googleapis.com` under the `-low` suffix only (`gemini-3.5-flash` and `gemini-3.5-flash-high` are 404 today). Pricing inherits from models.dev's `google/gemini-3.5-flash` entry (1.5 / 9 / 0.15 / 0.083 per M tokens; 1,048,576 context / 65,535 max).
- `handoff` extension gains a new `bookmark(quote, note)` tool — pin any past turn (user message, assistant message, tool call, tool result, system message) as significant. Quotes are reverse-scanned against the session JSONL (substring-matched against decoded turn text, latest-first; most recent wins on ambiguity); the matched turn entry is copied as-is to `bookmarks-<session-id>.jsonl` next to the transcript, with the note injected as `_bookmark_note`. The file is kept sorted by the original turn's `timestamp` so it reads chronologically regardless of bookmark-call order. `self_handoff` appends a pointer line to its `prepend_context` when the bookmarks file is non-empty so the child session reads the high-fidelity highlight reel directly via `read`/`grep` — no new child-side tools, no model re-authoring at the moment of handoff. An observable card (`handoff/bookmarks`) is published on every bookmark call; survives `/reexec` via the cards-file-on-construct mechanism (no event reconciler needed). Designed in `docs/design/handoff-bookmarks.md`.

### Fixed

- Antigravity provider catalogue had `gemini-3.1-flash-light` (with `-light`) which returns 404 from Cloud Code Assist; the real ID is `gemini-3.1-flash-lite` (with `-lite`). Renamed in `pkg/resources/builtin_extensions/antigravity_auth.py`. Discovered via the new `antigravity-models` probe skill.
- MCP channel history and auto-reply plumbing no longer carries Poe-specific wording or server-name assumptions; the core now describes and handles message history/replies by generic channel capabilities (`history` metadata and `message_id` reply tools).

### Changed

- Antigravity provider `auth_list_models` hook is no longer a no-op — it now opportunistically probes `/v1internal:streamGenerateContent` for every ID in the static catalogue and returns only the live ones, so stale catalogue entries are hidden from the model picker automatically. Status-code classifier: `200/400/429/500` → live; `404` → missing; everything else (network/auth) → permissive (treat as live). A pre-flight probe against one known-good ID (`gemini-3-flash`) is fired first; on `401/403/0` the hook aborts before the parallel sweep, saving 12 wasted requests on a dead token. Bails to `None` (= no filter) if every probe is 404, since that's almost always an auth/endpoint problem rather than a fully stale catalogue. Probe runs in fir's existing live-models background goroutine and is cached for 1 hour (`pkg/models/live_models.go:liveCacheTTL`), so it adds zero cost to the cold-start hot path. Set `FIR_ANTIGRAVITY_DISABLE_PROBE=1` to opt out (recommended for paid users who'd rather not spend tokens on diagnostics; default-on is appropriate for free-tier where the saving from auto-hiding stale entries outweighs the ~13 probe-tokens/hour). Catalogue *additions* (genuinely new model IDs) still go through the `antigravity-models` discovery skill — the in-extension probe can only filter what it already knows about. 14 tests in `pkg/resources/testdata/antigravity_auth_test.py` cover classifier semantics, pre-flight short-circuit, all-404 fallback, env-var opt-out, missing-creds handling, and presence of the renamed/added catalogue IDs.
- Antigravity client User-Agent bumped from `antigravity/1.21.9` to `antigravity/1.107.0` (matches the locally installed Antigravity desktop app's `package.json` version). Override with `PI_AI_ANTIGRAVITY_VERSION` if needed. Set in `pkg/resources/builtin_extensions/antigravity_auth.py`.

## [0.48.1] - 2026-05-21

### Fixed

- `mood` extension was bundled in v0.48.0 but missing `builtin: true` frontmatter, so the manager neither listed it (`fir extensions` omitted it) nor auto-loaded it — leaving the tool inert on fresh installs. The filter at `cmd/fir/extensions.go:106` and `pkg/resources/builtin_extensions.go:LoadBuiltinExtensions` skips any embedded script without `builtin: true` or `explicit: true`. Now correctly registered as a builtin. The bug was masked in the dev worktree because `.fir/extensions/` is a symlink to `pkg/resources/builtin_extensions/`, so project-scope discovery picked the file up regardless of frontmatter — only end users on fresh `brew install` / `fir update` saw the missing extension.
- `autoresearch` extension had the same accidental defect (commit `b9cd9b89` introduced it as "builtin extension" per its own message, but the frontmatter never declared `builtin: true`). Same symptom: `run_experiment` / `log_experiment` / `/autoresearch` were unavailable on fresh installs while appearing to work in-repo via the project symlink. Now correctly registered as a builtin alongside `mood`. The `schedule` and `provider-usage` extensions both retain explicit `builtin: false` plus `modes: tui` — those are intentionally mode-restricted, not regressions, and remain untouched.

## [0.48.0] - 2026-05-21

### Added

- Observable cards (`<sessionFile>.cards`) — per-session sidecar of state summaries that producers (extensions, plan tool, core) publish through a single primitive. Designed in `docs/design/observable-cards.md`. Plan tool publishes `plan/active` on every mutation; mood extension publishes `mood/current`; `ctx.set_status` is reimplemented as a wrapper over `put_observable("footer", ...)`. Extension SDK gains `ctx.put_observable(key, slug, detail)` and `ctx.clear_observable(key)`; source and `entry_id` are stamped host-side and cannot be spoofed. Extension names `plan`, `model`, and `session` are reserved and rejected at startup. `observe_session` prepends a one-line cards header (`plan: 3/8 in_progress  ·  mood: #engaged`) and gains `--ext <name>` / `--raw_json` flags. Observe's discovery sidecar gains a `cards_path` field. Plan progress is now visible across sibling agents through `observe_session` without parsing transcripts.
- `fir --agent-dir <dir>` overrides the global config/session root for one invocation (same target as `FIR_AGENT_DIR`, with the CLI flag taking precedence), covering auth/settings/sessions/debug logs/packages and user-level MCP config.

### Fixed

- The interactive `/resume` selector's "all sessions" scope now uses the active agent dir (including `--agent-dir` / `FIR_AGENT_DIR`) instead of always scanning the default `~/.config/fir` session root.

## [0.47.1] - 2026-05-17

### Fixed

- Homebrew install of `kfet/fir/fir` no longer fails with `unsupported shell: fish (supported: bash, zsh)`. Regression introduced in 0.47.0's brew-smoke CI surfaced — but the bug is older: Homebrew's `generate_completions_from_executable` defaults to invoking the binary for **bash, zsh, and fish**, and fir's `completion` subcommand only handles bash and zsh, exiting non-zero on `fish`. Under `brew install` that non-zero exit kills the whole install (`Failure while executing; \`{"SHELL" => "fish"} .../bin/fir completion fish\` exited with 1`). Fixed by passing `shells: [:bash, :zsh]` to `generate_completions_from_executable` in both the rolling `fir` and pinned `fir@X.Y` formula blocks of `.goreleaser.yaml`. The new brew-smoke job (added in 0.47.0) caught it on the first release — exactly the kind of end-to-end gap it was built to close. 0.47.0 binaries themselves are fine; only the brew install path is affected, and 0.47.1 supersedes it in the tap.

## [0.47.0] - 2026-05-17

### Changed

- `/skills` listings and `/skill:` autocomplete no longer print full filesystem paths for skills installed under well-known locations. Built-in, project (`.fir/skills/`), and user-scope (`~/.config/fir/skills/`, `~/.config/fir/packages/...`) skills now render as `built-in`, `project`, or `user`; only ad-hoc paths still show the absolute location. New helper `resources.DisplayOrigin` centralises the mapping; used by `cmd/fir/skills.go` (CLI `fir skills list`), `pkg/modes/interactive/commands.go` and `mode.go` (TUI `/skills` list, `/skills <name>` detail, `/skill:` autocomplete), and `pkg/modes/acp/commands.go` (ACP `/skills`). Regression coverage: `TestDisplayOrigin` in `pkg/resources/skills_test.go`.

### Fixed

- `wt` skill's `spawn.sh` no longer false-negatives the "window stuck" verification step. The old check ran `tmux list-windows -a | grep -q " <feature> "`, which depended on the window name being surrounded by spaces in tmux's default list-windows output — but the actual format puts the name right after `: ` (no leading space) and often immediately follows it with a `*`/`-` activity marker, so the match failed intermittently depending on tmux config and window state. Replaced with id-based verification: `tmux new-window -P -F '#{window_id}'` captures the new window's stable `@N` handle, then `tmux display-message -t "$WIN_ID"` checks it's still alive, wrapped in a short retry loop (~5s with 100ms backoff) to also handle the fresh-tmux-server case and survive a brief crash-on-start window. Using the id (not the name) additionally eliminates false-positives from stale windows with the same feature-name in other sessions. The flat `sleep 1` is gone, so the success path is faster. When verification really does fail, the error now dumps `tmux list-windows -a` to stderr so the next investigator has something to look at.
### Fixed

- Homebrew install/upgrade no longer fails with `brew: exec failed (EACCES): .../bin/fir` on the post-install completion step. Regression introduced in 0.33.x when `generate_completions_from_executable(bin/"fir", "completion")` was added to the goreleaser brews install block: the downloaded artifact is a raw binary (`archives.formats=binary`, no archive to carry a Unix mode), it lands in the cellar at 0644 because `bin.install` is just `FileUtils.mv` with no chmod, and exec'ing a non-executable file is EACCES. The kernel rejects the call before fir even starts, the install rolls back, and the user is left on the old version. Fixed by adding `chmod 0755, bin/"fir"` after `bin.install` in both the rolling `fir` formula and the per-minor `fir@X.Y` pinned formula blocks in `.goreleaser.yaml`. Older 0.32.0 installs were unaffected because that release predated the completion call.

### Added

- Release workflow has a new `brew-smoke` job that runs after the tap push and the kfet/fir-dist mirror step, on a matrix of `macos-latest` + `ubuntu-latest`. It does `brew tap kfet/fir && brew install kfet/fir/fir`, runs `fir --version`, exercises `fir completion bash|zsh` (the exact code path that broke with EACCES), and runs `brew test kfet/fir/fir`. Closes the gap that let the previous brew regression ship undetected for several releases — nothing in CI ever exercised the published tap end-to-end. `fail-fast: false` so a macOS-only or Linux-only break still surfaces both signals.
### Fixed

- `ctx.set_status` / `ctx.notify` from extensions silently no-oped in interactive (TUI) mode. The `SetStatusFn` / `NotifyFn` callbacks the mode constructs against its `footerDataProvider` are only available after `TUI.Init`, and `extension.Setup` runs in a background goroutine started *before* `mode.SetExtensionSetup` is invoked — so by the time `Manager.SetSetStatusFn` was finally called the bridges had already started and each one had snapshotted `setStatusFn = nil` at startup. The manager-level setter never propagated to existing bridges, leaving every `ctx.set_status` call from inside a running extension with no path to the footer. Fixed by (a) moving the callback fields on `Bridge` behind `atomic.Pointer[T]` with `SetNotifyFn` / `SetSetStatusFn` setter methods so the host can hot-swap the callback from any goroutine race-free, and (b) making `Manager.SetNotifyFn` / `Manager.SetSetStatusFn` snapshot the live bridge list under the manager mutex and propagate the new fn to every running bridge in addition to caching it for future ones. Regression coverage: `TestManager_SetSetStatusFn_PropagatesToRunningBridges` in `pkg/extension/manager_test.go` registers the callback *after* `Manager.Start`, fires `session_start`, and asserts the extension's `set_status` payload reaches the receiver. Affected every extension that called `ctx.set_status` (demo, plan_nudger, tmuxspinner, mood, etc.) — none of their statuses were visible in the TUI footer until now.

### Added

- `mood` extension (project-local, `.fir/extensions/mood.py`) — lightweight diary / mood-introspection mechanism inspired by Anthropic's "Emotion concepts and functional states" research. Gives the model two tools (`mood_note`, `mood_recent`) and a `/mood [--all]` slash command for an append-only log of brief self-observations stored in `set_session_data("mood_log", …)` (survives `/reexec`). On every `agent_end` a *gating step* asks the advisor (escalated via `ctx.call_tool("aside", {escalate: true, …})`) whether the current moment is a natural pause point; the gate is deliberately conservative — default "no", uncertain "no", parse-fail / RPC error / advisor-unavailable all log a silent gating entry and bail with no nag. When the advisor returns `{"checkin": true}` the extension does **not** synthesise a reflection via `ctx.side_query` (that would be a journal entry written by a clone — the in-session model would never have actually noticed anything, only inherited a note). Instead it calls `ctx.send_message(custom_type="mood_nudge", display=False, deliver_as="steer")` carrying a short ambient-context message; the steered message is converted to a plain user-role message by `store.convertCustomToLLM` (no `[SYS_EXT]` framing — deliberately *plainer* than a prepend, just an additional voice arriving as the model starts its next real turn), self-labelled with a `[mood-introspection]` prefix so the model doesn't misattribute the words to the user. `display=False` keeps it out of the TUI render; the JSONL transcript still records it as an audit trail. The model itself encounters that nudge on its **next real turn** — whenever the user prompts next — and decides for itself whether to call `mood_note` ("nothing notable" is a valid call, and so is skipping it entirely). The advisor's `reason` field is sanitised before splicing — `[SYS_EXT]` / `[/SYS_EXT]` markers stripped, newlines collapsed — so a hallucinating or jailbroken advisor can't smuggle in markers that fir's base system prompt would treat as authoritative. The advisor gate IS the dynamic off-switch — no on/off toggle exposed. The model never volunteers mood content into the visible transcript; the only surfaces are the tools, `/mood`, and the footer. Behaviour coverage in `pkg/extension/sdk/python/mood_ext_test.py` exercises the init handshake, both tools, `/mood` (with and without `--all`), the gating floor, advisor=NO / YES / is_error branches, the steered `send_message` payload (custom_type, display=False, deliver_as=steer, self-label, reason carry-through), explicit anti-assertions that `side_query` and `prepend_context` are *not* used, the marker sanitisation (incl. whitespace-inside-brackets and sanitise-before-truncate ordering), the stale-footer clear after TTL, and log persistence across a simulated `/reexec`.


## [0.46.5] - 2026-05-16

### Fixed

- Anthropic OAuth login (Claude Pro/Max) — token exchange now succeeds again against `https://platform.claude.com/v1/oauth/token`. Anthropic's token endpoint is a non-standard quirk of the Claude-Code OAuth client: it requires the OAuth `state` value to be echoed back in the token-request body. Until v0.43.x the Python `anthropic_auth` extension drove the exchange itself and included `state` in the body; commit `5bcc3d7b` extracted the flow into pinoauth and dropped state, so subsequent logins failed with `invalid_request`. Fixed by (a) bumping pinoauth to v0.3.0 which adds `ExchangeRequest.Extra` / `RefreshRequest.Extra` with reserved-key validation (callers can inject extra token-body fields without being able to overwrite security-critical fields like `client_id` or `code`); (b) extending `OAuthFlowSpec` with `TokenBodyExtra map[string]string` exposed on the wire / SDK as `token_body_extra`, whose values may contain the literal `"{state}"` placeholder that fir substitutes with the per-session state at request time (empty on refresh); (c) `anthropic_auth.py` declares `token_body_extra={"state": "{state}"}` — the Anthropic-specific quirk lives entirely in the Python extension, the core stays provider-agnostic. The extras path flows into Refresh too for provider-specific knobs that need to round-trip on both grants. Regression guard: `test_state_echoed_in_token_body` in `pkg/resources/testdata/anthropic_auth_test.py` and `TestGenericAuthProvider_TokenBodyExtraStatePlaceholder` / `TestGenericAuthProvider_RefreshIncludesTokenBodyExtra` in `pkg/extension/bridge_auth_generic_test.go`.

- Agent loop now logs errors from the `GetFollowUpMessages` hook at warn level (`firlog.Warn`) instead of silently swallowing them. Previously a failing telegram/channel poll, session-store read, or host-implementation bug would cause the agent to behave as if no messages were queued, dropping the user's message on the floor with zero trace in `debug.log`. Control flow is unchanged — the hook error still returns `nil` so the loop continues best-effort, as many callers rely on. Also folded the remaining inline `config.GetFollowUpMessages()` call site at the end-of-loop error-exit branch in `pkg/agent/loop.go` through the shared `drainFollowUps` helper so the logging happens in one place. Regression coverage: `TestAgentLoop_FollowUpHookError` in `pkg/agent/loop_test.go`.

- `SimplePrompt` / `SideQuery` (the path used by the `aside` extension when escalating to a stronger advisor model) no longer silently returns an empty string when the LLM responds with only thinking or only tool-call content. Previously the implementation in `pkg/agent/agent.go` collected just `Text` blocks, so a reasoning-heavy advisor reply with no top-level text block came back as `""` with `nil` error — the outer agent saw a bare `[advisor: …]` trace line with no body and either hallucinated a "fir returned empty" explanation or improvised an answer of its own, attributing nothing to the advisor. Now the response is flattened in source order: text verbatim, thinking with a `[think] …` prefix, tool calls as `[tool <name> <compact-json-args>]` markers (useful signal that the advisor *wanted* to run a tool the caller can't execute). When every content block is unusable (the truly-empty case) `SimplePrompt` returns an explicit error instead of `("", nil)`. The `aside.py` extension also gained a belt-and-suspenders empty-synthesis check that returns `is_error: True` with `"advisor returned no content"` so any future regression at lower layers still surfaces clearly to the calling agent. Regression coverage: `TestSimplePrompt_ThinkingOnlyReturnsMarker`, `TestSimplePrompt_ToolCallOnlyReturnsMarker`, `TestSimplePrompt_MixedContent`, `TestSimplePrompt_EmptyContentReturnsError` in `pkg/agent/agent_test.go`.

- Anthropic streaming connection dying mid-tool-call no longer silently commits a wire-poison partial assistant turn to history and exits. When the SSE stream drops after a `tool_use` block opens but before `input_json_delta` finishes (e.g. `read tcp ... operation timed out`), the resulting assistant message has `stop_reason=error` plus a `tool_use` content block with empty `Arguments` — unreplayable through Anthropic's API and previously the cause of the agent appearing to "stop mid-sentence" and then gaslighting the user about what happened. The agent loop now detects this shape (`hasIncompleteToolCall`: `StopReason==error` AND any `tool_use` with empty Arguments), drops the partial from history, and transparently retries up to 3 times with backoff (250ms / 750ms / 2s), emitting a new `EventStreamRetry` lifecycle event each attempt. If all retries fail it drops the partial entirely and injects a regular user-role note ("your previous response was cut off mid-tool-call by a network/stream error … the tool did NOT execute") so the next assistant turn has accurate context. When channel follow-up messages are queued at exhaustion time, the note is **folded into the first follow-up's text** rather than appended as a standalone user turn (Anthropic accepts consecutive user messages but keeping them attached is cleaner and avoids fragmenting history). Clean pre-content errors (e.g. 429 with empty `Content`) are deliberately left untouched so the existing follow-up-after-error path keeps working. Fix lives in `pkg/agent/loop.go` (core), so both CLI and ACP modes inherit it. Regression coverage: `TestAgentLoop_DropsPartialToolCallAndRetries`, `TestAgentLoop_MidToolCallRetryExhaustedInjectsUserNote`, `TestAgentLoop_MidToolCallExhaustedWithFollowUpsFoldsNote` in `pkg/agent/loop_test.go`.

### Changed

- Skill conflict warnings on session startup are now suppressed for the routine cases (`duplicate-name`, `override-conflict`) — when the same skill name exists in both a global and a project source, the loader picks one deterministically and there's nothing for the user to do. Parse errors and other unexpected diagnostics still surface. The `/skill:<name>` slash-command completion now prefixes the description with the skill's file path so it's obvious which copy `/skill:` would expand.

## [0.46.4] - 2026-05-14

### Changed

- Anthropic server-side content blocks (`server_tool_use`, `web_search_tool_result`, `code_execution_tool_result`, `web_fetch_tool_result`, `tool_invocation`, `tool_output`) now round-trip verbatim via a new generic passthrough variant `ai.ServerContent` (carries the provider's block-type tag, the raw `content_block` JSON, and a pre-formatted display string). The Anthropic streamer captures the original bytes; `convertAnthropicMessages` writes them back on the wire unchanged; `TransformMessages` drops them when crossing providers (replacing with the display text so user intent survives) since the wire shape is Anthropic-specific. Replaces six special-case handlers in the streamer with a single generic branch — future Anthropic server-side block types round-trip automatically. The TUI / ACP transcript renders `ServerContent.Display` so output looks identical to before. The v0.46.3 band-aids (`separateAdjacentThinkingBlocks` wire-time splice, non-empty `[server tool: <name>]` placeholder) are kept as defence-in-depth for sessions whose history was stored under the older text-flattened format; they can be removed once such sessions age out. `BACKLOG.md` marks the work done.

- Anthropic server-side tool invocations (`server_tool_use`, e.g. `web_search`, `code_execution`) now render as a visible `[server tool: <name>]` text marker in the TUI / ACP transcript instead of an invisible empty placeholder. Side effect of the v0.46.3 fix — the marker exists so the placeholder survives `pruneEmptyAssistantTextBlocks` (which would otherwise drop it and leave adjacent thinking blocks on disk). Will be replaced by proper passthrough rendering once the structural fix in `BACKLOG.md` lands.
### Fixed

- Anthropic and Codex OAuth login: "Redirect URI http://localhost:NNNNN/cb is not supported by client". Regression from the short-link refactor (`3c894eff`), which migrated every provider to OS-assigned loopback port (`127.0.0.1:0`) + unified `/cb` path. Empirically verified against the live providers (2026-05-14) that Anthropic whitelists the path exactly (`/callback`) but accepts any loopback port per RFC 8252 §7.3, while Codex requires both exact port (`1455`) and exact path (`/auth/callback`). Restored: Anthropic now uses wildcard port + `/callback`, Codex pins `http://localhost:1455/auth/callback`. Other providers (gemini-cli, antigravity, poe) were already fine. Regression guards added to `pkg/resources/testdata/anthropic_auth_test.py` and `codex_auth_test.py` freezing the callback addr/path.

## [0.46.3] - 2026-05-13
### Changed

- Skill/extension override resolver: an explicit `override:` (either `override: true` or `override: <full-id>`) no longer emits a "shadowed" warning for the displaced item — intentional overrides are silent. The killer→victim relationship is still returned via `overriddenBy` for listings/doctor. Genuinely ambiguous cases (`override-conflict`, unresolved override targets, `duplicate-name` coexistence) still warn.

### Fixed

- Anthropic 400 "thinking or redacted_thinking blocks in the latest assistant message cannot be modified" on resume of sessions containing a server-side tool block (e.g. `web_search`). Root cause: fir's stream parser flattens server-side content blocks (`server_tool_use`, `web_search_tool_result`, `code_execution_tool_result`, `web_fetch_tool_result`, `tool_invocation`, `tool_output`) into plain `text` blocks during streaming, and the `server_tool_use` invocation in particular became an **empty** text placeholder that `pruneEmptyAssistantTextBlocks` stripped end-of-stream — leaving the two signed `thinking` blocks that originally sandwiched it adjacent on disk. Anthropic's input validator rejects assistant messages with two consecutive thinking blocks (a structural rule), reporting it with the misleading "blocks cannot be modified" error string and a non-actionable cumulative-position pointer (e.g. `messages.1.content.12`). Confirmed by reproducing the failing wire 1:1 against `/v1/messages` with the user's actual signed-thinking bytes via `curl`, then bisecting to the adjacency rule. Two band-aids land in this release: the `server_tool_use` placeholder is now a non-empty `[server tool: <name>]` text block so the pruner leaves it in place; and a new `separateAdjacentThinkingBlocks` pass at wire-build time splices a synthetic text separator between any thinking blocks that would otherwise be emitted adjacent, preserving every signed block byte-for-byte. The proper structural fix — generic passthrough storage for server-side blocks so they round-trip verbatim — is tracked in `BACKLOG.md`. Regression coverage in `pkg/ai/providers/anthropic_adjacent_thinking_test.go`; existing `anthropic_thinking_invariants_test.go` updated to reflect the new wire-build invariant.

- Skill loading: every builtin `SKILL.md` now carries `override: true` in its frontmatter so that when the same file is also discovered via a project skills directory (the in-repo `.fir/skills` symlink at the builtin source tree, or a user-copied skill), the project-origin copy shadows the builtin-origin copy instead of coexisting under disambiguated `builtin__`/`project__` IDs. `LoadBuiltinSkills` deliberately drops the `Override` claim on the builtin self-load so the two copies don't both claim override and trigger an `override-conflict` diagnostic.

- `fir -c` on first invocation in a project (no prior session) now correctly stamps the user's `SessionInvocation` so a subsequent `fir -c` can restore `--mcp-config` / `--model` / `--extension` / etc. Previously `createSessionStore` reported `isResumed=true` whenever `--continue` (or a `--session <path>` to a non-existent file) was passed, even when `ContinueRecentSession` had silently fallen back to creating a fresh session — so `maybeRestoreInvocation` took the "resume" branch, found no stamped invocation, and skipped stamping. New `SessionStore.WasResumed()` is the single source of truth: it returns true only when an existing header was successfully loaded from disk. Doc on `maybeRestoreInvocation` corrected to match actual stderr-notice behaviour (drift/missing warnings only).

## [0.46.2] - 2026-05-13

### Added

- Per-message wire-summary trace at `-vv` across every provider — anthropic, openai (chat completions), openai-responses, bedrock, google (Gemini), google-vertex, and declgoogle. One `firlog.Trace` line per outgoing wire message with role, block count, and per-block-type structure: thinking-block signature lengths, tool_use / function_call names, text/reasoning lengths, Gemini parts (text/functionCall/functionResponse/thought/inlineData), OpenAI tool_calls and `reasoning_content` siblings. Single generic helper (`pkg/ai/providers/trace.go`) walks the body after JSON-marshalling it (which also handles SDK types like Bedrock's `*skipstone.ConverseStreamInput`) and looks for the message array under one of the well-known keys (`messages` / `input` / `contents`). No body bytes are ever logged — bounded cost per request; safe to leave on for whole sessions while investigating prefix-reconstruction / replay bugs.

## [0.46.0] - 2026-05-13
### Added

- Discovered extensions now carry `Origin` and `ID` fields (`<sanitized-origin>__<name>`, MCP-style) on `ExtProcConfig`, plus an `Override` field parsed from comment frontmatter. Origin maps Scope to the resource-coexistence vocabulary (`global` → `user`; `project`/`builtin`/`package` identity-mapped). Behaviour is unchanged — same-named extensions still shadow by name on discovery; the new fields exist for diagnostics and to lay groundwork for future coexistence/override support. The shared `ResolveOverrides[T Overridable]` helper used by skills now lives in `pkg/resources/overrides.go` and is reusable by any future Origin/ID-aware resource type. `resources.MakeResourceID` is the generic alias of `MakeSkillID` for non-skill callers.
### Changed

- `fir -c` / `fir -r` / `/resume` now restore the resumed session's original invocation config by default — `--mcp-config`, `--extension`/`-e`, `--no-extensions`, `--provider`, `--model`, `--models`, `--thinking`, `--system-prompt`, `--append-system-prompt`, `--tools`, `--no-tools`, `--no-mcp`, `--skill`, `--no-skills`, `--theme`, `--no-themes`, and `--disable-extension` are re-applied so the resumed session has the same tools/MCPs/extensions/model as the session it claims to continue. The user-intent config is stamped once into the session header (first jsonl line, new optional `invocation` field) at session creation and never rewritten on resume, so resume-of-resume preserves the *original* invocation. Explicit flags on the resume invocation override the persisted value per-field (list-valued flags like `-e` replace, never union — passing any `-e` means you're choosing the set yourself). `--mcp-config` is stored as a path + sha256; on resume the file is re-read and a stderr warning surfaces if the contents have changed since stamp time, or if the file has gone missing. New `--no-restore-config` flag opts out and behaves like the old `-c`. Sessions created before this change carry no `invocation` field and behave as today (no restore). Restore currently applies on the startup path (`-c` and ACP/CLI starts that open an existing session); restoring config mid-process on `/resume` / `-r` interactive selection is a planned follow-up. New `pkg/session/store.SessionInvocation` + `StampInvocation` / `GetInvocation` / `LoadInvocation` / `HashFile` helpers; CLI-side `BuildInvocation` / `ApplyInvocation` / `maybeRestoreInvocation` in `cmd/fir/invocation.go`; `Args` gained a `Seen map[string]bool` populated by `ParseArgs` so per-field "user explicitly passed it" beats "user didn't pass it" cleanly.

### Fixed

- bash tool: secondary race in the pipe-drain unblock path. The unconditional `pr.Close()` after `cmd.Process.Wait()` could win the race against the drain goroutine's first `Read` under Linux + race detector, truncating output to empty (`TestBashTool_Echo` failed in CI). Now wait for drain to finish naturally with a 50ms grace period, falling back to force-close only if a held-open descendant keeps the pipe alive past our killpg.
- bash tool: race where a backgrounded subshell (`(sleep 30; ...) &`) could keep the stdout pipe open after bash exited and `killpg(SIGKILL)` returned success, blocking `Execute` for the full child duration. After reaping bash and killpg'ing the group, we now force-close the pipe's read end to unblock the drain — bash's own output is already in the kernel buffer by the time `Wait` returns and the concurrent drain goroutine has captured it. Matches the documented contract that backgrounded jobs are killed when the foreground command exits.
### Added

- Repeatable `-v` verbosity flags driving slog levels. `-v` enables Debug, `-vv` enables Trace (a new level below Debug, slog -8). `-vvv` and beyond clamp to `-vv` with a warning log line. New `FIR_LOG_LEVEL=info|debug|trace` environment variable (numeric slog levels also accepted) — wins over `FIR_DEBUG`. New `firlog.Trace` / `firlog.TraceEnabled` / `firlog.SetLevel` / `firlog.ParseLevel` API in `pkg/log` with a custom `LevelTrace` constant; the JSON handler renders the custom level as `"TRACE"`.

### Changed

- Discovered extensions now carry `Origin` and `ID` fields (`<sanitized-origin>__<name>`, MCP-style) on `ExtProcConfig`, plus an `Override` field parsed from comment frontmatter. Origin maps Scope to the resource-coexistence vocabulary (`global` → `user`; `project`/`builtin`/`package` identity-mapped). Behaviour is unchanged — same-named extensions still shadow by name on discovery; the new fields exist for diagnostics and to lay groundwork for future coexistence/override support. The shared `ResolveOverrides[T Overridable]` helper used by skills now lives in `pkg/resources/overrides.go` and is reusable by any future Origin/ID-aware resource type. `resources.MakeResourceID` is the generic alias of `MakeSkillID` for non-skill callers.
- `-V` (and `--version`) now prints the version. The old short alias `-v` for `--version` is gone — `-v` now means verbose. When `fir -v` is invoked alone (no other args, matching the legacy version invocation pattern), a migration note is printed to stderr pointing at `-V` / `--version`. `FIR_DEBUG=1` still works as a Debug-level shortcut.
- `pkg/ai/providers/cacheguard.go` prefix-invalidation logs moved from Debug to Trace. Run with `-vv` to see them.
- MCP per-event Debug logs that fire dozens of times per turn (`typing indicator sent/failed`, auto-reply chunk/event/queue-full notices) moved from Debug to Trace.

### Removed

- `FIR_CACHE_DEBUG` environment variable. Use `-vv` (or `FIR_LOG_LEVEL=trace`) instead — cache invalidation logs are now at Trace level unconditionally.

### Changed

- `self_handoff` no longer writes `.fir/handoff-<ts>.md` to the user's cwd. The briefing is now carried in-context to the new session via a new optional `prepend_context` parameter on the `restart_session` extension RPC, which injects the briefing as a `[SYS_EXT]`-wrapped user message ahead of the (now-short) restart prompt. No filesystem artifact, no `.fir/` directory created in repos that did not opt into it. The briefing persists in the new session's jsonl log like any other message. Slimmer `handoff.py` (dropped `_default_path`, `_atomic_write`, `_verify_readable`). Wire-level: `restart_session` gains an optional `prepend_context: string`; SDK signature is `ctx.restart_session(prompt, prepend_context="")`. Tool description updated; restart prompt is now `"Continue from the handoff briefing above."`.

### Fixed

- `AgentSession.PrependContext` no longer silently drops the message when the `enableSysExtensions` setting is off. The setting now governs only how already-injected `[SYS_EXT]` blocks are *interpreted* — via the static authority hook line in the system prompt — so flipping it mid-session naturally re-interprets prior injections. Injection itself is unconditional; we never silently lose extension-supplied context. Previously self_handoff would have appeared to no-op for users who disabled SYS_EXT.

## [0.45.0] - 2026-05-13
### Removed

- **Prompt templates** — the `.fir/prompts/` and `~/.config/fir/prompts/` slash-command template feature is gone. The `--prompt-template` and `--no-prompt-templates` CLI flags, the `"prompts"` settings array, the `prompts` field in `fir.json` package manifests, `ResourceLoader.GetPrompts()`, `PromptTemplate`, `LoadPromptTemplates`, `ExpandPromptTemplate`, `SetProjectPromptTemplatePaths`, `GetPromptPaths`, and the ACP `prompts` capability listing all removed. ACP/`pkg/pkg`'s `ResolvePackageResources` signature shrinks from 4 path lists to 3. `pkg/mcp/prompts.go` (MCP server prompt support, unrelated feature) is unaffected.

### Added

- **Skill name conflicts now allowed.** Same-named skills from different origins coexist by default; each loaded skill carries an `Origin` (`builtin` / `user` / `project` / `pkg:<source>` / `path:<basename>`) and a unique `ID` of the form `<sanitized-origin>__<name>` (MCP-tool-name style — every char outside `[a-z0-9_]` in the origin becomes `_`). The agent-facing `<available_skills>` block uses bare names when unique and disambiguated IDs when not, with a one-line preamble noting the convention. To deliberately replace another same-named skill, add `override: true` (replaces all) or `override: <full-id>` (replaces one) to the skill's frontmatter. Override conflicts (multiple `override: true` for the same name) are resolved by origin precedence (user > project > path > pkg > builtin) and surface a warning. New diagnostic types: `duplicate-name` (info), `shadowed` (warning), `override-conflict` (warning); old `collision` type for skills retired. `fir skills list` gains an `ID` column and `[ambiguous]` / `[overrides …]` markers; `fir skills show` and `fir /<ref>` accept either bare name or full ID, and reject ambiguous bare names with the candidate IDs. `pkg.Manager` gains `ResolvePackageContributions()` so package-sourced skills get `pkg:<source>` origins. Documented in the `self` skill. Extensions, themes, and prompts continue to use the legacy first-wins behaviour for now and will get the same treatment in a follow-up.

- `pkg/exec` extracted to [`github.com/kfet/pinexec`](https://github.com/kfet/pinexec) v0.0.4 — now a standalone, dependency-free Go module (MIT, Go 1.21+, 100% coverage gate). The in-tree `pkg/exec/` directory is gone. `exec.ExecuteBash` → `pinexec.Execute`; `exec.BashResult` → `pinexec.Result`. Call site in `pkg/session/agentsession.go` updated. `pkg/agent/tools/{ansi,truncate}.go` (`StripAnsi`, `AppendColorEnv`, `TruncateHead`, `TruncateTail`, `TruncationOptions`, `TruncationResult`, `DefaultMaxBytes`, `DefaultMaxLines`, `GrepMaxLineLength`, `FormatSize`, `TruncateLine`) moved into pinexec — they live with the bash runner because they describe how its output is shaped. A small `pkg/agent/tools/exec_reexport.go` shim re-exports them as type aliases / vars so the dozens of existing in-fir call sites that import them from `tools` keep compiling unchanged; new code should import from pinexec directly. Temp-file prefix for the >50KB output spill changed from `fir-bash-` to `pinexec-`.

- OAuth extensions can now hand fir a pre-shortened auth URL alongside the full one. `auth/open_url` (and the Python SDK `ctx.open_url`) gained an optional `short_url` parameter; `oauth.AuthInfo` gained a matching `ShortURL` field. Modes show the short URL prominently with the full URL on a fallback line (`session.FormatAuthURLs`) and open the short one in the browser (`session.PreferredAuthURL`). All five OAuth provider extensions (gemini-cli, antigravity, codex, anthropic, poe) now pre-shorten their authorize URL via `https://tinyurl.com/fir-{gem,agr,cdx,ant,poe}` — pre-created links whose stored target is the static (non per-session) portion of the URL; per-session params (`state`, `code_challenge`, `redirect_uri`) are appended client-side and merged by the shortener. Cuts the worst-case auth URL from 624 → ~200 chars. Drift between each provider's static URL and its short-link target is caught by per-provider unit tests in `pkg/resources/testdata/*_auth_test.py`. Note: TinyURL routes Google destinations through an affiliate redirect (`redirect.viglink.com`) — functional end-to-end, an extra hop for gemini/antigravity only.

### Changed

- OAuth extensions now bind their local callback server on an OS-assigned port (`127.0.0.1:0`, RFC 8252 §7.3) with a unified `/cb` callback path, instead of each provider hardcoding a fixed port (8085 / 51121 / 1455 / 1456 / 53692) and a provider-specific path. The fixed-port fallback paths in gemini-cli, antigravity, and codex (which were dead code — they pointed at a port no listener was on) are removed; on callback-server start failure these three now raise a clear error instead of silently using a broken redirect URI. Anthropic's manual-paste fallback to `https://platform.claude.com/oauth/code/callback` and poe's manual-paste fallback are preserved. `_REDIRECT_URI` constants removed where dead. Affected files: `pkg/resources/builtin_extensions/{gemini_cli,antigravity,codex,poe,anthropic}_auth.py`.

- Adapt to pinoauth v0.2.0 breaking API: `pinoauth.Credentials` removed → fir now defines its own `ai.OAuthCredentials` (same `Access/Refresh/Expires/Extra` shape) so the toolkit stays stateless and fir-domain extras (project IDs, account IDs) live where they belong; standalone `ExchangeCode` / `Refresh` functions replaced by `pinoauth.Client{...}.Exchange` / `.Refresh` (per-provider config no longer repeated on every refresh — both `genericAuthProvider` and `pkg/ai/providers/google_vertex.go` now build one client and reuse it); `Provider` interface slimmed (lost `GetAPIKey` / `ListModels`) so `ai.OAuthProvider` is no longer an extension of `pinoauth.Provider` — it's a standalone fir interface that uses `*ai.OAuthCredentials` end-to-end, with provider implementations using `pinoauth.Token` only inside the conversion boundary; `GeneratePKCE` no longer returns an error (drop the `, err :=` and nil check). 69 call sites updated across `pkg/ai`, `pkg/auth`, `pkg/extension`, `pkg/models`, `pkg/modes/acp`, `pkg/modes/interactive`, `cmd/fir/login.go`, and all test mocks. The post_exchange hook and JSON-RPC wire shape extensions consume are unchanged.
- OAuth provider extensions now declarative — fir drives the flow, extensions only carry provider-specifics. The standard authorization-code+PKCE plumbing (PKCE generation, callback server, browser opening, token exchange, token refresh) moved out of every Python extension into Go via `pinoauth`. Extensions now register a static [`OAuthFlowSpec`](docs/extension-protocol.md#oauth-flow-spec) at init (URLs, client ID, scope, callback addr, body encoding, custom headers, optional `short_url_base` for pre-shortened authorize URLs) plus a handful of optional JSON-RPC hooks for the genuinely provider-specific bits — `auth/post_exchange` (extract account ID from JWT, preserve project ID across refresh), `auth/api_key` (override the trivial `creds.access` default), `auth/list_models`, `auth/modify_models`, and `auth/refresh` (only for non-standard refresh shapes). New SDK helpers: `fir_ext.declare_oauth_provider(...)` and `@fir_ext.auth_post_exchange(provider=...)`. Eliminates 5-7 JSON-RPC round-trips per login (one per generic step) and shrinks the auth code in each extension from ~200-300 lines to ~30-80. Converted: `anthropic-auth`, `codex-auth` (JWT chatgpt_account_id extraction), `poe-auth` (non-standard `api_key` token shape, no refresh), `antigravity-auth` and `gemini-cli-auth` (project-discovery hooks preserved). `copilot-auth` keeps its imperative `auth/login` because GitHub uses device-code, not authorization-code. New Go-side code: `pkg/extension/bridge_auth_generic.go` (`genericAuthProvider`), test in `pkg/extension/bridge_auth_generic_test.go` against a fake OAuth server. Wire-level addition to `AuthProviderSpec`: optional `flow` field carrying the static config.
- `pkg/ai/oauth` extracted to [`github.com/kfet/pinoauth`](https://github.com/kfet/pinoauth) v0.2.3 — now a standalone, stdlib-only Go module. All call sites import `pinoauth` directly; the in-tree `pkg/ai/oauth/` directory is gone. Notable shape changes adopted from the new module: `Provider.Login` and `Provider.RefreshToken` take `ctx` as their first argument (the `LoginCallbacks.Ctx` field is gone); `StartOAuthCallbackServer` → `StartCallbackServer`; the "browser callback OR manual paste, whichever wins" race in `pkg/extension/bridge_auth.go` collapses to a single `pinoauth.AwaitAuthCode` call; `pkg/auth.AuthStorage.Login` grew a `ctx` first argument; Google Vertex AI's ADC access-token refresh in `pkg/ai/providers/google_vertex.go` uses `pinoauth.Refresh` instead of an inline `http.PostForm` (error reporting improves slightly via `*pinoauth.TokenError`).
- `pkg/ai/oauth` was first refactored to be self-contained (zero fir-domain imports) before the lift-out. The `Provider` interface lost its `ModifyModels(models []*ai.Model, ...)` method and the `ModelDefaulter` interface was removed; both lived only to bridge OAuth credentials back to `ai.Model` and now live on the fir-side typed wrapper. The global `RegisterProvider` / `GetProvider` / `GetProviders` registry moved to `pkg/ai/oauthreg.go` as a typed `ai.OAuthProvider` registry. The new `ai.OAuthProvider` interface carries `ModifyModels` and `ModelDefaults` directly so `live_synthesis.go` no longer needs the old type-assertion. The unused `GetOAuthAPIKey` and `GetOAuthProviderInfoList` helpers were dropped.
- `extract-module` skill: add idiomatic-Go review pass (treat the copy-paste as the starting state; rework naming/errors/interfaces/docs/options for a standalone Go module before the first commit) and insist on a 100% coverage gate in the new module's Makefile (copy the exact recipe from skipstone/firpty rather than rolling your own).

### Fixed

- Transient network/transport errors (TCP `connection reset by peer`, `broken pipe`, `connection refused`/`timed out`, DNS `no such host`, `network is unreachable`, `TLS handshake timeout`, `i/o timeout`, `unexpected EOF`, bare trailing `EOF`, HTTP/2 stream `INTERNAL_ERROR` / `GOAWAY`, `use of closed network connection`) are now classified as retryable in `pkg/ai/ratelimit.IsRetryableError` via the new `IsTransientNetworkError`. Previously such errors fell through the provider retry loops and surfaced as `StopReasonError`, ending the turn — e.g. `read tcp 192.168.x.x:port->160.79.x.x:443: read: connection reset by peer` from Anthropic. All three providers (anthropic, google, openai/openai_responses) already route through the shared classifier and pick up the fix automatically; pre-stream resets are now retried transparently with the existing backoff, mid-stream resets still surface (replay would risk duplicated output). Also added HTTP 504 Gateway Timeout to `IsTransientServerError`.

### Removed

- The Go-side `OpenAICodexProvider` (`pkg/ai/oauth/openai_codex.go`) and its now-redundant fir-side wrapper (`pkg/ai/oauthproviders/`) have been deleted. The `openai-codex` OAuth provider is registered exclusively by the `codex-auth` Python builtin extension (`pkg/resources/builtin_extensions/codex_auth.py`), matching how the other OAuth providers (anthropic, github-copilot, gemini-cli, …) are already handled. This means `fir login openai-codex` requires extensions to be enabled (the default). The OpenAI-specific `JWTClaimPath` constant moved to `pkg/ai/providers/openai_codex_responses.go` as an unexported `codexJWTClaimPath`. The generic `ParseAuthorizationInput` helper stays in pinoauth since it's reused by the extension bridge for user-pasted auth codes.

## [0.44.0] - 2026-05-10

### Changed

- README install instructions now point at `https://raw.githubusercontent.com/kfet/fir-dist/main/install.sh` (the canonical install source on the public binary distribution repo) instead of `kfet/fir/main/install.sh`.

### Added

- Bash tool now applies a 10-second default timeout when the agent does not pass an explicit `timeout` parameter. Previously a missing timeout meant "no timeout", which let runaway commands (e.g. `strings` on a large binary) hang a session indefinitely. Agents can still override up or down by passing `timeout` explicitly. `pkg/agent/tools/bash.go` (new `DefaultBashTimeout` var) + tests in `bash_test.go`.

- MCP server lifecycle: fir now surfaces `connecting…` and `disconnected` notifications in addition to the existing `connected` event, in both interactive (TUI) and ACP modes. The MCP `Manager` exposes two new callbacks — `SetOnServerConnecting(name)` (fires once at the initial dial and once at the start of each reconnect cycle, not per retry) and `SetOnServerDisconnected(name, err)` (fires when an active session terminates unexpectedly; clean `Close`/`Reload` are silent). `SetOnServerReady` now also fires on each successful reconnect (previously initial-connect only) so every `connecting…` is followed by a matching `connected`. `pkg/session/factory.go` plumbs the new callbacks through `MCPManagerOptions` / `SetupOptions.OnMCPServerConnecting` / `OnMCPServerDisconnected`. Interactive mode adds `NotifyMCPServerConnecting` / `NotifyMCPServerDisconnected`. Tests in `pkg/mcp/client_test.go`.

### Fixed

- Builtin extensions (`demo`, `hello`, etc.) no longer fail to start with `fork/exec .../fir-builtin-extensions/<hash>/<name>.py: no such file or directory` when the temp cache directory has been partially purged (e.g. by macOS periodic temp cleanup). Extraction now writes a `.complete` sentinel file last and the cache-reuse check requires the sentinel to be present — if any file is missing the cache is wiped and re-extracted. Previously the check only verified that *some* top-level file existed, so a partial purge that left `.pyc` caches but removed the actual `.py` scripts would silently reuse a broken cache. `pkg/resources/builtin_extensions.go` + test in `builtin_extensions_test.go`.

### Changed

- `pipe` builtin extension now returns only **leaf** step outputs to the LLM. A leaf is a step whose output is not referenced by any later step via `{{prev}}`, `{{step:N}}`, or `{{step:N.field}}`. Non-leaf steps still execute and feed forward into substitutions, but their outputs are replaced in the final markdown by a one-line size marker (`## Step N: tool (intermediate, X bytes — omitted)`). Single-step pipes remain transparent passthrough. Errors that abort the pipe always surface the failing step's output regardless of leaf status. This enables large data pipelines whose intermediate blobs never enter LLM context. `pkg/resources/builtin_extensions/pipe.py` + tests in `pkg/resources/testdata/pipe_test.py`.
- `wt` skill now covers discussion/design sessions, not just autonomous task delegation. Description rewritten so the skill fires whenever the user wants the work *or* the conversation about it to happen in a separate window — including "let's discuss / design / propose this over there". Added explicit do-mode vs discuss-mode distinction with discuss as the safe default on ambiguity, and an anti-pattern note: don't draft designs inline in response to a wt-shaped cue.
### Added

- `fir skills show <name>` subcommand inspects a single skill — prints metadata (name, source, description, file path, base dir). Flags: `--full`/`-f` appends the SKILL.md body, `--path` prints only the file path for piping (e.g. `bat $(fir skills show foo --path)`). Bare-name shorthand `fir skills <name>` dispatches to `show` when `<name>` isn't a known subcommand. Unknown names suggest substring matches. Implemented in `cmd/fir/skills.go`.

## [0.43.3] - 2026-05-06

### Fixed

- `TestBridge_KeepAlive_UpdatesLastActivity` and `TestBridge_KeepAlive_ExtendsDuringSlowSideQuery` in `pkg/extension/bridge_test.go` no longer flake under `-race` / loaded CI. Both relied on a fixed `time.Sleep` to wait for a background ticker goroutine to emit at least one tick, but under the race detector the goroutine can be delayed past that single sleep window. Replaced both with bounded polling (3s deadline, 10ms intervals) that checks the same invariant deterministically. Per `AGENTS.md` testing guidance: avoid wall-clock waits in tests.

### Changed
- `extract-module` skill: add idiomatic-Go review pass — when extracting
  a package into its own repo, treat the copy-paste as the starting state
  and rework naming/errors/interfaces/docs/options for a standalone Go
  module before the first commit. Behaviour stays identical; shape and
  ergonomics get a fresh-start treatment.

### Changed
- `extract-module` skill: insist on a 100% coverage gate in the new module's
  Makefile (copy the exact recipe from skipstone/firpty rather than rolling
  your own). Plugs the loophole that produced a coverage-gate-less Makefile
  during the pinoauth extraction.
### Changed

- Model picker search now matches across all rendered fields, not just `id` and `provider`. Typing `[free` (or `FREE`) filters to the FREE-tagged models; typing `128k` filters by context window; `[openai]` filters by provider badge; `SWE:70` filters by SWE-bench score. `pkg/modes/interactive/components/model_selector.go` builds a richer haystack including the model display name, the `[FREE]` / cost / context / SWE badges, and the bracketed provider badge, then passes that to `tui.FuzzyFilter`. Test in `pkg/modes/interactive/components/model_selector_test.go`.

## [0.43.2] - 2026-05-05

### Changed

- Bedrock provider migrated from `aws-sdk-go-v2` (`bedrockruntime` + `smithy-go`) to the stdlib-only [`github.com/kfet/skipstone`](https://github.com/kfet/skipstone) client (pinned at `v0.1.0`). Drops 16 transitive `aws/*` modules + `smithy-go` (~58 MB of module cache) from fir's dependency graph. Behaviour preserved: ConverseStream API, prompt caching, tool calls, image content, reasoning/thinking blocks, stop-reason mapping, all streaming events, `AWS_BEDROCK_SKIP_AUTH=1`, `BaseURL` override. Credential resolution (env, shared profile, `credential_process`, IRSA, ECS task creds, IMDSv2, STS `AssumeRole` + `source_profile` + `mfa_serial`) is now provided by `skipstone/creds`.
- Model sort logic unified: `pkg/models/SortModels` is now the single source of truth for both ACP `session/new` model lists and the interactive TUI model selector. Previously `pkg/modes/acp/modelstate.go` and `pkg/modes/interactive/components/model_selector.go` carried separate, divergent implementations. The shared function takes an optional `currentModel` to pin (TUI uses it; ACP passes nil), groups by `OrderedProviders()` (TUI now uses canonical provider order instead of alphabetical), and uses the strict `IsFreeModel` definition (Poe + all four cost axes zero) everywhere. The ACP path previously used a looser zero-input+output check that incorrectly classified subscription/OAuth-gated zero-cost providers (github-copilot, openai-codex, opencode, …) as free and promoted them above paid API models in the dropdown.

### Fixed

- MCP reconnect warnings no longer corrupt the TUI. `pkg/mcp/client.go` previously emitted "MCP reconnect failing" / "MCP re-list tools error" / Reload errors via the `slog` package's *default* logger, whose default text handler writes to `os.Stderr` — so a flaky MCP server (victoria-metrics, grafana, …) printed raw `WARN ...` lines straight over the rendered TUI. All such call sites now route through `pkg/log` (firlog), which goes to the debug-log file or a discard handler — never stderr. Defense in depth: `pkg/log` now also calls `slog.SetDefault(...)` on init and on `Init`, so any stray `slog.*` call (ours or a dependency's) lands in our handler instead of stderr. Regression test `TestManager_ReconnectWarning_DoesNotLeakToDefaultSlog` asserts no record reaches `slog.Default()` while a reconnect loop fails past the surface threshold.

- Bedrock provider no longer crashes the request when an MCP tool reports an empty, missing, or non-object JSON Schema. The Bedrock Converse API requires `toolConfig.tools[].toolSpec.inputSchema.json` to be a JSON object and rejects anything else with `ValidationException`. `convertBedrockToolConfig` now coerces nil, empty, string-encoded, or otherwise non-object schemas to `{"type":"object","properties":{}}`, and decodes `json.RawMessage`/`[]byte`/`string` payloads (the shape MCP tools arrive in via `pkg/mcp/tool_adapter.go`) into a real map. Test in `pkg/ai/providers/bedrock_test.go`.
- Skill discovery now follows symlinks inside the skills subtree. `loadSkillsFromDirInternal` previously skipped any `os.ReadDir` entry that wasn't a regular file or directory according to lstat, so a symlinked `SKILL.md` or symlinked subdirectory was silently dropped. We now `os.Stat` symlinked entries and branch on the resolved mode, with cycle protection via a `filepath.EvalSymlinks`-keyed visited set threaded through the recursion. Tests added in `pkg/resources/skills_test.go`.
- Extension discovery (`pkg/extension/discovery.go` `scanExtDir`) had the same lstat-vs-stat issue: a symlink-to-directory ext slot was treated as a file (because `e.IsDir()` is false on a symlink) and ended up registered as a broken file extension pointing at the symlink itself rather than recursing into it. Now resolves symlinks via `os.Stat` and treats them as directories when appropriate. Test added.

## [0.43.1] - 2026-05-05

### Changed

- ACP `session/new` model list ordering refined: models are now grouped by **provider first** (using the canonical `models.KnownProviderOrder` — anthropic > openai > google > …), and only within each provider group sorted by SWE-bench Verified score descending, then free-before-paid, then ID. Previously the list was a flat capability-sorted mix that interleaved providers; the Poe chat model dropdown now keeps each provider's models contiguous, which matches operator mental model when scanning the list. `models.knownProviderOrder` is exported as `models.KnownProviderOrder` so the ACP modelstate builder can reuse the same canonical order. Test `TestSortAvailableModels` updated to cover provider grouping with unknown-provider fallback.

## [0.43.0] - 2026-05-05
### Fixed

- `--model <ext-shipped-id>` flag (e.g. `--provider google-gemini-cli --model gemini-2.5-flash`) failed with `Unknown provider` in `-p` / non-interactive modes because CLI model resolution ran before extensions had registered their providers. Resolution is now deferred until after `extension.Setup` completes and `modelRegistry.Refresh()` has picked up ext-shipped `ProviderSpec` records; the resolved model is then applied via `Session.SetModel`.
- `Registry.ResetApiProviders` was wiping extension-shipped wire-protocol Api providers (e.g. `google-gemini-cli`, `google-antigravity`) when `ModelRegistry.Refresh()` re-registered built-ins after auth-extension `ModifyModels` ran. The reset is now scoped: entries owned by extensions are preserved across both Api shapes — stand-alone wire-protocol Api specs (`builtin-ext-api:*` / `ext-api:*` source ids) and synthetic Apis allocated for hosted providers without a separate Api spec (`builtin-ext:*` / `ext:*`). Their lifecycle is owned by the contributing extension via `UnregisterApiProviders(sourceID)`; built-in and unsourced dynamic entries are still cleared and rebuilt as before. Regression test: `TestRegistry_ResetPreservesExtSources`. Without this fix, every `fir -p` run with an ext-shipped provider would fail mid-stream with `no API provider registered for api: <id>`.
- Hosted-provider extensions using the synthetic-stream dispatch path (`ProviderSpec.Api == ""`, e.g. demo's `echo` provider) registered only `Stream` on the synthetic Api's `ApiProvider` entry; `StreamSimple` was nil. The agent loop calls `StreamSimple` for every turn, so any third-party hosted-provider extension would have crashed with a nil-pointer panic on first message. The bridge now wires both — `StreamSimple` downgrades to `Stream` by passing the embedded `StreamOptions` through. Test coverage: `TestCLI_DemoEchoProvider_E2E` exercises the contract end-to-end against the demo extension's echo provider.

### Added

- `ApiSpec` wire-protocol contract — extensions can now ship the *wire-protocol Api* itself (endpoints, headers, request envelope, etc.) as data, not just hosted-provider metadata. Each `ApiSpec` carries a `kind` discriminator dispatched to an `apikind.Handler` registered in core (currently `decl-google` for the Cloud-Code-Assist Gemini family). Python SDK gains `DeclGoogleApi` / `DeclGoogleConditional` dataclasses and `register_api()`. The new `pkg/extension/apikind` subpackage is a tiny shared seam between `pkg/extension` and `pkg/ai/providers` to keep layering one-way and avoid an import cycle. See `docs/extension-protocol.md` § Wire-protocol Api specs.

- Full migration of `google-gemini-cli` and `google-antigravity` providers to extensions. The `gemini-cli-auth` and `antigravity-auth` builtin extensions now ship the entire provider — wire-protocol Api spec (`DeclGoogleApi`), `RegisteredProvider` record, and model catalogue (7 + 12 entries) — so `pkg/ai/providers/` carries no provider-specific literals for those services. `register_gemini_cli.go` and `register_antigravity.go` are deleted; `register_builtins.go` no longer registers them; the `ApiGoogleGeminiCLI`, `ProviderGoogleGeminiCLI`, `ProviderGoogleAntigravity` constants and 19 model entries are removed from `pkg/ai/types.go`, `cmd/generate-models/main.go`, and `pkg/ai/models_generated.go`. Net: zero "gemini-cli" / "antigravity" code paths in non-extension Go.

- `ProviderSpec.api` field — extension-shipped providers can opt into streaming-dispatch passthrough by setting `api` to a built-in wire protocol id (e.g. `"openai-completions"`, `"anthropic-messages"`). When set, fir reuses its in-process stream function for that wire protocol and the extension ships only metadata (display name, priority, env keys, OAuth wiring, models). Empty `api` keeps today's behaviour: fir allocates a synthetic `ext:<id>` Api and routes streams to a `@provider_stream` handler in the extension. Documented in `docs/extension-protocol.md` § Hosted Providers / Streaming dispatch modes.

- Extensions can register hosted AI providers via `InitResult.providers`. Python SDK adds `Provider`/`Model`/`EnvKeys` dataclasses, `register_provider()`, and `@provider_stream` / `@provider_list_models` / `@provider_resolve_custom_id` decorators (plus `is_cancelled()` for cooperative cancellation). fir registers a synthetic `ext:<id>` Api in its provider registry and proxies `provider/stream/start`, `provider/stream/cancel`, and `provider/listModels` over JSON-RPC; streamed events flow back as `provider.stream.event` notifications. Built-in `demo` extension now ships an `echo` provider as a worked example. See `docs/extension-protocol.md` § Hosted Providers.

### Changed

- Extension init handshake timeout default raised from **5s → 30s** to accommodate very slow hardware (e.g. Raspberry Pi Zero W, where Python interpreter startup alone can take many seconds and previously caused every extension to fail handshake). Still overridable via `FIR_EXT_TIMEOUT` (seconds). Updated `pkg/extension/capability.go`, `pkg/envvars/envvars.go`, `docs/extension-protocol.md`, and the `fir_ext` SDK module docstring.
- Strong typing across the extension wire boundary. `pkg/extension/types.go` now declares one Go struct per JSON-RPC method param/result and per event payload (e.g. `OkResult`, `SideQueryResult`, `GetSessionDataResult`, `MessageEndPayload`, `ToolExecutionEndPayload`, `SessionStartPayload`); the bridge marshals typed values instead of ad-hoc `map[string]any` literals. The Python SDK (`fir_ext`) re-exports a `TypedDict` for every wire shape (`ToolResult`, `ExecResult`, `MessageEndParams`, `ToolCallHookParams`, `ToolCallHookResult`, `CommandHookResult`, `SessionStartParams`, etc.) via `__all__`, so handlers can be annotated for IDE/type-checker support. TypedDicts are plain `dict` at runtime — existing extensions keep working unchanged; wire JSON is byte-for-byte identical. `demo.py` now uses the typed annotations as a reference example.

### Removed

### Fixed

- ACP `session/new` model list (used by Poe relay's chat-options dropdown, among others) now arrives in a stable, capability-ordered sequence instead of whatever order Go's map iteration happened to return. `BuildModelState` in `pkg/modes/acp/modelstate.go` runs `sortAvailableModels` over `ModelRegistry.GetAvailable()` before serialising, sorting by SWE-bench Verified score descending, then free-before-paid (zero input+output cost), then provider name, then model ID. Mirrors the priority order of the interactive TUI model picker so the same most-capable / cheapest models surface first wherever the list is shown. Test `TestSortAvailableModels` covers SWE-desc, free-vs-paid for same score, and ID tiebreaker stability.

- Model stats/metadata corrected in `cmd/generate-models/main.go` (`applyOverridesAndAdditions` and related tables): `claude-opus-4-7` now inherits the latest known Opus SWE-bench Verified score (80.9%, from Opus 4.5) instead of falling through to the base `claude-opus-4` pattern (67.6%); Poe-hosted `glm-5` and `glm-5.1-fw` context windows corrected from 131072 to 202752 (Z.ai's documented ~200K window, matches OpenRouter/Vercel entries); Poe-hosted `kimi-k2.5` and `kimi-k2.5-fw` context windows corrected from 128000 to 262144 (Moonshot's documented 256K window for Kimi K2.5 / K2 Thinking — Poe's own `max_output_tokens` parameter even allows 262144, confirming the lower number was a metadata bug). Implemented as entries in `poeContextOverrides` and a new `claude-opus-4-7` entry near the top of `sweModelPatterns`.
- `send_session` tool schema rejected by Gemini/Gemma providers because `deliver_as` enum contained an empty string (`["", "steer", "followUp"]`), which Google's API forbids. Renamed the default value to `"prompt"` (enum is now `["prompt", "steer", "followUp"]`); the handler maps `"prompt"` back to `""` on the wire so behaviour is unchanged.
- Anthropic Messages API 400 `messages.N.content.M: thinking or redacted_thinking blocks in the latest assistant message cannot be modified` (request id `req_011CaisANCxQkzQfGsdKwEcH`) on multi-turn extended-thinking turns. Anthropic's stream sometimes opens a `text` content block that never receives a delta — typically in interleaved-thinking turns that go straight from `thinking` to `tool_use`. Storing that empty text block put the codebase between two contradictory validators: keeping it on replay triggered the earlier 400 "messages: text content blocks must be non-empty" (`req_011CaiKVdgvopStQzBuvt3kq`); dropping it later in `convertAnthropicMessages` mutated a sibling of a signed thinking block and triggered the new 400. Fixed at the source: streaming aggregators now prune empty/whitespace-only text blocks from `output.Content` before emitting `EventDone`, so the stored assistant message and every subsequent replay are byte-stable and pass both validators. The shared helper `pruneEmptyAssistantTextBlocks` (in `pkg/ai/providers/anthropic.go`) is invoked by all four streaming aggregators — Anthropic, Bedrock, OpenAI completions, OpenAI Responses — closing the same class of bug across providers. The OpenAI completions, OpenAI Responses, and Bedrock paths were also leaking: whitespace-only `delta.Content` deltas in OpenAI completions accumulate into a whitespace-only text block; the OpenAI Responses streamer eagerly creates a `NewTextContent("")` on every `output_item.added` for type=message and leaves it empty when no `output_text.delta` arrives; Bedrock creates a text block on the first `text_delta` regardless of value, so whitespace-only deltas accumulate the same way. Regression tests cover: signed thinking + empty text sibling (`TestAnthropicStream_PrunesEmptyTextBlocksBesideThinking`), interleaved-thinking with multiple empty texts (`TestAnthropicStream_InterleavedThinking_AllEmptyTextsPruned`), redacted thinking + empty text (`TestAnthropicStream_RedactedThinking_EmptyTextPruned`), pruner idempotency (`TestPruneEmptyAssistantTextBlocks_Idempotent`), cross-provider resume with stored empty text plus gemini-thinking-downgrade preservation (`TestAnthropic_ConvertMessages_DropsStoredEmptyTextFromOtherProvider`), Bedrock whitespace-only deltas (`TestBedrock_PostStreamPruneRemovesWhitespaceText`), OpenAI Responses empty message item (`TestStreamOpenAIResponses_EmptyMessageItem`), OpenAI completions whitespace-only deltas (`TestStreamOpenAICompletions_WhitespaceOnlyTextPruned`), and compaction never summarises the latest signed-thinking turn or fragments an assistant message on split-turn cuts (`pkg/session/compaction/thinking_invariant_test.go`).

- `/schedule` extension no longer leaves a misleading "Scheduled: will continue in …" notice in the transcript with no follow-up when the timer fires. The initial response is now reworded as a clear future-tense announcement that explicitly states the notice will not change after firing, and the countdown thread emits a `[schedule_fired]` custom message in the transcript immediately before sending the user message — so it's unambiguous from the scrollback that the schedule actually ran.

### Added

- `/handoff` slash command on the `handoff` builtin extension. Lets the user explicitly trigger a self-handoff on demand (in addition to the agent invoking the `self_handoff` tool autonomously when context fills up). The command injects a user-role message instructing the agent to author a curated briefing and call `self_handoff`; any extra args after `/handoff` are passed through as focus hints.

- `pipe` builtin extension exposing one tool: `pipe(steps, label?)`. Chains multiple tool calls in a single agent turn with no intermediate LLM round-trip. Steps run sequentially; string params can reference earlier outputs via `{{prev}}`, `{{step:N}}` (0-indexed), or `{{step:N.field}}` for JSON field access. Aborts on the first error unless that step has `continue_on_error: true`. Single-step calls return the raw result (transparent passthrough); multi-step calls return a markdown block with one section per step.

### Changed

- AI providers: declarative Gemini adapter. `google_gemini_cli.go` (1012 LOC) is replaced by a generic, config-driven adapter (`pkg/ai/providers/declgoogle.go`) parameterised by `DeclGoogleConfig`. The `google-gemini-cli` and `google-antigravity` providers initially became data-only `DeclGoogleConfig` records and have since moved into their respective builtin extensions (see Added above). New `pkg/ai/providers/declcfg/` subpackage carries the substitution grammar (`${var.path}`, `${env.NAME}`, `${fn.rand_id(prefix)}`, `${fn.unix_millis()}`, `$$` escape, `"$inner"` JSON sentinel) used by the JSON-encoded ApiSpec payloads.

- Antigravity wire-protocol Api ("google-antigravity") is now actively used: model entries route through it (`api="google-antigravity"`) instead of through the gemini-cli Api as before. This activates the antigravity-specific config (system-instruction prefix, conditional `anthropic-beta: interleaved-thinking-2025-05-14` header on Claude+thinking) which was previously registered but never selected at runtime. Behaviour change: antigravity requests on Claude+reasoning models now correctly include the interleaved-thinking beta header and the antigravity system-prompt prefix; previous releases sent gemini-cli's plainer envelope.

- Cold-start failure mode for `google-gemini-cli` and `google-antigravity` providers extends to provider metadata + model catalogue (in addition to OAuth, which already had this property): if the `gemini-cli-auth` or `antigravity-auth` builtin extension fails to handshake, the corresponding provider/models won't appear in `--list-models`, the model picker, or env-key lookups until the extension is fixed. Recoverable: fir restarts the extension with exponential backoff up to 5 attempts.

- Strong typing across the extension wire boundary. `pkg/extension/types.go` now declares one Go struct per JSON-RPC method param/result and per event payload (e.g. `OkResult`, `SideQueryResult`, `GetSessionDataResult`, `MessageEndPayload`, `ToolExecutionEndPayload`, `SessionStartPayload`); the bridge marshals typed values instead of ad-hoc `map[string]any` literals. The Python SDK (`fir_ext`) re-exports a `TypedDict` for every wire shape (`ToolResult`, `ExecResult`, `MessageEndParams`, `ToolCallHookParams`, `ToolCallHookResult`, `CommandHookResult`, `SessionStartParams`, etc.) via `__all__`, so handlers can be annotated for IDE/type-checker support. TypedDicts are plain `dict` at runtime — existing extensions keep working unchanged; wire JSON is byte-for-byte identical. `demo.py` now uses the typed annotations as a reference example.

- Extension init handshake timeout default raised from **5s → 30s** to accommodate very slow hardware (e.g. Raspberry Pi Zero W, where Python interpreter startup alone can take many seconds and previously caused every extension to fail handshake). Still overridable via `FIR_EXT_TIMEOUT` (seconds). Updated `pkg/extension/capability.go`, `pkg/envvars/envvars.go`, `docs/extension-protocol.md`, and the `fir_ext` SDK module docstring.

### Fixed

- `--model <ext-shipped-id>` flag (e.g. `--provider google-gemini-cli --model gemini-2.5-flash`) failed with `Unknown provider` in `-p` / non-interactive modes because CLI model resolution ran before extensions had registered their providers. Resolution is now deferred until after `extension.Setup` completes and `modelRegistry.Refresh()` has picked up ext-shipped `ProviderSpec` records; the resolved model is then applied via `Session.SetModel`.

- `Registry.ResetApiProviders` was wiping extension-shipped wire-protocol Api providers (e.g. `google-gemini-cli`, `google-antigravity`) when `ModelRegistry.Refresh()` re-registered built-ins after auth-extension `ModifyModels` ran. The reset is now scoped: entries owned by extensions are preserved across both Api shapes — stand-alone wire-protocol Api specs (`builtin-ext-api:*` / `ext-api:*` source ids) and synthetic Apis allocated for hosted providers without a separate Api spec (`builtin-ext:*` / `ext:*`). Their lifecycle is owned by the contributing extension via `UnregisterApiProviders(sourceID)`; built-in and unsourced dynamic entries are still cleared and rebuilt as before. Regression test: `TestRegistry_ResetPreservesExtSources`. Without this fix, every `fir -p` run with an ext-shipped provider would fail mid-stream with `no API provider registered for api: <id>`.

- Hosted-provider extensions using the synthetic-stream dispatch path (`ProviderSpec.Api == ""`, e.g. demo's `echo` provider) registered only `Stream` on the synthetic Api's `ApiProvider` entry; `StreamSimple` was nil. The agent loop calls `StreamSimple` for every turn, so any third-party hosted-provider extension would have crashed with a nil-pointer panic on first message. The bridge now wires both — `StreamSimple` downgrades to `Stream` by passing the embedded `StreamOptions` through. Test coverage: `TestCLI_DemoEchoProvider_E2E` exercises the contract end-to-end against the demo extension's echo provider.

- `send_session` tool schema rejected by Gemini/Gemma providers because `deliver_as` enum contained an empty string (`["", "steer", "followUp"]`), which Google's API forbids. Renamed the default value to `"prompt"` (enum is now `["prompt", "steer", "followUp"]`); the handler maps `"prompt"` back to `""` on the wire so behaviour is unchanged.

- Anthropic Messages API 400 `messages.N.content.M: thinking or redacted_thinking blocks in the latest assistant message cannot be modified` (request id `req_011CaisANCxQkzQfGsdKwEcH`) on multi-turn extended-thinking turns. Anthropic's stream sometimes opens a `text` content block that never receives a delta — typically in interleaved-thinking turns that go straight from `thinking` to `tool_use`. Storing that empty text block put the codebase between two contradictory validators: keeping it on replay triggered the earlier 400 "messages: text content blocks must be non-empty" (`req_011CaiKVdgvopStQzBuvt3kq`); dropping it later in `convertAnthropicMessages` mutated a sibling of a signed thinking block and triggered the new 400. Fixed at the source: streaming aggregators now prune empty/whitespace-only text blocks from `output.Content` before emitting `EventDone`, so the stored assistant message and every subsequent replay are byte-stable and pass both validators. The shared helper `pruneEmptyAssistantTextBlocks` (in `pkg/ai/providers/anthropic.go`) is invoked by all four streaming aggregators — Anthropic, Bedrock, OpenAI completions, OpenAI Responses — closing the same class of bug across providers. The OpenAI completions, OpenAI Responses, and Bedrock paths were also leaking: whitespace-only `delta.Content` deltas in OpenAI completions accumulate into a whitespace-only text block; the OpenAI Responses streamer eagerly creates a `NewTextContent("")` on every `output_item.added` for type=message and leaves it empty when no `output_text.delta` arrives; Bedrock creates a text block on the first `text_delta` regardless of value, so whitespace-only deltas accumulate the same way. Regression tests cover: signed thinking + empty text sibling (`TestAnthropicStream_PrunesEmptyTextBlocksBesideThinking`), interleaved-thinking with multiple empty texts (`TestAnthropicStream_InterleavedThinking_AllEmptyTextsPruned`), redacted thinking + empty text (`TestAnthropicStream_RedactedThinking_EmptyTextPruned`), pruner idempotency (`TestPruneEmptyAssistantTextBlocks_Idempotent`), cross-provider resume with stored empty text plus gemini-thinking-downgrade preservation (`TestAnthropic_ConvertMessages_DropsStoredEmptyTextFromOtherProvider`), Bedrock whitespace-only deltas (`TestBedrock_PostStreamPruneRemovesWhitespaceText`), OpenAI Responses empty message item (`TestStreamOpenAIResponses_EmptyMessageItem`), OpenAI completions whitespace-only deltas (`TestStreamOpenAICompletions_WhitespaceOnlyTextPruned`), and compaction never summarises the latest signed-thinking turn or fragments an assistant message on split-turn cuts (`pkg/session/compaction/thinking_invariant_test.go`).

- `/schedule` extension no longer leaves a misleading "Scheduled: will continue in …" notice in the transcript with no follow-up when the timer fires. The initial response is now reworded as a clear future-tense announcement that explicitly states the notice will not change after firing, and the countdown thread emits a `[schedule_fired]` custom message in the transcript immediately before sending the user message — so it's unambiguous from the scrollback that the schedule actually ran.

## [0.42.0] - 2026-05-04

### Added

- `handoff` builtin extension exposing one strictly-typed tool: `self_handoff(content)`. Atomically validates the doc (≥200 chars after strip, ≥3 non-blank lines, ≤64 KB), writes it to `<cwd>/.fir/handoff-<timestamp>.md`, verifies the result is readable and non-empty, then triggers a session restart whose first user message points the new agent at the doc. Replaces the old `tmux send-keys "/new ..."` skill mechanism — typed JSON-RPC, no shell, no tmux dependency, hard validation before any restart fires. Validation failures return a regular tool error and the session continues. Bridge plumbing: new `restart_session` extension RPC plus `BridgeAPI.RestartSession`. `SessionBridge` calls `Agent.Abort()` synchronously to short-circuit the calling tool's result writeback, then runs `WaitForIdle` + UI clear + `NewSessionCmd` + `Prompt` on a goroutine via a mode-supplied callback (parallel to `SetStatusFn`). `InteractiveMode.SetExtensionSetup` registers the callback. Modes without a registered restart callback (ACP, headless) return a clear JSON-RPC error.

- MCP tool output handling: defensive size cap and TUI rendering. `pkg/mcp/tool_adapter.go` `convertResult` now applies `tools.TruncateTail` (DefaultMaxLines=2000 / DefaultMaxBytes=50KB) to `TextContent` and `EmbeddedResource` text returned by MCP servers. When truncation occurs the full original is spilled to a `fir-mcp-*.txt` temp file (single file per CallTool result, reused across content blocks), the truncated text gets a footer pointing the agent at the path (`[Output truncated (… lines, …). Full output written to <path> — use Read to view.]`), and the path is stashed under `Details["fullOutputPath"]` for the TUI. Image/audio/blob content unchanged. Mirrors bash's truncation behaviour so a misbehaving server can no longer blow up the LLM context. Test: `TestConvertResult_TruncatesOversizedText` (and `…_NoTruncationForSmallText`). On the TUI side, `tool_execution.go` `formatGeneric` now (a) detects `mcp__<server>__<tool>` tool names and renders a clean `mcp <server> · <tool> k="v" …` header with a compact scalar-arg summary instead of dumping the full args JSON, (b) pretty-prints JSON-shaped result text, and (c) applies the same 10-line preview cap + "ctrl+o to expand" footer used by `formatWithHint` — fixing a latent bug where any generic tool without a `DisplayHint` dumped the full body verbatim.

### Changed

- Strong typing across the extension wire boundary. `pkg/extension/types.go` now declares one Go struct per JSON-RPC method param/result and per event payload (e.g. `OkResult`, `SideQueryResult`, `GetSessionDataResult`, `MessageEndPayload`, `ToolExecutionEndPayload`, `SessionStartPayload`); the bridge marshals typed values instead of ad-hoc `map[string]any` literals. The Python SDK (`fir_ext`) re-exports a `TypedDict` for every wire shape (`ToolResult`, `ExecResult`, `MessageEndParams`, `ToolCallHookParams`, `ToolCallHookResult`, `CommandHookResult`, `SessionStartParams`, etc.) via `__all__`, so handlers can be annotated for IDE/type-checker support. TypedDicts are plain `dict` at runtime — existing extensions keep working unchanged; wire JSON is byte-for-byte identical. `demo.py` now uses the typed annotations as a reference example.

### Removed

- Builtin `self-handoff` skill — superseded by the `handoff` extension's `self_handoff` tool. The tool description carries the operational content; the doc body is left to the agent (no template).
- `fir observe --full` flag. Full untruncated formatted output (no message body or command-args truncation) is now the default and only behaviour. The previous truncating default was useless for agent consumers and only marginally helpful for humans, who can scroll. The `/observe` slash command also no longer accepts `--full`. `--json` is still available for raw JSONL output.

### Fixed

- Anthropic Messages API 400 "messages: text content blocks must be non-empty" on resumed sessions (e.g. opus-4.7 multi-turn after several "resume"/"continue" prompts; observed request id `req_011CaiKVdgvopStQzBuvt3kq`). `convertAnthropicMessages` previously preserved empty text blocks verbatim when the assistant turn carried a signed thinking block, on the theory that dropping a sibling would trigger a "thinking blocks cannot be modified" 400. Production evidence shows Anthropic's empty-text validation runs first and rejects the request. Empty/whitespace-only text blocks are now always dropped; signed/redacted thinking blocks are still replayed verbatim. Regression test `TestAnthropic_ConvertMessages_DropsEmptyTextBesideThinking` plus updated invariants matrix in `pkg/ai/providers/anthropic_thinking_invariants_test.go` (10-case block-sequence matrix, multi-turn property test, SSE-stream→replay roundtrip for `thinking` and `redacted_thinking`, tool_use input byte-stability test). The agent's partial-message `hasContent` check (`pkg/agent/agent.go`) also now counts redacted/signed thinking blocks even when their text is empty, so a redacted-only assistant turn is no longer silently dropped.
- Startup race after upgrade where the first LLM call could fail with `authentication failed for "anthropic". Credentials may have expired. Run /login anthropic to re-authenticate` even though the OAuth token was valid. Auth-provider extensions (`anthropic_auth`, etc.) register their `oauth.Provider` asynchronously during extension setup; on a fresh upgrade the embedded SDK is re-extracted and the handshake is slow enough that the agent's first turn could fire before registration. `pkg/session.AgentSession.Prompt` and `InjectMessage` now wait on the existing `ExtReady` channel before starting a turn — a single uniform "no turn before extensions are loaded" rule across all entry points. Wired through `AgentSessionOptions.ExtReady` from `CreateAgentSession`. Removes the previous ad-hoc waits at the initial-prompt path (`pkg/modes/interactive/mode.go`) and the MCP channel injection path (`pkg/session/factory.go`), which only covered specific call sites; both now rely on the central gate.
- ACP `session/set_config_option(thinking_level)` now clamps unsupported levels down the canonical ladder (max→xhigh→high→medium→low→minimal→off) to the highest level the current model supports, instead of erroring out. Matches the behaviour of `--thinking` on the CLI. The shared `agent.ClampThinkingLevel` and `agent.AvailableThinkingLevelsForModel` helpers in `pkg/agent/clamp.go` are now used by both `cmd/fir/app.go` and `pkg/modes/acp/config.go`; `pkg/session.AgentSession.GetAvailableThinkingLevels` delegates to them too.

## [0.41.0] - 2026-05-03

### Changed

- Built-in skills `fix`, `review`, `monitor`, and `e2e` collapsed into a single `project-ops` catalog skill. The catalog has a narrow trigger description and points to per-role sub-docs under `project-ops/docs/{fix,review,monitor,e2e}.md` (loaded on demand via `Read`). Cuts system-prompt surface for four mutually-exclusive, similarly-triggered loop agents. `skill-creator` now documents the catalog pattern.

- `fir observe` (no args) now lists only **live** sessions (status `running` or `idle`) by default. Ended and crashed sessions are hidden, with a one-line hint pointing at `--all`. Use `fir observe --all` to include them. The `/observe` slash command and the `observe_session` AI tool gain the same default behaviour (the tool takes a new `all` boolean parameter). `fir htop` is unchanged — it still shows every sidecar with status counters since that is its job. Rationale: a "crashed" sidecar means the fir process is gone and so are its socket and transcript writer, so the session is not observable in real time; only its post-mortem transcript can be tailed by id, which still works via `fir observe <id>`.

### Added

- `stop_session` AI tool in the `observe` extension — terminate a sibling fir session by id_prefix or cwd. Sends SIGTERM to the target's host process by default (graceful: fir flushes the transcript and runs session_end handlers); `force=true` sends SIGKILL. Resolves the target the same way as `send_session`. Sidecar gains a `host_pid` field (the fir process — distinct from `pid`, which is the observe extension subprocess); the list view (`fir observe`, `/observe`, `observe_session`) shows it as a `PID` column.

- `wt` skill promoted to a built-in skill — ships with fir by default (added `builtin: true` to its frontmatter and to the builtin-skills test expectation).

- Sync of upstream pi-mono v0.70.2 → v0.72.1.
  - New provider identifiers and env-key mappings: `moonshotai`, `moonshotai-cn` (`MOONSHOT_API_KEY`), `cloudflare-workers-ai`, `cloudflare-ai-gateway` (`CLOUDFLARE_API_KEY`), `xiaomi` (`XIAOMI_API_KEY`). Default model IDs for each added to the model resolver. Generator now ships 62 catalog entries for the five providers (cloudflare-workers-ai 8, cloudflare-ai-gateway 35, xiaomi 5, moonshotai 7, moonshotai-cn 7).
  - `AssistantMessage.ResponseModel` field — set when a provider chunk's `model` differs from the requested model (e.g. OpenRouter `auto` → concrete provider id). Wired through `pkg/ai/providers/openai.go`.
  - `AgentLoopConfig.ShouldStopAfterTurn` callback — request a graceful stop after the current turn fully completes, before steering/follow-up polling and before the next LLM call. Includes new `ShouldStopAfterTurnContext` struct.
  - `Transport "websocket-cached"` constant + Codex `previous_response_id` cached-WebSocket continuation in `pkg/ai/providers/codex_websocket.go`. Cached connections track `(lastBodyJSONNoInput, lastInput, lastResponseId)`. Follow-up requests are rewritten to send only the new input items + `previous_response_id`: the helper finds the prefix matching the prior input, skips any contiguous run of assistant-output items (`message[role=assistant]`, `reasoning`, `function_call`) since the server replays them, and sends the rest. New helpers `computeWSContinuationDelta`, `isAssistantOutputItem`, `responseInputsEqual`, `requestBodyJSONWithoutInput`. Test added.
  - Cloudflare AI Gateway client wiring: `pkg/ai/providers/cloudflare.go` resolves `{CLOUDFLARE_ACCOUNT_ID}`/`{CLOUDFLARE_GATEWAY_ID}` placeholders and rewrites Authorization → `cf-aig-authorization`. Integrated into `anthropic.go`, `openai.go`, `openai_responses.go`.
  - Xiaomi MiMo-style length-stop overflow detection in `pkg/ai/overflow` (stopReason "length" + output 0 + input filling the context window).
  - `gpt-5.5` recognised as an xhigh-thinking model.
  - Mistral `mistral-medium-3.5` model added to the catalog override list.
  - Azure OpenAI Responses URL normalisation: `normalizeAzureBaseURL` auto-appends `/openai/v1` for bare `*.openai.azure.com` / `*.cognitiveservices.azure.com` hosts.
  - DeepSeek-style `prompt_cache_hit_tokens` fallback for cache-hit token counting in `pkg/ai/providers/openai.go`.
  - Anthropic truncated-stream guard (`pkg/ai/providers/anthropic.go`): tracks `message_start`/`message_stop`; if the SSE channel closes after `message_start` without a matching `message_stop`, the stream is reported as an error instead of a successful (truncated) response.

### Changed

- Default agent transport is now `auto` instead of `sse` (matches upstream pi-mono v0.71.1).
- Bedrock model detection (Anthropic Claude, adaptive thinking, prompt caching) now also matches against `model.Name`, supporting AWS application inference profiles whose ARNs don't contain the model name.
- OpenAI Codex Responses default `text.verbosity` is now `"low"` (was `"medium"`).
- OpenAI Completions compat detection now recognises Moonshot AI (`moonshotai`/`moonshotai-cn`/`api.moonshot.*`) and Cloudflare Workers AI / AI Gateway: Moonshot and Cloudflare AI Gateway disable strict mode and `reasoning_effort` and use `max_tokens`; Cloudflare disables long cache retention.
- `acquireWebSocket` now returns the cached entry alongside the connection so callers can read/write per-connection continuation state.

## [0.40.0] - 2026-05-02

### Removed

- `full_text` parameter on the `observe_session` AI tool. The tool now always returns the full untruncated transcript snapshot — the previous default truncated long messages, which was useless for an agent consuming the output. The CLI `fir observe --full` flag is unchanged. Schema, handler, and tests updated.

- `quietStartup` setting. The flag was dead — fir no longer prints a startup banner, so nothing was reading the value. Removed the `Settings.QuietStartup` field and merge, the `/settings` "Quiet startup" entry / callback / case, the `SettingsConfig.QuietStartup` field, the test getter, and the mention in the `self` skill doc.

- `editorPaddingX` setting and the matching `/settings` "Editor padding" item. The setting was dead-wired: the production getter, settings-selector callback, and `Editor.SetPaddingX` were never connected, so changing it had no effect. Removed the field from `Settings`, the selector entry/callback, the `Editor.PaddingX` option / internal field / `GetPaddingX` / `SetPaddingX`, the render-time padding logic, and the mention in the `self` skill doc.
### Added

- MCP auto-reconnect with on-demand kick. Each MCP server now has a per-entry reconnect loop that transparently re-establishes the session when it drops (server-side timeout, dropped TCP, transient network failure). Tool calls arriving during the disconnect window block (bounded by ctx) on the loop's `ready` channel and proceed once a fresh session is installed; `CallTool` also sends a non-blocking kick so an on-demand call short-circuits any backoff sleep. Backoff is exponential (1s → 60s cap, ±20% jitter); errors surface in `Status()` only after 3 consecutive dial failures so transient blips don't churn the UI. `onToolsChanged` re-fires after every successful reconnect, fulfilling the contract that the aggregate tool list stays accurate. Reconnect goroutines are tracked by a WaitGroup so `Close()` returns cleanly with no leaks. `AdaptTool` now resolves the active session per-call via a `SessionGetter` so individual tool calls survive a transparent reconnect mid-turn instead of failing on a stale captured session. Covered by unit tests (in-memory transport, deterministic) and a wire-level integration test using `httptest.NewServer` + `sdk.StreamableHTTPHandler` that force-closes the server-side session and asserts auto-recovery over real HTTP/SSE.

### Changed

- The `pty` subcommand and PTY multiplexer are now provided by the standalone [`firpty`](https://github.com/kfet/firpty) module (extracted from `pkg/ptydriver/`). `fir` re-exposes `fir pty …` as a thin shim that imports `github.com/kfet/firpty`, so all existing skills and shell helpers keep working unchanged. The new module has 100% unit-test coverage with thin wrappers over `creack/pty` and `os/exec` excluded via `.covignore`.

### Fixed

- Update notification on dev builds: `fir 0.39.0-dev+sha` builds no longer get told "fir v0.39.0 available" because a dev build off the v0.39.0 tag is by construction *ahead* of that release. `update.IsNewer` now treats a `-dev` prerelease on the running binary as already past its core version, so the notice only fires when a strictly higher core release lands. Other prereleases (`-rc`, `-beta`) keep their standard semver semantics.

- MCP streamable HTTP transport: a clean session shutdown no longer surfaces as a user-visible disconnect error. The transport sends a `DELETE /mcp` to terminate the session in `Close()`; some servers (e.g. grafana MCP) close the TCP connection without sending an HTTP response, so the DELETE returns `Delete "URL": EOF`. `pkg/mcp` now treats EOF / `io.ErrUnexpectedEOF` / `context.Canceled` errors with `Op="Delete"` as benign in the post-startup disconnect goroutine — Status() shows `disconnected` instead of `error: disconnected: Delete "...": EOF`. Real mid-session failures (Op `Get`/`Post` or non-url errors) still surface unchanged.

- Interactive TUI: tool-result background colour stayed in the "pending" tone when `Ctrl+O` (toggle tool-output expansion) was active. `onToolExecEnd` was passing `m.toolOutputExpanded` as the `isPartial` argument to `UpdateResult`, conflating expansion state with completion state. Pass `false` (the tool has finished) and rely on the existing `SetExpanded` call in `onToolExecStart` for expansion. No verbosity change — output truncation thresholds are unchanged.

## [0.39.0] - 2026-05-01

### Added

- Slash-skill CLI invocation: `fir /<skill-name> <task...>` rewrites into an initial agent message that points at the named skill's `SKILL.md` and supplies the rest of the positional arguments as the task body. Composes with all other flags (`-p`, `--model`, etc.). Skill names are resolved via the usual loader (project + user + builtin); typos fail fast with the available list. Bash and zsh completion are extended so `fir /<TAB>` enumerates installed skills dynamically (cached per shell invocation, sourced from `fir skills list`).
- New `wt` builtin skill: delegate a task to a fresh fir agent in a new tmux window on its own git worktree. Replaces the old `.fir/prompts/wt.md` slash-prompt with a discoverable, descriptioned skill that the agent picks up automatically when the user asks to "kick off" / "spin up" / "delegate" a task.
- New `caveman` builtin skill: ultra-compressed caveman-speech mode that cuts token usage ~75% while keeping technical accuracy. Extracted from `AGENTS.md` so it's only loaded when the user asks for it (e.g. "caveman mode") instead of bloating every session's system prompt.
- Compaction rework Phase 2 (see `docs/review-compact-flow/COMPACTION_REWORK.md`):
  - **#4** `SerializeConversation` now stubs old/large tool results in **summarizer input** as `[entry <id> tool=<name> bytes=<n> head="..." tail="..."]` — pointer-stub references back to the session log. Live LLM context is unchanged so prompt-cache prefixes remain valid. Default threshold: 4 KiB; 128 head + 128 tail. Entry IDs are plumbed through `CompactionPreparation.EntryIDsToSummarize` / `TurnPrefixEntryIDs`.
  - **#8** File-op extraction now tracks `bash` tool redirects (`>`, `>>`) and `tee` targets, plus `multi_edit` / `MultiEdit` as edit ops. `/dev/null`, `/dev/std{out,err}`, and fd-dup tokens (`>&2`) are skipped. (`sed -i` extraction is documented as TODO — needs a real tokeniser.)
  - **#11** Summary format gains a `## Working Set` section: per-file one-line status (role, recent change, pending work). Carries forward across compactions via the update prompt.
  - **#12** New deterministic **`## Facts (verbatim)`** block appended to every summary: bash command lines and error/exit-code/build-failure lines extracted verbatim from the summarised history (and split-turn prefix). Capped at 20 each, deduped, in original order. Survives any LLM paraphrasing.
- Compaction rework Phase 1 (see `docs/review-compact-flow/COMPACTION_REWORK.md`):
  - **#3** File-op tracking now records the source session-store entry ID for each path; `<modified-files>` and `<read-files>` render `path (entry <id>)` so summaries become navigable back-references rather than orphaned filenames. Carried-forward paths from prior compactions render without IDs.
  - **#5** Summarization prompts now explain that older/large tool outputs may appear as `[entry <id> tool=<name> ...]` stubs (Phase 2 lands the actual stubbing) — the model is told to treat them as references and to recover content by re-running the command or re-`read`ing the file rather than guessing. The same hint is also attached to the post-compaction synthetic message (in `CompactionSummarySuffix`) so the *continuing* agent — not just the summarizer — knows how to interpret `(entry <id>)` file refs and `[entry <id> ...]` stubs in the summary it just received.
  - **#6** `[Assistant thinking]` blocks are no longer serialized into summarizer input — they bloated the prompt, biased the summary toward the agent's prior CoT, and leaked reasoning across compactions.
  - **#7** Auto-compaction triggers at **70%** fill ratio (was 90% AND-with-reserve). Models degrade well before the stated window is full; trigger early.
  - **#9** Update prompt now bounds the "Done" list to the most recent ~20 items, rolling older items into a single "Earlier (summarized)" bullet so multi-compaction sessions don't grow unbounded changelogs.
  - **#10** `/compact <instructions>` is now promoted to a `<user-focus>` section above the format spec (was a trailing footer the model under-weighted).
  - **#13** TUI re-renders during compaction streaming are throttled to ~20 Hz; per-token deltas no longer swamp the render loop.
- Compaction rework Phase 0 (see `docs/review-compact-flow/COMPACTION_REWORK.md`): moved `pkg/compaction` under `pkg/session/compaction` (compaction is a session operation), split the 850-line `compaction.go` into `compaction.go` / `cutpoint.go` / `prompts.go` / `tokens.go`. Introduced a neutral `session.Artifact` type (with `EntryID`, `Kind`, `Message`, `ToolName`, `Bytes`) plus `(*AgentSession).CompactionArtifacts()` and `(*AgentSession).ApplyCompaction()` so the compaction runner no longer reaches into `SessionStore.AppendCompaction` / `Agent.ReplaceMessages` directly. Removed the unused `compaction.SessionEntry` placeholder. No behaviour change.
- `# explicit: true` extension frontmatter flag: extensions marked explicit are **discovered** (so `fir -e <name>` and listings find them) but **not auto-loaded** — they only run when the user names them in the allowlist. Replaces the dropped `# demo: true` flag with a semantically clean opt-in marker. The shipped example/fixture extensions `demo.py` and `hello.py` are now annotated with it: they're loaded by the builtin extension loader so `fir -e demo` works, but stay dormant by default.
- Versioned Homebrew formulas: each release publishes `fir@MAJOR.MINOR` to the `kfet/homebrew-fir` tap alongside the rolling `fir`, letting users pin or roll back with `brew install kfet/fir/fir@0.29`. A `tap-prune` workflow keeps the 10 most-recent minor channels and removes older ones automatically after each release. Pinned and rolling formulas both ship a `bin/fir`; switching between them is `brew unlink` / `brew link --overwrite` (the requested clobber UX).
- New `pebble-emu` builtin skill: AI-friendly recipe for the Rebble/Core-Devices `pebble-tool` v5 emulator. Documents the build → install → screenshot loop (`pebble screenshot --no-open` pulls a live PNG over the pebble-protocol socket — the agent's eye), the `--vnc` flag (QEMU `-vnc :1` on port 5901 + websockify noVNC on 6080), input/sensor injection (`emu-tap`, `emu-accel`, `emu-battery`, `emu-bt-connection`, `emu-compass`, `emu-app-config`, `emu-control`), all six platforms (aplite/basalt/chalk/diorite/emery/flint), and cleanup (`pebble kill`, `pebble wipe`).
- Extensions can now register top-level CLI verbs via `cli_verbs:` in their comment frontmatter. `fir <verb> [args...]` spawns the extension cold (no session, no Manager), performs the standard init handshake, and dispatches via a `cli_invoke` JSON-RPC request. The extension drives fir's real TTY through `host.println()` / `host.eprintln()` / `host.readline()`; Ctrl-C/Ctrl-\ are forwarded as `cli_signal` notifications. Reserved built-in subcommands cannot be shadowed; verb collisions between two extensions surface as a startup error. Python SDK adds `@fir_ext.cli_verb(name)` and `@fir_ext.on_cli_signal`. See `docs/design/extension-cli-verbs.md`.
- `/observe` and `/send` slash commands plus `observe_session` and `send_session` AI tools, all served by the builtin `observe` extension. The slash commands and tools take **snapshots** of another session's transcript / inject single messages — they do not live-tail (use `fir observe` from another terminal for that). Lets one fir agent inspect or steer a sibling session as part of its turn.
- `observe.py` now meters live session activity in the sidecar: provider/model, token usage (input/output/cache_read/cache_write/total), USD cost (per-category + total), and per-event activity counters (turns, messages, assistant_messages, tool_calls, tool_errors, last_event timestamp). Sidecar schema bumped to 2; older clients see the original keys plus the new ones additively. Hooks into the existing `message_end` extension event, which now carries `{role, provider?, model?, stop_reason?, response_id?, usage?}` for assistant messages — see `docs/extension-protocol.md`.
- New `fir htop` CLI verb (served by the builtin `observe` extension): a top/htop-style live monitor for fir sessions. Reuses the same sidecar discovery as `fir observe` and surfaces the new metering as MODEL · TOK (total tokens) · $ (cost) · TOOLS (calls/errors) columns alongside ID/NAME/CWD/STATUS/AGE/ACT. Auto-refreshes every `--interval <dur>` (default 1s; min 100ms). Input is line-based per the cli-verbs protocol — `q<Enter>` or Ctrl-C to quit, `<Enter>` to force a refresh. Falls back to a one-shot list when stdout is not a TTY.

### Changed

- `AGENTS.md` reorganised: worktree convention (previously in the demoted `work` skill) and advisor-escalation guidance are now first-class sections, and the standing Python-3.9 / "you own this codebase" rules are tightened. Caveman mode moved out into its own skill.
- `shepherd` skill no longer references the removed `work` skill — it points at the worktree convention in `AGENTS.md` instead.
- `notify` builtin extension now uses the tmux session id (`$TMUX`) as the OSC 99 notification identifier when running inside tmux. Kitty coalesces same-id notifications, so multiple background fir agents in one tmux session collapse into a single updating banner instead of stacking. Outside tmux, falls back to the previous fixed id. OSC 777 terminals (Ghostty, iTerm2, WezTerm) are unaffected — that protocol has no id field.
- `fir observe` and `fir send` are now CLI verbs of the builtin `observe` extension instead of Go subcommands. Behaviour is identical (sidecar discovery, formatter, `--json` / `--full` / `--interact`, sigil parsing, `--cwd`, NDJSON wire to the per-session socket); the implementation moved from `cmd/fir/observe.go` + `cmd/fir/send.go` (~880 LoC of Go) into `pkg/resources/builtin_extensions/observe.py`. First real users of the extension-CLI-verbs mechanism.

### Removed

- `work` builtin skill and `.fir/prompts/wt.md` slash-prompt — content folded into `AGENTS.md` (worktree convention) and the new `wt` skill (delegated-agent recipe) respectively.
- `# demo: true` extension frontmatter flag and its `Demo` gating in the manager. The flag's only effective use was skipping demo extensions in real sessions, but the only files marked with it (`demo.py`, `hello.py`) are also missing `# builtin: true` (the real load gate) and were already not loaded at runtime — so the gate never fired in practice. Both files remain as test fixtures in `pkg/resources/builtin_extensions/` for direct-path loading by tests.

### Fixed

- `fir htop` no longer leaves the shell cursor hidden after exit. Empirical capture (tmux 3.6a) showed tmux composites alt-screen toggles internally and never forwards `?1049l` from the verb's exit to the outer terminal — its own follow-up redraw frame ends in `?25l` from a stale alt-screen state, which sticks. The fix sidesteps the entire DECTCEM dance: the verb no longer hides the cursor (`?25l`) on entry, so there is nothing to restore on exit. Cursor blinks briefly between frames during the 1s redraw — acceptable for a batch-mode monitor; permanently-hidden cursor afterwards is not. SIGQUIT (Ctrl-\) during `htop` now also runs the cleanup path instead of `os._exit(0)`-ing past the `finally` block. Supersedes the earlier 0.38.0 attempt that re-emitted `?25h` after `?1049l` — that fix worked on plain ghostty/iTerm but not under tmux, which composites alt-screen toggles internally and dedupes the trailing `?25h`.
- `observe` builtin extension daemon no longer pegs a CPU on long-running sessions. `_accept_loop` now does a plain blocking `accept()` and relies on `on_session_shutdown` closing the listening socket (which raises `OSError` and exits the loop) — the previous 0.5s `settimeout` poll was inherited by accepted sockets on POSIX, so `_handle_conn`'s `for line in f` over `conn.makefile()` could surface `socket.timeout` mid-read instead of blocking. The transcript tail loop in `fir observe` also yields 10ms when `readline()` returns a partial line (no trailing `\n`), preventing a writer that appends bytes without newlines from busy-spinning.

## [0.38.0] - 2026-04-29

### Added

- ACP `authenticate` RPC now supports a non-blocking interactive OAuth flow via `_meta.auth.interactive`. Two-call protocol: call 1 starts the login and returns the auth URL plus an opaque pending-id; call 2 submits the pasted redirect URL to complete. Reuses the existing `oauth.LoginCallbacks` plumbing (same flow as the TUI's `fir login`). Lets ACP clients without an attached browser (e.g. `poe-acp-relay`) drive OAuth by surfacing the URL to the end user and feeding the redirect back. Legacy clients that don't set the meta flag use the existing blocking `OpenBrowser` path unchanged. Multiple concurrent logins per provider are supported via the per-flow id.
- New `tmux-observer` builtin skill: instructions for attaching a tmux window to an already-running fir session and driving it via `tmux send-keys`. Pure markdown — three tmux primitives (`new-window`, `capture-pane`, `send-keys`) plus `fir observe <id> --interact --full`. No script, no orchestration logic — the parent agent decides layout and window strategy.

### Fixed

- Poe bots that route to Google Gemini/Gemma upstream (e.g. `poe/gemma-4-31b`) no longer fail with a 400 `Unknown name "additional_properties"` error. Tool parameter schemas now have `additionalProperties` stripped recursively when targeting Poe — Google's API rejects the field even though OpenAI accepts it.
- `fir --model poe/<unknown-bot>` now fails fast with a clear "Model not found for provider \"poe\". Use --list-models …" error instead of falling back to a custom model id and producing an opaque upstream 500. Poe's full bot catalogue lives in our model list, so a missing id is always a typo.
- `bash` tool no longer hangs when a command backgrounds a process that inherits the stdout pipe (e.g. `(sleep 30; echo done) &`). After the foreground bash process exits we now `killpg(-pgid, SIGKILL)` to reap any orphaned background children still holding the pipe, so the tool returns immediately. Well-behaved daemons that `setsid` (tmux server, sshd, etc.) escape the process group and are unaffected.
- `fir observe --interact` now sends each non-empty line on the first Enter, matching `fir send` and every other line-oriented CLI tool. Previously it accumulated lines and only flushed on a blank line (second Enter), which broke `tmux send-keys "msg" Enter` driving and confused human users. Sigils (`!` steer, `+` followUp, `\` escape) are still parsed via the shared `sendMsg`.

### Changed

- Demoted `review` from built-in skills. Still available via `fir skills install review` or by checking out the source.
- `/skills` listing now classifies skills as `builtin`, `user`, `project`, `package`, or `path` based on file location, instead of labelling everything from `SkillPaths` as `path`. Makes it obvious whether a skill came from `~/.fir/skills`, `<cwd>/.fir/skills`, an installed package, or an explicit `--skill` path.

## [0.37.0] - 2026-04-29

### Changed

- Built-in skill set re-evaluated. Promoted to built-in: `self-handoff`, `rebase-on-main`, `merge-to-main`, `poe-usage`. Demoted (no longer built-in): `work`, `fix`, `loop`, `monitor` — these were too opinionated or niche to ship by default. Merged `skill-updater` into `skill-creator` (the review checklist now lives in the creator skill; updater is removed).

### Added

- Bedrock ARN model support: when `CLAUDE_CODE_USE_BEDROCK=1` and `ANTHROPIC_MODEL` is set, fir routes through `amazon-bedrock` and passes `ANTHROPIC_MODEL` to Bedrock as the model id. Accepts both regular Bedrock model ids and full ARNs (e.g. `arn:aws:bedrock:us-east-1:123:application-inference-profile/abc`). ARN-form ids are passed through to `ConverseStream` verbatim — no "model not found" warning. Explicit `--model`/`--provider` CLI flags still take precedence.
- Shell completion for bash and zsh, shipped in `cmd/fir/completions/` and embedded into the binary. New subcommand `fir completion <bash|zsh>` prints the script. Static completion covers every flag and subcommand; dynamic completion shells out to the binary itself (`fir --list-models`, `fir extensions list`, `fir skills list`, `fir sessions list`, `fir packages list`) so providers, models, extensions, skills, sessions, and installed packages all complete live. Homebrew (via `generate_completions_from_executable` in the goreleaser brews block) and `install.sh` (per-user `~/.local/share/{bash-completion,zsh}/`) drop the files automatically; `make install` runs the new `install-completions` target. Build-time test `cmd/fir/completion_test.go` parses `args.go` and `subcommands.go` for every flag/short-flag/subcommand and fails CI if any are missing from either completion script — and additionally runs `bash -n` / `zsh -fn` over the embedded scripts so a typo in the static files fails the build, not the user's shell.
- `--list-models` and `--list-available-models` now display each model's context window size (e.g. `128k`, `1M`) and color-code entries by cost tier: cyan for free/unknown, green for cheap (input < $0.50/M tokens), yellow for expensive (input > $3/M tokens), default for mid-range.
- Interactive `/models` selector popup: new `[ctx]` column shows each model's context window (`128k`, `1M`, `200k`, ...) aligned across rows; model IDs are now color-coded by input-cost tier (green for cheap < $0.50/M, yellow for expensive > $3/M, default for mid-range) — previously only free Poe bots were highlighted. The selected row is now highlighted with a full-width background bar across all columns, not just the arrow + ID.
- New `fir observe` CLI command (`cmd/fir/observe.go`): `fir observe` (no args) lists live sessions across all running fir processes, reading sidecars from `$XDG_STATE_HOME/fir/agents/`, with kill-0 liveness check (dead pid + status running ⇒ crashed), sorted newest-first. `fir observe <id-prefix>` tails-and-formats the matching session's JSONL transcript (prefix-match against session id / name / cwd-basename, Git-style ambiguity error, 100ms stat-poll tail loop, exits cleanly when sidecar status flips to ended). `--json` outputs raw JSONL with no truncation (best for agent consumers). `--full` outputs formatted prose with no truncation (agent-readable). Default formatted output truncates message content (200 runes) and command args (60 runes) for human readability. `--cwd <path>` / `--cwd .` resolves session by working directory. `--interact` starts the `fir send` input loop concurrently with the tail for single-pane use. ANSI colour on TTY, `NO_COLOR` respected. Test coverage: 33 unit tests in `cmd/fir/observe_test.go` (sidecar reader, resolver, formatter, age formatting, rune-aware truncation) + 11 e2e tests in `tests/e2e/observe_test.go`.
- New `fir send` CLI command (`cmd/fir/send.go`): interactive stdin → session input socket. **Enter submits each non-empty line as a message** (matches every other line-oriented CLI tool — no blank-line accumulator). First-line sigils set `deliver_as` per-message: `!message` → steer (interrupt current turn), `+message` → followUp (queue for after current turn), `\!` / `\+` to escape. `--steer` / `--follow` flags set the default `deliver_as` for all messages. `--cwd .` resolves by directory. Greeting on TTY connect. SIGQUIT (Ctrl-\\) for clean detach. Also works as a stdin filter (`echo "msg" | fir send <id>`). Test coverage: 13 unit tests in `cmd/fir/send_test.go` (all sigil combinations, default override, conflict between `--steer` + `--follow`, empty-content skip, arg parsing) + 5 e2e tests covering the full socket round-trip.
- `observe` builtin extension (`pkg/resources/builtin_extensions/observe.py`): per-session sidecar writer + Unix-socket listener that powers `fir observe` and `fir send`. On `session_start` writes a JSON sidecar at `$XDG_STATE_HOME/fir/agents/<session-id>.json` carrying `{schema:1, session_id, pid, socket_path, store_path, cwd, started_at, status, session_name}` for discovery (atomic write via `tmp` + `rename`; persists past `session_shutdown` for post-mortem reads with `status=ended`). Binds a Unix socket at `<runtime-dir>/fir/observe/<session-id-prefix>.sock` (mode 0600; runtime-dir resolves `FIR_OBSERVE_DIR → XDG_RUNTIME_DIR → TMPDIR → ~/.fir-tmp`; 16-char session-id prefix to fit macOS's 104-byte sun_path cap) accepting NDJSON `{deliver_as, content}` lines that get forwarded as `send_user_message` calls. Defensive `_is_safe_session_id()` rejects path-traversal characters in session ids. Status transitions tracked: `running` (default) → `idle` (agent_end) → `running` (agent_start) → `ended` (session_shutdown). 17 unit tests in `pkg/resources/testdata/observe_test.py`.
- Subcommand registry (`cmd/fir/subcommands.go`): single source of truth for both subcommand dispatch (in `app.go`) and `--help` rendering (in `args.go`). Adding a new top-level `fir <verb>` is a single struct literal — no more diverging dispatch and help text. Existing subcommands (update, skills, extensions, install, uninstall, packages, pty, sessions, observe, send, login, completion) all moved to the registry; `pty` is dispatch-only (omitted from help as an internal helper). The completion build-time test scans `subcommands.go` for the `Name:` fields and fails CI if any subcommand is missing from the bash/zsh completion scripts.
- Extension protocol: three new `EXTENSION → FIR` bridge methods. `get_session_file` returns the absolute path to the session's JSONL transcript on disk (the foundation of `fir observe`: the observation extension announces this path so external readers can `tail -F` directly without further IPC into fir). `get_session_name` returns the session's display name. `get_session_id` returns the unique session id (also delivered in `session_start` event params under the `session_id` key — the bridge method allows retrieval at any point during the session lifetime). All three return empty string for unset / in-memory / non-persisted values. Wired through `pkg/extension/{api.go,bridge.go,session_bridge.go,auth_setup.go,manager.go}`, exposed via the Python SDK as `Context.get_session_file()` / `Context.get_session_name()` / `Context.get_session_id()`, documented in `docs/extension-protocol.md` (event reference + bridge method reference) and the `fir_ext.py` docstring table, exercised by the canonical `demo.py` (with matching `demo_ext_test.py` assertions).
- Design docs: session observability (`docs/design/observe.md`) — `fir observe <session-id>` tails the per-session JSONL transcript across all fir modes (interactive/ACP/print), with the observation extension providing discovery sidecars and input sockets. PTY sub-process attach (`docs/design/pty-attach.md`) — lower-priority `fir pty attach <target>` design with bounded scrollback for ptydriver-managed sessions. Extension CLI verbs stub (`docs/design/extension-cli-verbs.md`) — future-anchor for letting extensions register top-level `fir <verb>` commands via frontmatter; deferred until ≥3 unrelated motivating cases.

### Fixed

- `/reload` now reconnects MCP servers that have disconnected or errored even when their config is unchanged. Previously `Manager.Reload` only restarted servers whose config differed, so a dead-but-still-configured MCP server (e.g. a local HTTP server that went away on `connect: connection refused`) could not be recovered without restarting fir. Reload now also retries any unchanged-config server whose session is nil or whose last error is set, and clears `e.err` on successful (re)connect. Regression test in `pkg/mcp/reload_test.go::TestManager_Reload_ReconnectsDisconnected`.
- `fir completion <bash|zsh>` no longer drops into interactive mode. The dedicated dispatch branch in `cmd/fir/app.go::run()` was removed when subcommands were migrated to the `subcommands.go` registry, but `dispatchSubcommand` was never wired in — so `completion` (and any other registry-only entry) fell through to TUI startup. `run()` now calls `dispatchSubcommand(os.Args[1])` before normal arg parsing, collapsing the eleven hand-written `if os.Args[1] == "..."` branches into a single registry lookup. Matches the "single source of truth" intent the registry was introduced for.
- `/advise` command now prints the advisor response to the scrollable conversation area instead of the transient status bar, so long multi-line answers are fully visible and persistent. Added `print_response` field to `CommandResult`; when true, interactive mode routes the message to `showMessage` (conversation area) rather than `showStatus` (status bar).
- Anthropic provider no longer drops or downgrades thinking blocks when replaying conversation history, which caused `400 thinking or redacted_thinking blocks in the latest assistant message cannot be modified` errors on multi-turn sessions with extended thinking enabled. Two root causes fixed:
  1. `convertAnthropicMessages`: a thinking block with a non-empty `ThinkingSignature` but empty `Thinking` text was silently dropped by the empty-text guard. Reordered checks so any block with a valid signature is always forwarded verbatim.
  2. `TransformMessages`: when model IDs didn't match exactly (e.g. alias `claude-opus-4-7` vs dated `claude-opus-4-7-20250514`), thinking blocks with signatures were converted to plain text, stripping the signature. A new `isSameProvider` check (same `Provider` + `Api`) now preserves all signed thinking blocks — including `redacted_thinking` — verbatim across model-ID variations within the same provider.
- `pkg/session/store.SetSessionFile` now holds the write mutex for its entire duration. Concurrent `GetSessionFile()` reads (e.g. from the extension bridge while the user runs `/resume`) no longer race against the file-switch path. Race detector caught this on `TestResumeSession_DuplicateIDCleansUpOldSession` once the bridge gained `GetSessionFile()`.

### Changed

- `pkg/session/store`: session JSONL file is now created with its header line at session creation, and every `Append*` call appends to disk immediately (was gated on first assistant message; first persist was a non-atomic truncate-rewrite). The new behaviour is the contract required by `fir observe`: observers can `tail -F` the transcript from byte 0 without missing the first turn. The `rewriteFile` path remains for compaction-driven full rewrites. Two regression tests in `pkg/session/store/session_test.go` pin the contract.

### Removed

- Removed the "Available tools" section from `--help` output and from the default system prompt; tool definitions are already supplied via the API, so listing them in prose was redundant. Also removed `ToolSnippets` from `BuildSystemPromptOptions`.

## [0.36.0] - 2026-04-27

### Added

- New `/advise` slash command in the `aside` extension: routes a side question directly to the configured advisor model (same resolution as `escalate=true` on the `aside` tool). When no advisor is configured, prints a hint to `/aside-advisor` instead of silently falling back — the whole point of the command is to ask a stronger model. Output is prefixed `[advisor: provider/model[:effort]]` for traceability, mirroring the tool path.

### Fixed

- Anthropic provider no longer emits an empty `anthropic-beta` header when no beta features are needed (or when the OAuth `x-anthropic-oauth-beta-prefix` is empty). The API rejects empty values with `400 Unexpected value(s) `` for the `anthropic-beta` header`, which surfaced as `aside LLM call failed` for users on accounts where no betas applied to the chosen model. New helper `joinBetaParts` strips empty entries; the header is omitted entirely when the result is empty. Regression tests in `pkg/ai/providers/anthropic_test.go`.

## [0.35.0] - 2026-04-27

### Fixed

- `pkg/mcp`: eliminate root cause of `ToolListChangedHandler` race — if a server sends `notifications/tools/list_changed` during the `initialize` handshake (before `Connect()` returns), the re-list goroutine now detects that `e.session` is still nil and exits early instead of calling `session.Tools()` on a mid-initialization session. The `startServer` call already does a full tool enumeration after `Connect()` returns, so the notification is redundant and safe to skip.

### Added

- New builtin skill `aside-advisor`: teaches the executor the advisor/escalation pattern via a `[SYS_EXT]` description in the base system prompt. Unlike the old `session_start` prepend (a user-role history message that drifts during compaction), the skill's description lives in `<available_skills>` inside the system prompt and is present on every turn. The skill body adds detailed guidance on when to escalate, timing, and how to formulate a good advisor query. Removed the `on_session_start` handler from `aside.py` — the skill now owns this steering. Body and description rewritten to incorporate Anthropic advisor-tool best practices: call before substantive work, before declaring done (after making deliverable durable), when stuck or changing approach; treat advice with weight and surface conflicts in a reconcile call; ask advisor for <100-word enumerated responses. Description now keyword-dense (deliberating · stuck · uncertain · change of approach · declare done · second opinion) so the executor pattern-matches on the actual trigger states.

## [0.34.0] - 2026-04-26

### Fixed

- `pkg/mcp.TestManager_LoggingHandler` flaked on CI: the slog-capture channel sometimes received an unrelated `"MCP re-list tools error"` record (from a startup-race re-list against a server that hadn't yet completed its initialize handshake) before the expected `"MCP server log"` warning, and the test's blocking `<-ch` consumed the wrong record. Test now drains the channel until it sees the expected message (with the same 3 s overall deadline). Reproduced under CI load on the v0.34.0 release build.

### Added

- `aside` extension now subscribes to `session_start` and prepends a `[SYS_EXT]` note teaching the LLM *why* the advisor model exists when one is configured. Principle-based, not a checklist — the note frames the advisor as a second opinion to reach for when the LLM's own reasoning (not its tools) is the bottleneck and the cost of being wrong outweighs the cost of asking. Co-located with the tool that owns the behaviour and only fires when the advisor is enabled, so users who run `/aside-advisor off` see zero prompt bloat.
- Extension init handshake now includes `config_dirs` — a priority-ordered list of directories (project-local `.fir` first, global `~/.config/fir` last) for per-extension config storage. The Python SDK exposes `fir_ext.config_dirs`, `fir_ext.load_config()` (first-found JSON read), and `fir_ext.config_path()` (highest-priority write target). `aside.py`, `doctor.py`, and `provider_usage.py` migrated away from hardcoded `~/.config/fir` paths. `aside.py` default advisor model now guarded by unit test `DefaultAdvisorTracksHighestAnthropicOpus` in `pkg/resources/testdata/aside_test.py`, which ensures `_DEFAULT_ADVISOR_SPEC` stays in sync with the highest Anthropic Opus in the model registry.
- Upstream sync v0.67.68 → v0.70.2: DeepSeek V4 provider (`deepseek-v4-flash`, `deepseek-v4-pro`) and Fireworks provider with API key support; gpt-5.5 codex model; updated default models (anthropic→claude-opus-4-7, openai-codex→gpt-5.5, google→gemini-3.1-pro-preview); `AnthropicMessagesCompat` for conditional fine-grained-tool-streaming beta and long cache retention; agent tool `Terminate` flag for early loop exit; `sanitizeForOpenAPI` strips JSON Schema meta-keys from Gemini tool parameters; `downgradeUnsupportedImages` centralises image-to-placeholder conversion for non-vision models; synthetic tool results now inserted at end of conversation; `PI_OAUTH_CALLBACK_HOST` env var for Codex OAuth; `TimeoutMs`/`MaxRetries` fields on `StreamOptions`.

### Changed

- Extensions now always start eagerly in parallel — lazy/deferred startup is gone.  Removed `pendingExtension` machinery from `pkg/extension/manager.go` (`startPendingForEvent`, the `m.pending` slice, lazy command stub registration, lazy session_start dispatch).  Removed the entire `frontmatter_check.go` pipeline — `FrontmatterMismatch`, `CheckFrontmatter`, `FixFrontmatter`, `FormatFrontmatterWarning` and the `Manager.OfferFixFn` hook — because there is no longer any frontmatter declaration to validate against.  `events:`, `commands:`, and `tools:` keys are no longer parsed and have been stripped from every builtin extension; the actual capability set is reported by the extension during the init handshake and that's the single source of truth.  Measured impact: ~30ms additional startup wall-clock (on M-series, ~9 builtin extensions, parallel handshakes — well inside session-start noise) in exchange for ~hundreds of lines deleted across `manager.go`, `frontmatter_check.go`, `setup.go`, `auth_setup.go`, `discovery.go`, `pkg/resources/builtin_extensions.go` and the matching tests.  Eager startup also makes the cost predictable: it no longer ambushes the user mid-session when a lazy extension is finally triggered.  Slash-command autocomplete, session-start event dispatch, and command discovery all keep working out of the box because every extension is now running by the time the user can submit a message.
- Python SDK (`fir_ext.py`) dropped all keyword-only (`*,`) separators from public Context methods (`side_query`, `send_message`, `send_user_message`). Optional arguments are now ordinary positional-with-default parameters, and aside.py/extensions pass them as named arguments rather than splatting `**kwargs`. Stronger typing, more deterministic at test time, and easier to mock — assertions can match the exact call shape without `assert_called_with(**kwargs_dict)` dances.
- `plan-nudger` extension: kept the firing rules from main (idle-turn threshold, wall-clock backstop, stagnation tracking — those proved sound) but replaced every imperative with calm collaborator-to-collaborator framing. The previous design — "your next reply MUST contain a tool call" + escalation tiers + tool-hint prose + `[SYS_EXT]` prepend at the critical tier — produced two failure modes: (1) the agent gamed the literal check by marking incomplete items completed or splitting them off into a "future work" plan, and (2) the procedural voice triggered either brittle compliance or rationalisation-driven evasion. The new steer body opens with `[plan-status — keeping plan visible to the user]` (framing *why* before any counters), then a single counter line that grows with whichever signals are non-trivial (incomplete steps · idle turns · plan-updates since `progress_metric` changed · consecutive pauses without plan progress). Two optional blocks appear only when relevant: a `progress_metric` tip when the AI hasn't set one (gentle, not nagging — only on a steer that's already firing for another reason), and the `self-handoff` reassurance line once stagnation is real. No `MUST`, no `[CONTINUE]`, no `[SYS_EXT]` prepend, no escalation tiers — same shape every fire so it stays legible. The plan tool description in `pkg/agent/tools/plan.go` was updated to teach the AI about `progress_metric`. Test file `pkg/resources/testdata/plan_nudger_test.py` rewritten — covers firing rules (never on tool-using turns, fires after idle threshold, wall-clock backstop, agent_end always fires when in flight), body composition (every optional block conditionally present), and a hard "must not contain" assertion that pins the psychology fix so a future copy-edit can't quietly re-introduce the parental voice.

### Fixed

- ACP `ResumeSession` data race exposed by faster eager extension startup: the goroutine spawned by `createSession` to call `EmitSessionStart` reads session state via `GetSessionName`, while the main thread proceeds to `SwitchSession` on the same `SessionStore`. After lazy startup was removed the goroutine ran fast enough to overlap with the `SwitchSession` write, triggering `-race` detection in `TestResumeSession_DuplicateIDCleansUpOldSession`. Fixed by waiting on `entry.extReady` before `SwitchSession` (mirrors what `Prompt` already does).
- Reverted accidental regression: `/reload` no longer resets session date for active sessions; date remains stable across session lifetime unless session recreated, preventing prompt-cache invalidation after midnight.
- Anthropic prompt-cache thrash. Several sources of non-deterministic prefix serialisation between turns were busting the prompt cache and inflating Claude plan token usage. Fixed five issues: (1) `LoadSkills` returned skills in random map-iteration order, so the system prompt's `<available_skills>` block reordered on every process start / `Reload`, invalidating the system+tools cache slot. Now sorted alphabetically (with a second sort after appended builtins). (2) `convertAnthropicTools` emitted tools in `ToolSet` insertion order, which depends on extension and MCP-server startup races; now sorted alphabetically by name at the provider boundary so the `tools` block is byte-stable across turns regardless of registration order. (3) `AgentSession.Reload()` reset `sessionDate` to today's date and rebuilt the system prompt — any `/reload` after midnight permanently invalidated the cache for the rest of the session. `sessionDate` is no longer touched in `Reload()`; it still refreshes on `NewSessionCmd`/`SwitchSession` (where a fresh prompt is expected). (4) The OAuth Claude Code identity prefix system block was carrying its own `cache_control` breakpoint — wasted, since the next system block's breakpoint already covers it as a strict prefix. Dropped, freeing one of the four available breakpoints for messages. (5) `cacheguard.PrefixGuard` only logged invalidations at Debug level. Now promoted to Warn when `FIR_CACHE_DEBUG=1` so the thrashing is observable in the wild without recompiling. Tests added: `TestLoadSkills_StableOrder`, `TestAnthropic_ConvertTools_StableOrder`, `TestAnthropic_BuildParams_OAuthPrefixNoCacheControl`.
- Provider HTTP calls (streaming SSE + non-streaming `DoJSONRequest`) used `http.Client`s with no custom `Transport`, so they inherited Go's default `TLSHandshakeTimeout` of 10s. On slow networks / corporate proxies this surfaced as `net/http: TLS handshake timeout` against `api.anthropic.com` and other providers. Switched both clients to a shared `*http.Transport` cloned from `http.DefaultTransport` (preserving proxy/env settings) and tuned for long-lived sessions: `TLSHandshakeTimeout` 60s (up from Go's 10s default; an interim 30s wasn't always enough on slow corp links), `MaxIdleConnsPerHost` 20 (up from 2 — sessions issue several parallel calls and the default forced a new dial + handshake for every extra concurrent request, itself a major source of the handshake timeouts we were seeing), and `IdleConnTimeout` 10m (up from 90s) so TCP/TLS connections stay warm across turns and we reuse instead of re-handshake. TCP keep-alive is inherited from the default dialer to detect dead paths. Regression test in `pkg/ai/providers/sse_transport_test.go`.

## [0.33.1] - 2026-04-23

### Changed

- TUI spinner label: rename `Working...` to `Inferring...` throughout the interactive mode (agent turn spinner, post-compaction resume spinner, and the default fallback for hint-based tool spinners like `aside`). A small cosmetic nod to the fact that what fir is actually doing while the wheel spins is LLM inference.

### Fixed

- Live-model filter incorrectly hid every built-in model of a provider once its live-list fetch completed. The regression was introduced in commit `47f0954f` ("feat: live-list new-model synthesis with provider extensions") where `liveModelState.models` was repurposed to store only *synthesised* metadata for IDs not in the built-in registry, but `has(modelID)` still scanned that same slice to confirm liveness. Because `synthesiseForLiveIDs` skips any ID already present as a built-in, all known models were absent from `s.models`, so `has("claude-sonnet-4-5")` returned `false` and `GetAvailable` filtered them out. Most visible in ACP mode's model selector (all 23 built-in Anthropic models vanished once the OAuth-authenticated live fetch succeeded). Fixed by adding a separate `liveIDs map[string]bool` on `liveModelState` that holds the raw ID list from `ListModels`; `has()` now consults that set while `models` continues to carry synth metadata for novel IDs. Cache format (`live-models-v2-<provider>.json`) grew an `ids` field; legacy v2 caches without the field are rejected so they're repopulated on next run (prevents silently reloading a broken cache). Test coverage: existing `TestLiveModelState_Filters` and `TestGetAvailable_WithLiveFiltering` were updated to pass both ID lists and synth slices; they continue to verify that `has()` returns true only for live-listed IDs.
- ACP mode: OAuth auth methods (Anthropic, Codex, Copilot, Gemini, Antigravity, Poe) were never advertised in the `initialize` response. `Initialize` called `buildAuthMethods` against an empty `oauth` registry because auth providers are registered by builtin extensions (`anthropic_auth.py`, `copilot_auth.py`, …) and ACP only started extensions inside `createSession` — after Initialize had already returned. An ACP client (Zed, etc.) therefore only saw `env-*` methods and had no way to start an OAuth flow. Fixed by adding `extension.SetupAuthProviders`, a parallel startup path that discovers extensions declaring `auth_providers` in frontmatter and handshakes them concurrently via the existing Manager goroutine pool (so the Initialize hit is one parallel wave of handshakes, not sequential). ACP Initialize now calls it before `buildAuthMethods`; Session setup excludes the already-running auth-extension names from its own `DisabledNames` to avoid double-starting them. Auth extensions are stopped on agent shutdown alongside session extensions. A nop `BridgeAPI` is used during this early phase because auth extensions only invoke `auth/*` helper RPCs (handled directly by `Bridge`) and never touch session-scoped APIs during login.
- First turn of a freshly-created session now includes the system prompt. `NewAgentSession` built `baseSystemPrompt` but never pushed it onto the underlying `agent.Agent`'s state — only `Reload()`, `NewSessionCmd()`, and `SwitchSession()` did. A brand-new session's first LLM call therefore went out with no `system`/`developer` message at all (confirmed empirically via captured OpenAI chat-completions requests against a mock backend, affecting TUI, print, and ACP modes equally). Fixed by moving the `Agent.SetSystemPrompt` call into `buildSystemPrompt()` itself so every build path (construction, new session, session switch, resource reload) propagates the prompt to the agent in one step — the four duplicated two-line sequences at each call site collapse to one, and no future caller can forget the second half. Regression test `TestAgentSession_NewAgentSession_WiresSystemPromptOntoAgent` constructs an agent with an empty initial `SystemPrompt` and asserts that `session.Agent.State().SystemPrompt` is non-empty and equal to `session.baseSystemPrompt` immediately after `NewAgentSession` returns (no `Reload` required).
- `anthropic-auth` builtin extension: corrected the Claude Code tool-name map for fir's two background-bash tools. They were previously mapped `bash_output → Monitor` and `bash_kill → TaskStop`, neither of which exist in Claude Code's tool surface — the renames were essentially decorative custom names that the OAuth backend would still see as foreign. They now map to the real CC names: `bash_output → BashOutput` and `bash_kill → KillShell`. `BashOutput` is unambiguous; `KillShell` is the name CC's own internal system prompt uses (the public Python SDK's `claude-agent-sdk` exposes it as `KillBash`, but the model-facing surface — which is what an OAuth backend would compare against — uses `KillShell`). The header comment listing the canonical CC surface in `pkg/resources/builtin_extensions/anthropic_auth.py` was updated to include `BashOutput` and `KillShell` accordingly.

## [0.33.0] - 2026-04-23

### Added

- `fir login <provider-id>` CLI subcommand: drives the interactive OAuth login flow for a named provider and persists credentials to `~/.config/fir/auth.json`, equivalent to the existing `--login` flag but available as a first-class subcommand alongside `fir skills` / `fir extensions` / `fir sessions`. Because OAuth providers are contributed by auth extensions (`anthropic_auth.py`, `copilot_auth.py`, `gemini_cli_auth.py`, `poe_auth.py`, `codex_auth.py`, `antigravity_auth.py`), the subcommand boots a minimal ephemeral session (no tools, no MCP, no skills, in-memory session store) that loads extensions the same way the main CLI does — honouring `settings.json` `extensions`, `--extension`/`-e` allow-listing, `--disable-extension`/`-d`, and `--no-extensions` — so every extension-registered provider is visible and usable. `fir login list` (or bare `fir login`) prints all registered providers after loading extensions. Also supports `--debug` for login-time log output.

### Removed

- `ls` tool. The fir-native directory-listing tool duplicated `bash ls`, was not in `DefaultCodingTools` (CLI/interactive/print never shipped it by default), and its system-prompt claim of respecting `.gitignore` was untrue — the implementation was a plain `os.ReadDir` with no ignore logic. `grep` and `find` are retained (ripgrep speed, structured glob matching); `ls` was the weakest of the three. Cleanup touched `pkg/agent/tools/ls.go` (deleted), `pkg/session/sdk.go` (`AllTools` dropped), `cmd/fir/app.go` (`--tools ls` whitelist + warning message), `pkg/modes/interactive/components/tool_execution.go` + `tree_selector.go` (display cases), `pkg/resources/systemprompt.go` (re-attributed `.gitignore` to `grep` only). Users who relied on the tool can do the same thing with `bash("ls <path>")`.

### Changed

- `plan-nudger` extension: revamped into a "keep-working" reminder.
  (1) **Renamed** `STUCK LOOP DETECTED` → `[CONTINUE]` across all nudge variants (mild/warn/crit/agent-end). The old wording produced false-positive reactions when the agent was actually making steady progress; the neutral tag reads as a loop-tick reminder instead of an accusation.
  (2) **Removed** the "rewrite the most problematic file completely from scratch" advice from the critical nudge. That was bad advice inside a healthy work loop — it invited the agent to second-guess the current approach and reply-to-explain instead of acting.
  (3) **Unskippable**: every nudge now says "Your next reply MUST contain a tool call" and forbids prose-only replies, so the agent can no longer satisfy the reminder by narrating.
  (4) **Concrete next-action hint**: nudge text now includes the last tool the agent ran and whether it errored (e.g. `Last tool was Bash (ok).` or `No tool has run yet this session.`), making inaction feel wrong. Implemented by subscribing to the new `tool_execution_end` event.
  (5) **Only fires on genuine idleness**: the nudge condition changed from "N turns since last plan update" (which tripped mid-loop even when the agent was calling tools every turn) to "N *consecutive* turns that ended without any tool call". A healthy loop tick — tool calls with successful exits — now resets the counter and never nudges. Idle-turn threshold is `2`; backstop wall-clock threshold raised from 60s → 120s. Agent-end nudge similarly reframed: end-of-turn is "a pause point in the loop, not a stopping point", and demands a tool call next rather than asking an open-ended question the agent can answer with prose.
- ACP mode no longer auto-registers `ls` alongside read/bash/edit/write/grep/find — it was the only mode doing so, and since the tool itself is now gone this closes the asymmetry flagged by the ACP system-tools audit (`pkg/modes/acp/tools.go::createAcpTools`). The ACP branch that falls back to `DefaultCodingTools` (no client Terminal/Fs capabilities) is unchanged.
- `anthropic-auth` builtin extension: map `bash_output` → `Monitor` and `bash_kill` → `TaskStop` in the Claude Code tool-name map. These two fir-specific tools (state accessors for ACP's `run_in_background` feature) previously went to Anthropic's OAuth endpoint under their raw fir names. The rename is cosmetic — neither `Monitor` nor `TaskStop` is part of CC's canonical tool surface (Agent/Bash/Edit/Glob/Grep/Read/ScheduleWakeup/Skill/ToolSearch/Write), so the endpoint still treats them as custom tool names — but it gives nicer wire-level display.

## [0.32.0] - 2026-04-21

### Fixed

- `fir --help` no longer claims `--provider` defaults to `google`. The actual resolution (in `pkg/models/modelresolver.go::FindInitialModel`) is: CLI flags → `settings.json` `defaultProvider`/`defaultModelID` → first available provider with a valid API key (in `knownProviderOrder`). The hardcoded `(default: google)` text was misleading — `ParseArgs` leaves `Args.Provider` empty when the flag is omitted, with no implicit provider.

### Added

- Print/JSON (`-p`) mode now waits up to 30 seconds for all configured MCP servers to finish their initial connect/initialize handshake before sending the first prompt. This fixes the case where `fir -p "use my mcp tool"` would race the LLM call against the MCP subprocess spawn and run without the tools being registered. New `(*mcp.Manager).WaitReady(ctx)` blocks until every `Start`-launched goroutine has settled (success or error) and is used under a `context.WithTimeout` from `cmd/fir/app.go` — a timeout only emits a stderr warning rather than aborting. Interactive and ACP modes opt into the same behaviour via a new `--wait-mcp` flag: the TUI prints `Waiting for MCP servers to initialize...` to stderr and blocks before `ui.Init()`; ACP blocks inside `createSession` after `session.Setup` returns (plumbed through `acpmode.Options.WaitMCP`) so the first `session/prompt` for that session sees every tool.
- Model selector rows now show model pricing in a dedicated aligned column as `[$input/$output]` (USD per million tokens) with two-decimal precision, and include a compact selected-model cost details line (`in/out/cache` per 1M tokens).
- `[FREE]` free-marker is now rendered in the price column (for Poe free variants), replacing prior inline placement.
- Model selector rows now align the price (`[$..]`/`[FREE]`) and SWE score (`[SWE:xx%]`) columns across the filtered list for stable readability while scrolling.
- Model picker highlights free Poe bots with a green `[FREE]` badge and coloured model ID, and sorts them ahead of paid duplicates. When two entries share the same provider + display name — the common Poe case where the same underlying model is exposed as both a paid bot and a free bot — the free variant is listed first so `/model` selects it by default. Scoped to `ai.ProviderPoe` on purpose: other zero-cost entries (GitHub Copilot, Gemini CLI, Antigravity, OpenAI Codex) are gated behind a subscription / OAuth plan, so labelling them as "free" would be misleading.
- Live-list new-model synthesis: when a provider's `/models` endpoint returns an ID that isn't in the built-in registry (e.g. a model released after the last `cmd/generate-models` run), fir now synthesises a `*ai.Model` for it on the fly so the user can select and use it without waiting for a release. The synthesised metadata is also persisted to disk (cache filename bumped to `live-models-v2-<provider>.json` so v1 caches from older builds are ignored rather than silently mis-decoded), so a model selected one session is resolvable on the next cold start before live-fetch completes. Resolution order in `ModelRegistry.Find`: built-in models → live-list state (authoritative when present — mistyped IDs against an authed provider return nil rather than a phantom model) → cold-start sibling-clone synthesis. New optional interfaces let providers ship per-provider heuristics: `oauth.ModelDefaulter` for OAuth providers (including extension-backed ones via the new `auth/model_defaults` JSON-RPC hook) and `models.ListerModelDefaulter` for API-key providers via `ModelLister`. Built-in defaulters: OpenAI parses families (`gpt-4o`, `gpt-4.1`, `gpt-5`, `o1`–`o4`) and clones the most recent same-family sibling — preserves correct `MaxTokensField`/`Reasoning`; OpenRouter parses `vendor/model` and clones from the same vendor (carries OpenRouter routing config); Anthropic parses `claude-{opus,sonnet,haiku}-…-yyyymmdd` and produces a readable name like `Claude Sonnet 4 7 (2026-06-01)`.
- Poe support for the OpenAI `/v1/responses` endpoint. Some Poe bots (notably `gpt-5.3-codex-spark`, the Cerebras-hosted ~1000 tok/s variant) don't expose `/v1/chat/completions` at all — they're reachable only via `/v1/responses`. The Poe catalog fetcher now picks an API per bot based on `supported_endpoints`: prefers `openai-completions`, falls back to `openai-responses` when only that is advertised. `buildOpenAIResponsesBody` / `convertResponsesInput` also learned to suppress three OpenAI/Azure-only extensions when talking to Poe: the `developer` role (Poe accepts only system/user/assistant), `reasoning.effort: "none"` (Poe's enum is `{low, medium, high, xhigh}`), and `include: ["reasoning.encrypted_content"]` (unknown include value). The shared Responses SSE processor also learned to cope with Poe's event shape: Poe lazy-sends `response.output_text.delta` without a preceding `response.output_item.added`, and for tool-call-only responses it sends NO delta events at all — just a final `response.completed` carrying the full `response.output[]` array. `handleTextDelta` now lazy-opens a message item when one isn't live (with an anonymous id — Poe's delta `item_id` is actually the *response* id, not the message id, so matching falls back to type), and `handleResponseCompleted` walks `response.output[]` to replay any items (text, reasoning, function_call) that were never streamed, tracked via a new `seenItemIDs` set. Detection mirrors `detectCompat`: `provider == "poe"` or base URL contains `poe.com`. Auth still flows through `POE_API_KEY`, so the existing `poe-auth` OAuth extension works unchanged.

- `/skills <name>` shows details for a loaded skill (name, source, location, description). The loaded-skills names are also offered as top-level autocomplete suggestions alongside `list` and `install`, so `/skills <tab>` cycles through subcommands *and* every loaded skill. The combined autocomplete is driven by letting `CommandArgSpec` carry both `SubCommands` and a static `Values` list at the first-arg position.
- `fir sessions [list]` subcommand: prints the sessions associated with the current working directory (resolved via `store.SessionDirForCwd`), as a table with id / modified / message count / name / first-message columns. Mirrors the existing `fir skills` / `fir extensions` / `fir packages` list UX and works without touching auth or extensions.
- Global `-C <dir>` flag (also `-C=dir`, `--cwd[=dir]`, `--directory[=dir]`): runs fir as if it had been launched in `<dir>`. Handled in `main` before argument parsing by `applyChdirFlag`, which `os.Chdir`s and strips the flag from `os.Args`, so every subcommand (including `sessions`, interactive, print, ACP) sees the requested cwd. Mirrors `git -C <dir>` / `make -C <dir>` ergonomics.
- Poe (poe.com) OAuth provider ("Sign in with Poe") via a new builtin extension `poe-auth`. Implements the OAuth 2.0 Authorization Code + PKCE flow documented at `creator.poe.com`, with a default client ID baked in for fir's registered app (overridable via `FIR_POE_CLIENT_ID` / `FIR_POE_REDIRECT_URI`). Run `fir --login poe` to authenticate. Poe doesn't issue refresh tokens, so on expiry the user is told to re-run `--login`. New env vars `POE_API_KEY`, `FIR_POE_CLIENT_ID`, `FIR_POE_REDIRECT_URI` documented in the registry; new provider constant `ai.ProviderPoe`.
- Poe model catalog (106 tool-capable bots) auto-generated from `https://api.poe.com/v1/models`: spans `gpt-5`, `claude-opus-4.x`, `gemini-3.x`, `grok-4`, `kimi-k2.x`, `glm-5`, `qwen3.x`, `minimax-m2.x`, `nova`, `seed-2.0`, and more. Context windows are resolved via a three-tier fallback (structured `context_window` → `parameters[].schema.maximum` for `max_output_tokens` → free-text regex scrape of model descriptions), plus sibling inheritance for `-fw`/`-tog`/`-t`/`-n` variants and a hardcoded override for `magistral-medium-2509-thinking` (Poe's metadata has a `40,000k` typo). Only 2 legacy models (`gpt-3.5-turbo-raw`, `gpt-3.5-turbo-instruct`) fall below 16k; every modern bot gets a real size signal.
- Poe Claude-family bots now route through Anthropic's native `/v1/messages` endpoint (instead of `/v1/chat/completions`) when Poe advertises it in the model's `supported_endpoints`. Preserves native `thinking` content blocks and Anthropic-shaped tool-use streaming that Poe's OpenAI translation would otherwise flatten. The 13 affected models (opus/sonnet/haiku 3→4.7) get `Api: "anthropic-messages"` at generation time; non-Claude bots stay on `openai-completions` since Poe returns a 200 error envelope for them on `/v1/messages`. `buildAnthropicHeaders` gains a Poe branch: `Authorization: Bearer <POE_API_KEY or OAuth access token>`, no `x-api-key`, no `anthropic-beta` headers (Poe's Bedrock-backed proxy rejects unknown beta flags). `cache_control` annotations are still emitted and currently dropped silently by Poe — if they fix that, fir gets prompt caching for free.

### Added

- `agent_introspect` builtin extension + host `agent.info` RPC: gives the agent a single tool that returns a structured JSON snapshot of the current runtime (version, mode, cwd, session id/file/name, model, context usage + compact mode, thinking level + available levels, message counts, token totals, cost). Backed by a new `AgentSession.Introspect()` method reusing existing helpers (`GetSessionStats`, `GetContextUsage`, `GetAvailableThinkingLevels`). Served per-session via the extension bridge so concurrent ACP sessions each get their own snapshot with no cross-session leakage. Python SDK gains `Context.agent_info()`; the builtin extension is ~20 lines that just forwards the RPC.
- `/session` output (both interactive and ACP) now includes a `Context:` line showing `<percent>% / <used> / <window> (<off|client|server>)`, so context-window usage and compaction mode are visible alongside the existing session details. Unknown usage (e.g. right after compaction) renders as `?%`. Backed by new `session.FormatContextUsage` helper and `AgentSession.CompactMode()` accessor.
- Interactive mode: new `Ctrl+S` keybinding (action `showSession`) prints the full session info inline — same content as `/session`, without typing the slash command. Exposed via `ActionShowSession` in the keybindings manager and listed in the keyboard shortcuts help text.

- Pressing the model-selector keybinding (default `Ctrl+L`) a second time while the selector is open now closes it, the same as `Esc`. Resolved dynamically via `KeybindingsManager.Matches(ActionSelectModel)` so it tracks any user rebinding in `keybindings.json`.

### Changed

- Builtin `review-and-fix` skill: highlight simplification as an explicit goal of the review phase (one-line emphasis above the checklist).
- Tool-name mapping for Anthropic OAuth in ACP is now extension-owned: the `anthropic-auth` builtin extension now ships a static `tool_name_map` via handshake, and the provider uses that map to translate between fir tool names and Claude Code names at startup (no hardcoded/duplicate mapping in provider code). This keeps mapping logic centralized in the auth extension and preserves ACP-only tool behavior (`bash_output`/`bash_kill` pass through unchanged if unmapped).

### Fixed

- Poe usage builtin skill (`poe-usage`) now resolves Poe OAuth credentials from `~/.config/fir/auth.json` (`poe.access`) as well as legacy `poe.key`, so usage queries work right after `fir login poe` without manually setting `POE_API_KEY`.

- Poe `assistant` bot (backed by `gpt-5.2-chat-latest`) no longer 400s with `Unsupported value: 'low' is not supported with the 'gpt-5.2-chat-latest' model. Supported values are: 'medium'.` when the user's thinking level is anything other than medium. Generalised as a metadata-driven clamp: `ai.Model` gained a `ReasoningEffortValues []string` field, populated by the Poe catalog generator from each bot's `parameters[].schema.enum` (Poe exposes narrower enums per bot — e.g. `gpt-5.4-pro` is `{medium, high, xhigh}`, `gemini-3.1-pro` is `{low, high}`, `grok-4.20-multi-agent` is `{medium, high}`). `buildOpenAIRequestBody` now snaps the requested effort to the nearest allowed neighbour on fir's canonical ladder (`none < minimal < low < medium < high < xhigh < max`) before serialising. The `assistant` bot is a special case — Poe ships `parameters: []` for it despite `supports_reasoning_effort: true`, so the generator emits a hardcoded `ReasoningEffortValues: []string{"medium"}` override (removable once Poe populates the enum).
- Homebrew formula install: the GoReleaser-generated `Formula/fir.rb` previously hardcoded `bin.install "fir"`, but `archives.formats=binary` publishes raw binaries named `fir-<os>-<arch>`, causing `brew install kfet/fir/fir` to fail with `Errno::ENOENT: No such file or directory - fir`. Updated the `brews.install` template (and the reference `homebrew/fir.rb.template`) to glob the staged artifact: `bin.install Dir["fir-*"].first => "fir"`. Durable — every future release regenerates the tap's formula from this config.
- Flaky test `TestStartUpdateNoticeWatcher_ShowsNotice` (CI run 24650519048): the watcher goroutine raced with the test's single `waitRender()` check. Replaced with a poll loop that re-renders up to 2s.
- Poe API compatibility: fir no longer sends `"store": false` on OpenAI-completions requests routed at `api.poe.com`. Poe rejects the field with `400 body.store: property 'body.store' is unsupported`, which broke every Poe-hosted model (e.g. `gpt-oss-120b-cs`). Added `provider == "poe"` / `baseURL contains "poe.com"` to the `isNonStandard` set in `detectCompat`, so `SupportsStore` is `false` for Poe and the field is omitted. Same treatment keeps `developer`-role and other OpenAI-only extensions off the wire for Poe.

## [0.31.0] - 2026-04-19

### Added

- `/reexec` now accepts an optional continuation prompt that is injected as the first user message after the re-exec. Forms: `/reexec -- <prompt>`, `/reexec <path> -- <prompt>`, and the shorthand `/reexec <prompt>` (when the first token doesn't look like a path, i.e. contains no `/` and doesn't start with `.`). The prompt rides along in the existing reexec sidecar `QueueMessages`, which is already replayed via `Agent.FollowUp` on the restored side, so no new plumbing was needed.

### Fixed

- Builtin slash commands (e.g. `/help`, `/model`, `/compact`) are now added to the editor's prompt history, so they can be recalled with up-arrow like regular prompts, `!`-bash commands, and extension slash commands. Previously the builtin-dispatch branch in `setupEditorHandlers` cleared the editor without calling `AddToHistory`.
- `/reload` now also refreshes the live model list. Previously, once a session was running the live model lists fetched from provider APIs were effectively frozen for the session lifetime (cached in-memory, with a 1-hour on-disk TTL that would also survive a reexec). After a binary bump, rotated API key, newly-added provider, or upstream model catalogue change, users had to kill and restart the session to see new models. `Session.Reload()` now calls `ModelRegistry.Refresh()` + new `ModelRegistry.RefreshLive(ctx)`, which drops the in-memory live-model state, deletes the `live-models-*.json` disk caches, and re-kicks the same background fetchers used at startup.
- `/reexec` (and the `/update`-triggered restart) now detects when the new binary ships a different set of builtin extensions and SIGKILLs the inherited extension subprocesses before the new manager starts. Previously `syscall.Exec` deliberately preserved the running extension pool for warm restarts, but any builtin added/removed/modified across the release boundary would either silently fail to load (clash with the orphan still holding its name/sockets) or remain stale. The check is hash-based: when the embedded `BuiltinExtensionsHash()` matches what the outgoing process recorded in the reexec sidecar, behavior is unchanged (warm reexec); only on mismatch are the orphans killed. A full kill-and-restart is no longer required to pick up new extensions like `anthropic-auth`.
- Flaky race test `TestHandleDequeue_ClearsQueueAfterDequeue` (and 17 sibling tests in `pkg/modes/interactive` sharing the same pattern) surfaced by CI run 24623416594. The `testMode.waitRender()` helper relied on a fixed 10 ms `time.Sleep`, racing against the asynchronous TUI render goroutine. Replaced with a synchronous `ui.DoRender()` call preceded by a single `runtime.Gosched()`, which is deterministic under `-race` and adds no extra sleep on the happy path.

### Changed

- `self` builtin skill: added "Installing fir" section covering `brew install kfet/fir/fir`, the `install.sh` one-liner, and `fir update` self-update, with a note disambiguating from `fir install <source>` (package manager).
- `review-and-fix` skill: final summary now leads with a one-line high-level description of what the reviewed code implements, giving reviewers immediate context before the file/iteration/issue breakdown.
- `provider-usage` extension no longer auto-loads: frontmatter flipped to `builtin: false`. It is still embedded in the binary and can be enabled explicitly (`--extension provider-usage` or via `settings.json` `"extensions"`). Rationale: it's a TUI-only status-bar widget that polls provider APIs every 5 minutes — not a sensible always-on default, especially for non-interactive / headless fir sessions.
- `tmux-driver` skill: new "Spawning `fir` Inside a Window" section instructing agents to pass `-d auto-namer -d notify -d provider-usage -d tmuxspinner` when they launch a child fir process in a tmux-driver window. These four builtin extensions fight tmux-driver's fixed window-name routing (`auto-namer`, `tmuxspinner`), spam the host desktop (`notify`), or render into a non-existent status bar (`provider-usage`).
- `shepherd` skill: worker-launch example now includes the same four `-d` flags.

### Added

- `extensionPaths` settings key: list of directories to scan for extensions, mirroring the existing `skills` / `prompts` / `themes` path arrays. Entries support absolute, `~/`-relative, and cwd-relative forms; missing paths are silently skipped. Works in both global (`~/.config/fir/settings.json`) and project (`.fir/settings.json`) configs. Lets you keep extensions under e.g. `<project>/extensions/` without symlinking into `.fir/extensions/`.
- Homebrew tap: `brew install kfet/fir/fir` now works. GoReleaser auto-publishes `Formula/fir.rb` to `kfet/homebrew-fir` on every release, with download URLs pointing at the public `kfet/fir-dist` mirror so installation requires no GitHub authentication. Uses the existing `HOMEBREW_TAP_TOKEN` secret.

### Changed

- `install.sh`, the in-binary self-updater (`fir update`, background notice), and the `licensesURL` embedded in the binary now read from the public [`kfet/fir-dist`](https://github.com/kfet/fir-dist) mirror instead of `kfet/fir`. No GitHub authentication is required to install or update. Dropped the `gh` CLI / `GITHUB_TOKEN` fallback paths and the `FetchLatestOrGH` / `ghToken` helpers from `pkg/update`.
- `homebrew/fir.rb.template` points at `kfet/fir-dist` too.

## [0.30.0] - 2026-04-19

### Added

- Thinking/reasoning effort level `max`: new top-tier reasoning level. Anthropic adaptive-thinking models (Opus 4.6+, Sonnet 4.6+, Opus 4.7) support `max` natively on first-party, Bedrock, and Vertex surfaces. Models that don't support it clamp down to the highest tier they do support (`xhigh` if available, else `high`).
- `ai.SupportsMax(model)` capability check and `ai.ThinkingMax` / `agent.ThinkingMax` constants.
- `providers.ClampReasoningForModel(level, model)`: model-aware clamp — preserves `xhigh`/`max` when the model supports them and clamps down otherwise. OpenAI Completions, OpenAI Responses, Azure OpenAI Responses, Codex Responses, and Bedrock (adaptive path) now use it so the requested level propagates correctly per-model.
- Bedrock: `xhigh` now propagates to Opus 4.7 on Bedrock (previously lost in the simple-stream clamp).
- Third-party attribution: `THIRD_PARTY_NOTICES.md` is now generated at release time via `go-licenses` and uploaded as a release asset alongside `LICENSE` and `checksums.txt` (sha256 covers every asset). New `make notices` and `make check-licenses` targets; the license check is wired into `make all` and fails on forbidden/restricted licenses.
- `fir --version` / `-v` prints a second line with the MIT license and a link to the exact release page. The URL is injected at build time via `-ldflags -X main.licensesURL=…`.
- Release workflow now mirrors every release to [kfet/fir-dist](https://github.com/kfet/fir-dist) — a public, binaries-only mirror that will eventually become the download source for `install.sh`, the self-updater, and distro packagers. Requires a `FIR_DIST_TOKEN` secret (fine-grained PAT scoped to `fir-dist` with Contents: read+write). If unset, the mirror step is skipped with a warning and the source release still succeeds.

### Changed

- Upstream sync to v0.67.68 (a1edb8a4): Claude Opus 4.7 recognised as adaptive-thinking across Anthropic/Bedrock/Vertex (including xhigh effort); z.ai `tool_stream: true` streams tool-call deltas on supported models (glm-4.5 family excluded via new `ZaiToolStream` compat flag); OpenAI Responses + Codex (SSE & WebSocket) now send `session_id` and `x-client-request-id` so session-keyed prompt caches hit; OpenRouter cache-write accounting fixed (`cache_write_tokens` subtracted from reported `cached_tokens`); `OpenRouterRouting` expanded to the full API surface (`allow_fallbacks`, `require_parameters`, `data_collection`, `zdr`, `enforce_distillable_text`, `ignore`, `quantizations`, `sort`, `max_price`, `preferred_min_throughput`, `preferred_max_latency`); context-overflow detection recognises Anthropic HTTP 413 `request_too_large` and excludes Bedrock throttling / rate-limit messages; `models.json` entries for built-in providers inherit `api`/`baseUrl` from the first built-in model; Gemma 4 family uses thinking-level semantics; Gemini 2.5 Flash Lite gets its own budget schedule; Antigravity default client version bumped `1.18.4` → `1.21.9`; kimi-coding default model renamed `kimi-k2-thinking` → `kimi-for-coding` (legacy `k2p5` normalizes); `qwen-chat-template` thinking now sets `preserve_thinking: true`; Vertex literal `gcp-vertex-credentials` treated as "use ADC"; `ProviderResponse` / `OnResponse` hook plumbed through `StreamOptions` and `BuildBaseOptions` (not yet invoked by fir's HTTP callers); per-tool `ToolExecutionMode` added to `AgentTool`.
- `ai.SupportsXhigh` narrowed on Anthropic: only **Opus 4.7** exposes a distinct `xhigh` tier. Opus 4.6 / Sonnet 4.6 now clamp `xhigh` to `high` and use the new `max` tier for their top effort. OpenAI `gpt-5.2`/`gpt-5.3` continue to support `xhigh` as before. `SupportsXhigh`/`SupportsMax` now recognize Anthropic model IDs across every API surface (first-party, Bedrock, Vertex), not just `ApiAnthropicMessages`.
- `providers.ClampReasoning` now folds both `xhigh` and `max` down to `high` (it's the safe fallback for providers that don't know about the higher tiers). Model-aware callers should use `ClampReasoningForModel` instead.
- Anthropic `mapThinkingLevelToEffort` is now model-aware (signature `(level, modelID)`): returns `xhigh` only for Opus 4.7, `max` for the new top tier, clamps other cases down.
- Bedrock `bedrockThinkingLevelToEffort` follows the same rule: `xhigh` only for Opus 4.7, `max` for all adaptive models.
- Codex `clampCodexReasoningEffort` accepts the new `max` effort and clamps it to `xhigh` up-front (no Codex model supports `max`), preserving existing model-specific rules.
- `--thinking` flag, interactive settings selector, theme color palette, and `/thinking` resolver all accept `max`.
- `clampThinkingLevel` (CLI) falls back `max` → `xhigh` → `high` based on model capability; `GetAvailableThinkingLevels` exposes `xhigh`/`max` only for supporting models.

### Fixed

- Redeploy no longer leaks agent processes. Previous versions installed a process-wide SIGHUP handler that converted SIGHUP into an in-place re-exec. When `tmux respawn-window -k` (used by `make poe-deploy`) or any ssh/tty hangup delivered SIGHUP, fir re-exec'd itself instead of exiting, detaching from the dying pane. Because MCP/extension subprocesses run in their own process groups (Setpgid), `syscall.Exec` preserved them across the re-exec, so each unintended SIGHUP orphaned a tree of subprocesses. SIGHUP now takes its default action (terminate); `/reexec` and `/update` continue to work since they call the reexec path directly without signals.
- OAuth tokens no longer sent as `x-api-key` when the auth extension is not loaded; `GetApiKey` returns empty instead, surfacing a clear "no API key" error.

### Removed

- `ANTHROPIC_OAUTH_TOKEN` environment variable — OAuth tokens require refreshing, which env vars cannot support. Use `fir login anthropic` instead.

## [0.29.0] - 2026-04-16

### Added

- `/new <prompt>` — atomic session clear + initial prompt. The prompt is submitted atomically after the session clears, fixing a race condition in self-handoff where `/new` and the follow-up message were sent as separate inputs.
- Auto-reply rich rendering: tool calls render inside markdown code fences (with language hints like `bash`, `text`, `tool`). Bash commands show `$ cmd` syntax. Tool output truncated to 8 lines with a line count when longer.
- Auto-reply thinking blocks: LLM thinking/reasoning streamed as italic blockquotes (`> *thinking...*`), giving visibility into the model's reasoning process.
- Auto-reply plan rendering: plan tool calls render as rich markdown with Unicode progress bars (`████░░░░`), status icons (✓/→/○), priority markers (❗), and metadata.
- `ChannelImage` type for multi-modal channel messages — base64-encoded image with MIME type and optional name.
- `MessageInjector` now accepts `any` content (string or `[]any` content blocks) instead of just `string`, enabling multi-modal message injection with text + images.
- MCP: channel meta (chat_id, message_id, etc.) included in injected message text.
- MCP: history preamble injection on empty sessions.
- MCP: `SendChannel` waits for connection instead of failing immediately.
- Session: wait for ExtReady before injecting channel messages.
- Shared SIGHUP reexec handler across all modes; interactive SIGHUP triggers graceful reexec.
- Adaptive grace period + faster reconnect + rejection notification for channel connections.
- Builtin skill: `telegram-bot-setup` — document subdir install form.
- Builtin skill: `remote-update` — self-update + reexec over channel.
- `make bridges-install` target for external bridges.

### Changed

- Self-handoff skill updated to use `/new <prompt>` instead of two-step `tmux send-keys`.
- Auto-reply tool arg formatting: bash commands show up to 120 characters (was 80) with `$ ` prefix; file paths use space separator instead of colon.
- Auto-reply only wires for Poe-style reply tools (not all MCP tools).

### Fixed

- Agent loop: drain follow-up queue after error/abort before exiting loop — prevents silently dropped channel messages after a 429 or other LLM error.
- Auto-reply: prevent `send on closed channel` panic — `sendCh` is never closed; a `closed` flag gates all sends.
- Auto-reply: recover from send-on-closed-channel race via deferred recover in `sendChunk`.
- Auto-reply: only finalize on `agent_end`, not `message_end`.
- Auto-reply: async send queue prevents agent event loop deadlock.
- Auto-reply: intercept manual `reply()` when auto-reply is active.
- Auto-reply: wire even when MCP tools not yet loaded.
- Extensions: handle empty temp cache dir from macOS cleanup.
- Agent callbacks moved to Config for race-safe initialization.
- Channel-based sync in reconnect tests, fix data race.
- Review pass: dead code removal, goroutine leak fix, ACP reexec safety, grep safety.

## [0.28.0] - 2026-04-08

### Added

- MCP server initialization notifications: the TUI and ACP mode now show a message when each MCP server finishes connecting (or fails), so users know when tools are ready.
- `/session` command now shows a **Tools** section listing built-in tools and extension tools grouped by extension name. MCP tools are excluded (use `/mcp` instead).
- `/session` command now shows a **Paths** section with SDK and skills extraction directories for debugging.
- `FIR_EXT_TIMEOUT` environment variable: configurable extension init handshake timeout in seconds (default: 5). Useful for slow hardware (e.g. Raspberry Pi) where extensions need more time to start.
- `pkg/envvars` registry: single source of truth for all `FIR_*` environment variables. Both CLI `--help` and the `self` skill now read from the same registry, so documentation can never drift.
- `self` skill: added `## Environment Variables` section with full table of all public env vars, auto-generated from the registry via `{{FIR_ENV_VARS_TABLE}}` placeholder.
- Extension startup failure notifications: when an extension fails to start, a warning is shown. Auth extensions get a prominent warning; regular extensions get a subtle muted message.
- Deterministic auth provider conflict resolution: when multiple extensions register the same auth provider ID, the highest-scope extension wins (project > global > package > builtin). Same-scope ties are broken alphabetically and produce a user-visible warning.

### Removed

- Legacy `~/.fir/agent/` config path support: removed `LegacyFirAgentDir()`, `MigrateConfigFromLegacyDir()`, legacy extension discovery fallback, legacy session directory scanning, and `provider-usage` extension cache/config paths. All users should already be on `~/.config/fir/`. The `~/.pi/agent` fallback for Claude Code sessions is retained.

### Fixed

- Extension shutdown is now parallelised: event emission and process stopping happen concurrently instead of sequentially, significantly reducing exit latency on low-powered hardware (e.g. Raspberry Pi) with multiple extensions.
- Model selector no longer calls `Refresh()` synchronously when opened, eliminating a slow full reload of models.json, built-in models, and OAuth hooks on every open. Dramatically improves model selector responsiveness on low-power devices like Raspberry Pi.
- Fixed flaky `TestHandleQueueCommand_ShowsQueuedMessages` test by replacing fixed sleep with polling.
- After `/reexec`, closing the session no longer kills the parent terminal (restore stdin blocking mode on every exit path, not just before exec). AST-based regression test added to prevent this from regressing again.
- `plan-nudger` extension: added "do not call plan again if you already did" guard to all nudge levels, preventing duplicate plan tool calls when a nudge races with an in-flight plan update.
- Ctrl+N (new session) now cancels any in-progress LLM stream before starting a new session.
- `AgentSession.Close()` now aborts any in-flight LLM stream before tearing down, preventing leaked goroutines in ACP session cleanup and shutdown.

### Changed

- Renamed `SessionManager` to `SessionStore` throughout the codebase to better reflect its role as a persistence/storage layer.
- `plan-nudger` extension: softened mild nudge to say "Use the plan tool" and encourage continuing work, instead of forbidding text replies. Added reminder to inform the user of work done on agent exit.

### Fixed

- ACP `session/resume`: MCP servers from project config and client request are now loaded (previously no MCPs were started on resume).
- `/reload` command: MCP servers are now reloaded from disk in both interactive and ACP modes. If no MCP servers existed at startup but configs are added later, `/reload` creates and wires them.
- Extracted shared `session.StartMCPManager` and `session.ReloadMCP` helpers to deduplicate MCP wiring across all modes.

## [0.27.0] - 2026-03-31

### Added

- `anthropic-auth` extension: Anthropic (Claude Pro/Max) OAuth provider ported from Go to a Python builtin extension, with OAuth-specific headers (authorization, beta prefix, system prompt, user-agent) injected via `modify_models`.
- `copilot-auth` extension: GitHub Copilot OAuth provider using the device code flow, ported from the Go implementation to a Python extension.
- `codex-auth` extension: OpenAI Codex OAuth provider ported to a Python builtin extension.
- Support sparse checkout for subdirectory package installs.
- Settings-based resource lookup paths: `"skills"`, `"prompts"`, and `"themes"` arrays in `settings.json` now add extra directories to the resource search. Relative paths resolve against the working directory, so a global `"skills": ["skills"]` discovers `./skills/` in every project automatically.

### Changed

- Unified Authorization header construction across all 8 providers with a shared `BuildRequestHeaders` helper enforcing canonical 3-layer merge order (auth < model.Headers < options.Headers).

### Fixed

- Interactive mode: initial prompt (CLI `fir 'task'`) no longer races against extension startup — waits for auth extensions to apply OAuth headers before making the first API call, fixing 400 errors on launch.
- Anthropic OAuth: token is now fetched fresh on every API call via `GetApiKey()` (with auto-refresh) instead of using a stale token baked into model headers at startup, fixing 401 errors after token expiry.
- `plan-nudger` extension: rewrote nudge messages to be directive ("Call the plan tool now… Do not reply with text") instead of conversational ("Reminder: update your plan") — prevents the model from responding with plain text instead of actually calling the plan tool.
- `plan-nudger` extension: `agent_end` nudge now asks the model whether it intended to stop instead of unconditionally commanding it to continue, avoiding hijacking one-off user questions mid-plan.
- `plan-nudger` extension: nudge messages now visible in UI (`display=True`) so users can see when nudges fire.
- `openai_codex_responses`: auth headers were set last, preventing user config overrides via model/options headers.
- `google_gemini_cli`: model.Headers were previously ignored entirely.
- Auto-clear transient command status messages (e.g. "No plan entries.") on the next turn or Escape instead of on a timer.
- Fixed sporadic `Error: 400` from Anthropic API when using the `aside` tool on long conversations by removing `trimMessagesForSideQuery` entirely. The old trimming dropped messages from the front of the conversation, which invalidated the provider's prompt cache and caused cache thrashing. Side queries now send the full conversation prefix (preserving cache hits) and let context-overflow errors surface gracefully.
- Fixed `Error: 401 invalid x-api-key` after `/reexec` when using OAuth auth. OAuth provider extensions now auto-refresh the model registry on registration, and the session model is re-resolved to pick up Bearer token headers.
- Fixed vague `Error: 400 Error (request-id: ...)` messages from Anthropic by surfacing the full API error body when the extracted message is too short.
- Show spinner ("Running /aside...") during extension slash command dispatch so the user sees progress while the command runs.
- Fixed `✓` (U+2713) and other dingbats being miscounted as width 2 instead of 1, causing rendering artifacts (ghost ANSI codes, truncated text) in the plan widget. Replaced hand-curated `couldBeEmoji` heuristic with `uniseg.StringWidth` which uses Unicode Emoji_Presentation property tables.
- Fixed plan widget data race: `updateDisplay` modified Box children without synchronization while `Render` read them concurrently from the TUI goroutine, causing ghost ANSI fragments (e.g. `250;123m`) from inconsistent line counts during diff rendering. Plan children are now rebuilt atomically under a mutex.
- Fixed plan widget metadata key ordering: map iteration produced random key order, causing unnecessary diff redraws on every plan update. Metadata keys are now sorted for stable rendering.
- Removed redundant double-padding in `Box.applyBg` → `ApplyBackgroundToLine` pipeline.
- Fixed 401 `Invalid authentication credentials` error when OAuth token expires mid-session: Anthropic provider now detects auth errors (401/403, `authentication_error`), calls `RefreshApiKey` to obtain a fresh token from the auth extension, and retries the request transparently.
- Added signal handler to ACP mode for clean extension shutdown.

### Removed

- Removed Go-side `GitHubCopilotProvider` (`pkg/ai/oauth/github_copilot.go`); login, refresh, token exchange, and `ModifyModels` are now handled entirely by the `copilot-auth` builtin extension.
- Removed Go-side `AnthropicProvider` (`pkg/ai/oauth/anthropic.go`); login, refresh, token exchange, and OAuth header injection are now handled entirely by the `anthropic-auth` builtin extension.

## [0.26.2] - 2026-03-29

### Changed

- Pinned GitHub Actions to commit SHAs for supply-chain security: `actions/checkout` v6.0.2, `actions/setup-go` v6.3.0, `goreleaser/goreleaser-action` v7.0.0, `astral-sh/setup-uv` v7.3.0.
- CI and release workflows now run `make all` instead of individual commands, ensuring CI matches local dev exactly.
- `make all` now includes `go vet` in its parallel targets.

### Fixed

- Fixed nil pointer dereference in MCP tool re-listing when `InitializeResult()` returns nil during a race with session initialization.
- Fixed data race between MCP `Connect()` and `ToolListChangedHandler` calling `InitializeResult()` concurrently; capabilities are now cached after connect.

## [0.26.1] - 2026-03-28

### Changed

- Regenerated model definitions with latest upstream data.
- Restored Python build optimizations: parallel test targets, lint coverage for testdata, and per-file-ignores in pyproject.toml.

### Fixed

- Fixed flaky `TestAnthropicLogin_EndToEnd` in CI: OAuth callback server now uses a dynamic port to avoid conflicts.
- Fixed Python 3.9 compatibility: replaced `datetime.UTC` with `datetime.timezone.utc` and `X | Y` type unions with `Optional[X]` across extensions and tests.
- Enforced Python ≥3.9 floor in pyproject.toml and documented the requirement.

## [0.26.0] - 2026-03-28

### Added

- `/reload` now also reloads MCP server configs from disk — adds new servers, removes deleted ones, and restarts changed ones.
- `/mcp` slash command to inspect configured MCP servers: shows connection status, transport, capabilities (resources, prompts), and a full list of exposed tools with descriptions. Available in both TUI and ACP modes.
- ACP: `session/resume` with a bare UUID (sent by Zed when opening a new window) now resolves the UUID to a session file and uses `flock` to detect if the session is active in another process; if locked, creates a fresh session instead of failing.

### Fixed

- ACP: `session/resume` rejected sessions stored in legacy directories (`~/.fir/agent`, `~/.pi/agent`) with "must be within sessions directory" error.

### Changed

- ACP `/session` command now uses Markdown lists for cleaner, more readable formatting.
- MCP server status now shows "connecting" instead of "disconnected" while a server is still establishing its connection.

## [0.25.0] - 2026-03-27

### Added

- Render thinking blocks in a rounded border frame instead of plain indented text.
- Unified OAuth callback server with server-side state validation, replacing per-provider callback implementations.
- MCP server status now shown in `/session` output.
- `merge-to-main` builtin skill.
- `tui.UI` interface extracted from interactive mode for cleaner abstraction.

### Fixed

- Session replay now renders tool calls and matches results to calls by ID.
- Show manual paste prompt immediately during OAuth login instead of waiting for callback timeout.
- Send `Chatgpt-Account-Id` header in OpenAI Codex `ListModels` requests.
- Provider usage backoff no longer compounds jitter, fixing ~2.5× growth per 429 instead of intended 2×.
- Snapshot streaming `AssistantMessage` in `Push` to prevent data race.
- Flaky OAuth callback server tests now use port 0 instead of hardcoded ports.

### Changed

- OAuth callback HTML page now has dark mode support and improved styling.
- Worktree prompt simplified for launching fir in new tmux windows.

## [0.24.0] - 2026-03-25

### Removed

- Removed scoped models feature (`/scoped-models` command, `ScopedModel` type, `ResolveModelScope`). Model cycling with `Ctrl+P` now always cycles through all available models.

### Added

- MCP channel server support: servers that advertise the `claude/channel` experimental capability can now push messages into the running session via `notifications/claude/channel` notifications. Channel messages are automatically injected into the agent conversation. The server's `channel_reply` tool works as a regular MCP tool with no special handling needed.
- Provider usage status bar now shows `⚠ rate-limited` or `⚠ stale` indicators when usage data cannot be refreshed due to API rate limits or fetch errors.
- Show a `⠋ Working...` spinner inside aside (and other hint-based extension tool) components while the tool call is in progress. Extensions can update the spinner text via `ctx.report_progress()` (e.g. "Calling Read...", "Synthesizing...").

## [0.23.0] - 2026-03-21

### Fixed

- ACP `session/list` now returns sessions from all projects when no `cwd` filter is given, matching the ACP spec. Previously it resolved to an empty/wrong directory.
- ACP `session/load` method is now handled, enabling session history loading in Zed. `session/resume` no longer replays history per the ACP spec.
- ACP debug log now dumps full JSON-RPC params instead of truncating to 200 chars.
- Doctor extension no longer crashes on `session_start` when params is `None`.
- Extension `tool_call` hook timeout is now activity-aware — the 30s deadline resets whenever the extension sends any message (request or response), preventing spurious timeouts for long-running tools like `aside` that make multiple bridge calls.
- Skills are now included in the system prompt even when a custom system prompt (`SYSTEM.md` / `--system-prompt`) is active; previously the custom override silently dropped the `<available_skills>` block.

### Changed

- Migrate Google Antigravity OAuth provider from Go core to a builtin Python extension (`antigravity_auth.py`), matching the earlier Gemini CLI migration pattern.
- Remove `google-antigravity` and `google-gemini-cli` from the built-in Go provider registry; both are now provided by builtin extensions.

### Added

- `aside` tool now accepts an optional `title` field on each tool call entry, shown in the TUI output header (e.g. `--- Bash: check go.mod ---` instead of `--- Bash ---`).

- `install` builtin extension — exposes package management as in-session slash commands (`/install`, `/uninstall`, `/packages`, `/update`) and AI tools (`install_package`, `uninstall_package`, `list_packages`, `update_packages`).
- `doctor` builtin extension — records tool errors and session failures to `~/.config/fir/doctor.jsonl`; exposes `doctor_query` and `doctor_summary` tools plus `/doctor` slash command for cross-session diagnostics.
- `session_end` event with `{reason, error}` payload emitted before `session_shutdown` on all exit paths (normal, error, reexec).
- `tool_execution_end` event now includes `error_text` when `is_error` is true.
- `docs/extension-protocol.md`: full wire-protocol reference covering transport, init handshake, tool calls, hooks, events, all ext→fir bridge methods, frontmatter, discovery/trust, and process lifecycle.
- `docs/extensions.md`: removed duplicated Protocol Reference section and stale Context Methods table; added cross-links to `extension-protocol.md` and `fir_ext.py`.
- `pkg/extension/sdk/python/fir_ext.py`: replaced terse module docstring with a self-contained protocol reference covering every message type, all bridge methods with exact params/responses, event table, and lifecycle notes.

## [0.22.0] - 2026-03-17

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
