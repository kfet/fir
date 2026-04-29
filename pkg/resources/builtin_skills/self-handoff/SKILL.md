---
builtin: true
name: self-handoff
description: Write a handoff document and restart with a clean LLM context in the same tmux pane. Use when context is getting large, switching tasks, or explicitly asked to hand off. The new session picks up from the handoff doc. This is a self-handoff — same process, clean slate.
---

# self-handoff

Hand off to a fresh version of yourself with a clean context window.

## When to use

- Context window is filling up (>60-70% used)
- Major task boundary — finished one thing, starting another
- User explicitly asks for a handoff or fresh start
- Long session with accumulated irrelevant context

## How it works

1. Write a handoff document capturing everything the next session needs
2. Use `/new <prompt>` to atomically clear the session and submit the
   handoff instruction as the first message — no race conditions.

## The handoff document

Write a structured markdown file. Choose the location:

- **Temporary** (`/tmp/fir-handoff-<topic>.md`): for quick task
  switches, context resets, or when the state is all in files anyway.
- **Persistent** (`<project>/.fir/handoff.md` or similar): for ongoing
  work where the handoff context should survive reboots.

### Template

```markdown
# Self-Handoff

## Context
- Working directory: /path/to/project
- Branch: wt/feature-name
- What we're building: <one-line summary>

## Completed
- [x] Thing one
- [x] Thing two

## In Progress
- [ ] Current task — describe exactly where you left off
  - File: path/to/file.go, function doThing(), line ~120
  - What's done: ...
  - What's left: ...

## Key Decisions
- Chose X over Y because Z
- User preference: <specific preference noted during session>

## State / Running Services
- Relay running on krpi2one in tmux poe-relay
- Auth expires at ~HH:MM

## Next Steps
1. First thing to do
2. Second thing
3. Then tell user via telegram
```

**Be selective.** Don't dump the entire session. Write what the next
agent actually needs to continue effectively. Think of it as a briefing
for a colleague taking over your shift.

## The command

After writing the handoff doc, send ONE command via tmux:

```bash
tmux send-keys -t "$TMUX_PANE" "/new Read and follow the self-handoff document at /path/to/handoff.md — continue where the previous session left off." Enter
```

This atomically clears the session and submits the handoff instruction
as the first prompt — no race between `/new` and the follow-up message.

## Critical rules

- The `tmux send-keys` bash call MUST be the last tool call. Do not
  call any other tools after it — the session is about to be cleared.
- Write the handoff doc BEFORE sending the keys. If you send `/new`
  first, the doc never gets written.
- Keep the prompt concise — just point to the doc. Don't inline the
  handoff content in the tmux command.
- If not running in tmux (no `$TMUX_PANE`), fall back to telling the
  user: "Context is getting large. Please run `/new` and then ask me
  to read the handoff doc at <path>."
