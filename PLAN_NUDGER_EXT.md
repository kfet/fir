# Plan: Extract Plan Nudger as Extension

## Goal

Move the plan nudger out of core Go code and into a built-in Python extension.
The plan tool, types, persistence, TUI rendering, and ACP integration all stay
exactly where they are — strongly typed, in-process. Only the nudger (behavioral
policy: "remind the model to update its plan") moves out.

## Why

The nudger is orthogonal to plan semantics. It's behavioral policy that changes
independently (interval tuning, smarter logic). Today it's wired through
`AgentSession`, `PlanNudger`, and `NewPlanTool` — touching core code for what is
essentially a timer + steering message. As an extension it becomes a single
Python file with zero Go recompilation for policy changes.

## Context

### Current nudger footprint in Go

1. **`pkg/agent/tools/plan.go`** — `PlanNudger` struct (~45 lines): counter,
   mutex, `RecordTurn()`, `RecordPlanUpdate()`, `Check()`,
   `DefaultPlanNudgeInterval`
2. **`pkg/core/agentsession.go`** — wiring (~18 lines):
   - Field: `planNudger *tools.PlanNudger` (line 233)
   - Construction: `NewPlanNudger(interval, s.hasActivePlan)` (line 252)
   - Event handler: on `EventTurnEnd`, call `RecordTurn()` + `Check()` + `Steer()` (lines 472-478)
   - Method: `hasActivePlan()` — iterates plan entries checking for non-completed (lines 351-360)
   - Tool wiring: `NewPlanTool(s, s.planNudger)` passes nudger in (line 1617)
3. **`pkg/agent/tools/plan.go`** — in `NewPlanTool` Execute func:
   `nudger.RecordPlanUpdate()` called on each plan update (line 139)

### Extension capabilities needed

The nudger needs to:
- Know when a turn ends → `turn_end` event (already emitted to extensions)
- Know when the plan changes and whether it has incomplete entries → **`session_update` event needs to be added** (carries plan counts alongside other session state)
- Inject a steering message → `send_message` with `deliver_as: "steer"` — **bridge needs to accept `deliver_as` option**

## Steps

### Step 1: Add `deliver_as` support to `send_message` bridge call

**Files:**
- `pkg/extension/bridge.go` — parse `deliver_as` and `trigger_turn` from
  `send_message` params, pass as `SendMessageOptions` instead of `nil`
- `pkg/extension/sdk/python/fir_ext.py` — add `deliver_as` and `trigger_turn`
  kwargs to `Context.send_message()`
- `pkg/extension/bridge_test.go` — test that `deliver_as` is forwarded

The `send_message` handler (bridge.go ~line 136) currently discards options:
```go
api.SendMessage(CustomMessageSpec{...}, nil)  // nil opts
```

Change to parse and forward:
```go
api.SendMessage(CustomMessageSpec{...}, &SendMessageOptions{
    DeliverAs:   p.DeliverAs,
    TriggerTurn: p.TriggerTurn,
})
```

Similarly for `send_user_message` — add `deliver_as` parsing.

Update the Python SDK `send_message` signature:
```python
def send_message(self, custom_type, content, *, display=False, deliver_as=None, trigger_turn=False):
```

### Step 2: Emit `session_update` event to extensions

Plan state is session state. Rather than a plan-specific event, emit a generic
`session_update` event whenever session state changes. Today that means
`plan_update` and `session_named`; tomorrow it could include other state.

**Files:**
- `pkg/extension/setup.go` — in the session event subscriber, forward
  session-level state changes as a `session_update` event carrying the current
  session snapshot

In `setup.go`, the subscriber already has a branch for session-level events
(where `event.AgentEvent == nil`). Currently it only handles `session_named`.
Add `plan_update` to that branch, and emit a single `session_update` event
that includes everything an extension might need:

```go
case "plan_update", "session_named":
    mgr.EmitEvent("session_update", map[string]any{
        "type":         event.Type,
        "session_name": event.SessionName,
        "plan": map[string]any{
            "total":     len(event.PlanEntries),
            "completed": countCompleted(event.PlanEntries),
        },
    })
```

Keep the existing `session_named` event emission too for backward compat — the
new `session_update` is additive.

