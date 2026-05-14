# Reliable self-handoff — design proposal

## Status quo

The current `.fir/skills/self-handoff/SKILL.md` instructs the agent to:

1. Write a handoff doc somewhere on disk (typically `/tmp/fir-handoff-<topic>.md`).
2. As the LAST tool call, invoke
   `tmux send-keys -t "$TMUX_PANE" "/new Read and follow ..." Enter`
   to atomically clear the session and submit the handoff prompt as the
   first message of a fresh context.

The `/new <prompt>` slash command itself (in `pkg/modes/interactive/commands.go`,
`handleClearCommand`) is genuinely atomic: it aborts the current stream, calls
`session.NewSessionCmd()`, clears UI containers, and submits the prompt via
`session.Prompt(prompt)` in a single goroutine. That part is solid. The
unreliability is entirely in the **delivery channel** (tmux send-keys) and the
**ordering relative to the in-flight LLM turn**.

## Failure modes observed / inferred

1. **`$TMUX_PANE` not set or stale.** When fir is launched outside tmux, or
   inside a tmux session that the agent has lost track of, send-keys either
   fails or — worse — targets the wrong pane. A handoff doc gets written and
   abandoned with no restart.
2. **Editor not empty.** If the user (or a prior tool result) left text in the
   prompt buffer, `tmux send-keys "/new ..."` prepends to that text. The
   resulting line does not start with `/` and so is not recognised as a slash
   command — it gets sent to the LLM as a regular message. The session is *not*
   cleared. Worst case: the original 90% full context now also has the entire
   handoff prompt added to it.
3. **Quoting / escaping hazards.** The prompt path or summary is interpolated
   into a shell string. Newlines, double quotes, backticks, `$` in the path or
   prompt body break the command silently or get interpreted.
4. **Race with the in-flight turn.** `tmux send-keys` runs synchronously, but
   the LLM turn that issued it is still streaming. fir's input pipeline may
   queue the keystroke as "user typed something while the agent is busy".
   Different code paths handle this differently across modes; in some failure
   states (e.g. modal selector open, autocompletion popup, `/` triggers a
   different completer) the keys are consumed by the wrong handler.
5. **Tool-call ordering.** The skill says send-keys "MUST be the last tool
   call". But the tool-result for that bash call still gets recorded; the LLM
   may then emit a final assistant message before fir actually processes the
   queued `/new`. That assistant message is now part of the *new* session's
   pre-context if delivery slips, or just wasted tokens before the clear lands.
6. **No pre-check that the doc exists.** If the Write tool failed silently, or
   the path was mistyped, the handoff prompt points to a non-existent file. The
   new session then has no context to recover from.
7. **No first-class affordance.** The mechanism only works in interactive +
   tmux. ACP, headless `fir -p`, and any mode without a TTY-mux can't use it,
   even though they all have the same underlying `/new <prompt>` capability.
8. **Discoverability.** It's a skill that depends on bash + tmux + the agent
   reading the rules carefully. There is no single typed surface that the
   agent can call and that fails loudly when misused.

## Options considered

### A. Improved skill (still tmux send-keys)

Tighten the instructions: detect `$TMUX_PANE`, explicitly clear the editor
first with `C-u`, refuse to handoff if not in tmux, etc. This shrinks failure
modes 1, 2 and partially 3, but **none of 4–7** can be fixed at the skill
layer. We still depend on tmux entirely. Rejected.

### B. Pure core change (new slash command / agent verb)

Add e.g. `/handoff <doc-path>` to interactive mode. Same machinery as `/new`
but with a small wrapper that validates the doc first. Still fundamentally
requires the agent to be able to *submit a slash command* from within a tool
call — which puts us back in the tmux-send-keys problem. We could instead
expose it as a tool, but tool registration lives in core then. Per AGENTS.md,
"only put logic inside [core] when … skills/extensions cannot express" it.
Most of this work fits naturally in an extension; only the *one* primitive
(clear-and-prompt from inside a tool call) needs to be exposed.

### C. Extension + minimal core primitive (chosen)

