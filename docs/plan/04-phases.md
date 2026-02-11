# Implementation Phases

## Phase 0: Scaffolding

- `go mod init github.com/kfet/pi-go`
- Makefile with cross-compilation targets
- Directory structure
- `sync/UPSTREAM_MAP.md` — the master TS↔Go file map

## Phase 1: AI Layer (`pkg/ai/`)

Foundation — everything depends on talking to LLMs.

**Port order:**
1. `types.go` — Message, Model, Tool, Context, Usage, StreamOptions
2. `eventstream.go` — `chan AssistantMessageEvent` with `Result()` 
3. `models.go` — model registry, `GetModel()`, cost calculation
4. `envkeys.go` — env var API key resolution
5. `registry.go` — provider registry pattern
6. `providers/options.go` — shared reasoning/budget logic
7. `providers/transform.go` — message format transforms
8. `providers/anthropic.go` — first provider (most common)
9. `providers/openai.go` — OpenAI + compatibles
10. `providers/google.go` — Gemini
11. `stream.go` — `StreamSimple()` dispatcher

**Go-specific:** Use raw `net/http` for SSE. No provider SDKs → smaller binary, no CGo.

## Phase 2: Agent Loop (`pkg/agent/`)

1. `types.go` — AgentTool, AgentMessage, AgentState, AgentEvent, ThinkingLevel
2. `loop.go` — stream → collect tool calls → execute → steer/followup → repeat
3. `agent.go` — Agent struct: prompt/steer/followUp/abort/subscribe

**Go-specific:** Agent loop runs in goroutine, emits events via channel. `context.Context` for cancellation.

## Phase 3: Tools (`pkg/core/tools/`)

1. `truncate.go` — output truncation (shared by read + bash)
2. `pathutils.go` — path resolution
3. `read.go` — file reading, image detection, truncation
4. `bash.go` — `os/exec`, process groups, timeout
5. `edit.go` — surgical text replacement
6. `write.go` — file creation
7. `grep.go` — pattern search (respects .gitignore)
8. `find.go` — glob file finding
9. `ls.go` — directory listing

**Go-specific:** Image resize via Go's `image` stdlib + `x/image` (replaces photon-node WASM). Process tree kill via `syscall.Kill(-pgid, SIGKILL)`.

## Phase 4: Session Manager (`pkg/core/session.go`)

JSONL append-only log with branching. ~1400 lines TS → likely similar in Go.

## Phase 5: Core Infrastructure (`pkg/core/`)

Port in dependency order:
1. `defaults.go`, `messages.go`
2. `authstorage.go`, `settings.go`
3. `systemprompt.go`, `skills.go`, `prompttemplates.go`
4. `modelregistry.go`, `modelresolver.go`
5. `resourceloader.go`, `keybindings.go`
6. `bashexec.go`, `slashcmds.go`
7. `compaction/` (compaction.go, utils.go)
8. `sdk.go` — CreateAgentSession factory
9. `agentsession.go` — the big one (2785 lines)

### 🎯 Milestone: Print Mode Works (after Phase 5)

```bash
echo "Hello" | pi-go -p
```

Validates the entire pipeline without TUI.

## Phase 6: TUI (`pkg/tui/`)

1. `terminal.go` — raw mode, resize, Kitty protocol
2. `keys.go` — ANSI/Kitty key parsing (1152 lines TS — complex)
3. `utils.go` — `visibleWidth()`, ANSI slicing (889 lines TS)
4. `tui.go` — differential renderer (1154 lines TS — the tricky one)
5. `image.go` — Kitty/iTerm2 image protocols
6. `components/` — text, box, input, editor, markdown, selectlist, loader

## Phase 7: Interactive Mode (`pkg/modes/interactive/`)

The 4362-line monster. Wires TUI + AgentSession + slash commands + overlays.

## Phase 8: Print & RPC Modes

- `modes/print.go` — streaming stdout output (124 lines TS)
- `modes/rpc/` — JSON-RPC over stdin/stdout

## Phase 9: CLI Entry Point (`cmd/pi/`)

Arg parsing, mode dispatch, package management commands.

## Phase 10: OAuth Flows (`pkg/ai/oauth/`)

OAuth authentication for providers. Required for Anthropic (Claude), GitHub Copilot, Google (Gemini CLI, Antigravity), and OpenAI Codex.

**Port order:**
1. `types.go` — OAuthCredentials, OAuthProviderInterface, OAuthLoginCallbacks (~59 lines TS)
2. `pkce.go` — PKCE code verifier + challenge generation (~34 lines TS)
3. `anthropic.go` — Anthropic OAuth flow (~138 lines TS)
4. `github_copilot.go` — GitHub Copilot device flow (~381 lines TS)
5. `google_antigravity.go` — Google Antigravity OAuth (~457 lines TS)
6. `google_gemini_cli.go` — Google Gemini CLI auth via gcloud (~599 lines TS)
7. `openai_codex.go` — OpenAI Codex OAuth (~455 lines TS)
8. Wire into `modelregistry.go` — token refresh, model modification
9. Wire into `interactive/mode.go` — `/login`, `/logout` slash commands
10. Wire into `authstorage.go` — token refresh with file locking

**Go-specific:** Use `crypto/sha256` for PKCE (simpler than Web Crypto). Use `net/http` for callback servers. File locking via `syscall.Flock`.

## What's Deferred

| Feature | Reason | TS Lines |
|---|---|---|
| Extension system | Needs different approach in Go | ~2,600 |
| Package manager | npm/git specific | 1,730 |
| HTML export | Low priority | ~650 |
| Migrations | TS-specific data migration | 295 |