The nudger only cares about `plan.total` and `plan.completed` from this payload.

### Step 3: Create the nudger extension

**File:** `.fir/extensions/plan_nudger.py`

```python
#!/usr/bin/env python3
# ---
# name: plan-nudger
# description: Remind the agent to update its plan periodically
# builtin: true
# ---
import fir_ext

NUDGE_INTERVAL = 5
turns_since_update = 0
has_active_plan = False

@fir_ext.on("session_update")
def on_session_update(params, ctx):
    global turns_since_update, has_active_plan
    plan = params.get("plan", {})
    total = plan.get("total", 0)
    completed = plan.get("completed", 0)
    if total > 0:
        turns_since_update = 0
        has_active_plan = total > completed
    # plan cleared (total == 0) → stop nudging
    if total == 0:
        has_active_plan = False

@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    global turns_since_update
    turns_since_update += 1
    if turns_since_update >= NUDGE_INTERVAL and has_active_plan:
        turns_since_update = 0
        ctx.send_message(
            "nudge", 
            "Reminder: update your plan to reflect current progress.",
            deliver_as="steer",
        )

fir_ext.run(name="plan-nudger")
```

### Step 4: Remove nudger from Go code

**Files to edit:**

1. **`pkg/agent/tools/plan.go`** — delete:
   - `DefaultPlanNudgeInterval` const
   - `PlanNudger` struct and all its methods (`NewPlanNudger`, `RecordTurn`,
     `RecordPlanUpdate`, `Check`)
   - Remove `nudger *PlanNudger` param from `NewPlanTool`
   - Remove `nudger.RecordPlanUpdate()` call inside Execute func
   - Signature becomes: `func NewPlanTool(session PlanUpdater) agent.AgentTool`

2. **`pkg/core/agentsession.go`** — delete:
   - Field `planNudger *tools.PlanNudger` (line 233)
   - Construction `s.planNudger = tools.NewPlanNudger(...)` (line 252)
   - Turn-end nudge block in event handler (lines 472-478)
   - `hasActivePlan()` method (lines 351-360)
   - Change `tools.NewPlanTool(s, s.planNudger)` → `tools.NewPlanTool(s)` (line 1617)

3. **`pkg/agent/tools/plan_test.go`** — remove any nudger-specific tests (keep
   plan tool tests)

### Step 5: Tests

- **Extension tests:** Verify the nudger extension starts, responds to
  `plan_update` and `turn_end` events correctly. Can be a simple unit test of
  the Python logic or an integration test via the extension test harness.
- **Bridge tests:** Test that `send_message` with `deliver_as: "steer"` properly
  forwards the option. Test in `pkg/extension/bridge_test.go`.
- **Plan tool tests:** Verify `NewPlanTool` still works without a nudger param.
  Existing tests in `pkg/agent/tools/plan_test.go` should be updated and still pass.
- **`make all`** must pass.

### Step 6: Changelog

Add under `## [Unreleased]` in `CHANGELOG.md`:

```
### Changed
- Plan nudger extracted from core into built-in extension (`plan_nudger.py`)

### Added
- Extensions can now use `deliver_as` option in `send_message` and `send_user_message`
- New `session_update` event emitted to extensions on session state changes (plan updates, naming)
```

## Order of operations

Do steps 1 and 2 first (prerequisites — extend extension API). Then step 3
(create extension). Then step 4 (remove Go nudger code). Then step 5 (tests)
throughout. Step 6 last.

Run `make all` after each step to ensure nothing breaks incrementally.

## What does NOT change

- `pkg/agent/plan.go` — PlanEntry types, validation (untouched)
- `pkg/agent/tools/plan.go` — tool definition, param parsing (simplified, not moved)
- `pkg/core/agentsession.go` — plan state, `UpdatePlan()`, `PlanEntries()`,
  `restorePlan()`, event emission (all stay)
- `pkg/session/session.go` — plan persistence, restore (untouched)
- `pkg/modes/interactive/components/plan.go` — TUI rendering (untouched)
- `pkg/modes/interactive/mode.go` — TUI event handling (untouched)
- `pkg/modes/acp/plan.go` — ACP adapter (untouched)
- `pkg/modes/acp/acp.go` — ACP plan_update handling (untouched)