Add a single new bridge RPC, `restart_session`, that any extension can call.
It accepts `{prompt: string}` and:

1. Aborts the in-flight stream (which is the very turn currently executing
   the tool that called us).
2. Calls `session.NewSessionCmd()` — clears LLM history, plan, system prompt,
   re-emits `session_named ""`.
3. Asks the mode to clear its UI surfaces (interactive: message/activity/
   command-status containers + footer invalidate + render).
4. Submits `prompt` via `session.Prompt(prompt)` so the new session opens
   with the handoff instruction as its first user message.

Steps 1–2 + 4 are mode-agnostic and live in `SessionBridge`. Step 3 is
mode-specific and is supplied by the mode through a callback, mirroring the
existing `Manager.SetNotifyFn` / `SetSetStatusFn` plumbing. Modes that don't
register a callback simply skip the UI clear (still safe — the next render
pass rebuilds from session state, which is empty after `NewSessionCmd`).

On top of that primitive, ship a builtin extension `handoff.py` that
registers exactly one tool:

```
self_handoff(content: str)
```

Behaviour (the final shape, after the iterations captured in the
"Refinements" section below):

* Validates `content` (string, ≥200 chars after strip, ≥3 non-blank
  lines, ≤64 KB). Validation failures return a regular tool error and
  the session continues — restart only fires once content is known good.
* Writes the content atomically to
  `<cwd>/.fir/handoff-<YYYYMMDD-HHMMSS>.md`.
* Verifies the just-written file is readable and non-empty (catches
  full-disk / permissions / external-rm races).
* Calls `ctx.restart_session(prompt)` with a fixed-template prompt
  pointing the new session at the on-disk path.

Failure modes solved:

| # | Mode                         | Solved by                                               |
|---|------------------------------|---------------------------------------------------------|
| 1 | `$TMUX_PANE` missing/stale   | No tmux dependency at all                               |
| 2 | Editor not empty             | Restart goes through `Prompt()`, never the editor       |
| 3 | Quoting/escaping             | JSON-RPC string parameter, no shell                     |
| 4 | Race with in-flight turn     | `Agent.Abort()` + `WaitForIdle()` happen in-process     |
| 5 | Tool ordering                | The aborting goroutine cancels any further tool/llm work|
| 6 | Doc doesn't exist            | Pre-validated before restart fires                      |
| 7 | Other modes                  | Any mode that registers the callback gets it for free   |
| 8 | Discoverability              | First-class typed tool with a parameter schema          |

### D. Tool-result-side effect (rejected)

Have the existing `/new <prompt>` slash command be invoked by the agent
through a tool that simply returns a special "magic" result that fir
intercepts. Too implicit; couples tool-result rendering to control flow.

## Refinements after self-critique and user iteration

- **Abort first, then everything else.** `restart_session` synchronously
  calls `Agent.Abort()` on the bridge goroutine before returning the RPC
  response. The remaining work (`WaitForIdle` → UI clear → `NewSessionCmd`
  → `Prompt`) runs on a separate goroutine.
- **One tool, one parameter.** Earlier shapes had `new(prompt)`,
  `path` / `summary` parameters, and a two-tool composition with
  `ctx.call_tool`. All cut. `new` was a footgun — wiping context with no
  on-disk artifact has no legitimate agent-driven use case (compaction
  has `/compact`; tabula rasa is a user concern). `summary` was redundant
  with the doc's own heading. `path` invited the "agent forgot to Write"
  failure mode. Final surface: `self_handoff(content: string)`. The tool
  owns the file location; the agent owns the body.
- **Strict content validation.** Rejects content shorter than 200 chars
  (after strip), longer than 64 KB, or with fewer than 3 non-blank lines.
  Schema enforces `minLength`/`maxLength`/`required`/`additionalProperties:
  false` so the LLM gets upfront feedback; the handler defends in depth
  with explicit isinstance + structure checks. Validation failures return
  a regular tool error and the session continues — restart only fires
  once content is known good.
- **Restart prompt is a fixed template** — no agent input. The new agent's
  first action is a `Read` on the path; the doc itself is the briefing.
- **Hard error in unsupported modes.** When no `RestartFunc` is registered
  (ACP, headless) the bridge returns a JSON-RPC error so the agent sees a
  loud failure instead of a silent no-op.

## Chosen plan

Go with C. Concretely:

1. **Core (small):**
   * `pkg/extension/api.go`: add `RestartSession(prompt string) error` to
     `BridgeAPI`.
   * `pkg/extension/bridge.go`: dispatch `restart_session` RPC.
   * `pkg/extension/types.go`: `restartSessionParams{Prompt string}`.
   * `pkg/extension/session_bridge.go`: implement; calls a mode-supplied
     `RestartFunc` if set, else does the mode-agnostic minimum
     (Abort+WaitForIdle → NewSessionCmd → Prompt).
   * `pkg/extension/manager.go` + `setup.go`: add `RestartFunc`
     plumbing parallel to `SetStatusFunc`.
   * `pkg/modes/interactive/mode.go`: register a callback that calls a new
     thin method `m.handleHandoff(prompt)` which mirrors `handleClearCommand`
     (UI clears + `NewSessionCmd` + `Prompt`).
   * `pkg/extension/sdk/python/fir_ext.py`: add
     `BridgeContext.restart_session(prompt: str)`.

2. **Builtin extension:**
   * `pkg/resources/builtin_extensions/handoff.py` — extension name
     `handoff`, one tool `self_handoff(content)`. Validates, writes the
     doc atomically, calls `ctx.restart_session(prompt)`.

3. **Skill:**
   * Delete `pkg/resources/builtin_skills/self-handoff/SKILL.md`. The
     tool description carries everything operational; the doc body is
     left to the agent (no template).

4. **Tests:**
   * `pkg/extension/bridge_test.go`: `TestBridge_RestartSession` confirms
     the RPC reaches `BridgeAPI.RestartSession` with the right prompt.
   * `pkg/resources/testdata/handoff_test.py`: covers content validation
     (type, length min/max, line count), default-path absoluteness,
     atomic write + verify_readable, and end-to-end with a stub ctx.
   * `pkg/extension/sdk/python/demo_ext_test.py`: a guarded `restart_demo`
     tool exercises the SDK helper round-trip via FakeFir.

5. **Docs / housekeeping:**
   * `docs/extension-protocol.md` — document `restart_session`.
   * `pkg/resources/builtin_extensions/demo.py` + matching test — exercise it.
   * Module docstring in `fir_ext.py` updated.
   * `CHANGELOG.md` `## [Unreleased] / ### Added` entry.

## Revision: no-file handoff

A later iteration removed the on-disk handoff artifact entirely. The
file at `<cwd>/.fir/handoff-<ts>.md` was polluting repos that did not
gitignore `.fir/`, and the design's "the doc itself is the briefing"
property is satisfied just as well by carrying the briefing inside the
new session's own conversation log.

The `restart_session` RPC grew an optional `prepend_context` field. When
non-empty, the active mode's restart callback calls
`AgentSession.PrependContext(prepend_context)` between `NewSessionCmd`
and `Prompt`, injecting the briefing as a `[SYS_EXT]`-wrapped user
message ahead of the fixed-template prompt. `handoff.py` shrank
accordingly — no more `_default_path`, `_atomic_write`, `_verify_readable`.

In the same change, `AgentSession.PrependContext` was fixed to *always*
inject. The SYS_EXT setting governs whether the static "[SYS_EXT] is
authoritative" hook line is rendered into the system prompt — i.e. how
already-injected messages are *interpreted* on the next render. It is
no longer a silent gate at injection time. Flipping the setting mid-
session naturally re-interprets all previously injected `[SYS_EXT]`
blocks; we never silently drop content.

Net effect:

- Zero filesystem pollution.
- Briefing persists in the new session's jsonl, durable like any other
  message.
- `[SYS_EXT]` framing makes the briefing authoritative when the setting
  is on, advisory when off — but always present.
- `handoff.py` is ~30 lines lighter; no temp file dance.
