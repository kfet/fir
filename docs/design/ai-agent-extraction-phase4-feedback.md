# pkg/agent API ergonomics — surfaced during Phase 4 bake-in

This list captures rough edges that showed up while writing the
testable examples in `example_test.go` (the second internal consumer
required by Phase 4 of the extraction plan). Each entry is shaped as:
**what hurts → what we'd change**.

These are *not* fixed in Phase 4. Phase 4's whole point is that the
new boundary lives in fir for one release so issues like these can be
collected and judged before they ossify at `kfet/agent v0.1.0`. The
list is the input to Phase 4.5 (an optional API-polish slice) or to
the pre-v0.1.0 prep in Phase 5.

## 1. ~~`WaitForIdle()` blocks instead of returning a channel~~ (done in Phase 4.5)

**Done in Phase 4.5.** Added `func (a *Agent) IdleChan() <-chan struct{}`
exposing the underlying idle channel; `WaitForIdle` is now a thin wrapper
(`<-a.IdleChan()`). The channel starts closed so a never-run agent reads as
idle, and composes with `select` against ctx/timeout.

**Hurts.** Callers cannot `select` against agent idleness alongside
their own cancellation or timeout. They get exactly one thing: a
blocking call that returns when the agent decides it's idle.

```go
a.WaitForIdle() // can't compose with select-ctx.Done()
```

**Change.** Either return the underlying channel —

```go
func (a *Agent) Idle() <-chan struct{}
```

— or, less invasively, add a sibling `IdleChan()` and keep
`WaitForIdle` as a thin convenience that does `<-a.Idle()`.

## 2. ~~Extracting assistant text from `AgentEvent` is non-obvious~~ (done in Phase 4.5)

**Done in Phase 4.5.** Added `func (m *AgentMessage) Text() string` returning
the concatenated assistant text (or "" for non-assistant messages). The verbose
`AsAssistant()`+walk pattern in `example_test.go` now uses it.

**Hurts.** `ev.Message` is `*AgentMessage`, which embeds `core.Message`,
which discriminates by role. To get the assistant's text content you
need `ev.Message.AsAssistant()` and then walk `[]AssistantContent`,
even when the event type is already `EventMessageEnd` and you
*know* it's an assistant message.

```go
am := ev.Message.AsAssistant()
if am == nil { return }
for _, c := range am.Content {
    if c.Text.Text != "" { … }
}
```

**Change.** Either expose a typed `EventMessageEnd` payload (e.g.
`ev.Assistant *AssistantMessage` already-discriminated by the agent
before dispatch) or add a `(AgentMessage).Text() string` convenience
that returns the concatenated assistant text or empty. The first is
the right shape; the second is the smallest viable.

## 3. ~~`SimplePromptOptions` requires `nil` for the no-override case~~ (considered, rejected)

Passing explicit `nil` for "no overrides" is un-Go-ish but the
alternative (two arities) is worse. Documentation emphasises that
nil = use agent defaults, which is the smallest viable fix and has
already been done. Recording the rejection here so the next
reviewer doesn't re-litigate it.

## 4. ~~`AgentOptions.InitialState` is overloaded~~ (done in Phase 4.5)

**Done in Phase 4.5.** Lifted `Model`, `SystemPrompt`, `ThinkingLevel`, and
`Tools` directly onto `AgentOptions`. `InitialState` is retained for bulk
restore (replaying a snapshot) and is layered on top — when both set the same
field, `InitialState` wins.

**Hurts.** The natural way to set the model is
`AgentOptions{Model: m}`, but the field is on `AgentState`, so you
must write:

```go
agent.NewAgent(agent.AgentOptions{
    InitialState: &agent.AgentState{Model: m},
    StreamFn:     myStreamFn,
    ConvertToLLM: agent.DefaultConvertToLLM,
})
```

**Change.** Lift the common state fields (Model, SystemPrompt,
ThinkingLevel, Tools) onto `AgentOptions` directly. Keep
`InitialState` for the rare bulk-restore case (replaying a snapshot).

## 5. ~~`ConvertToLLM` is a required option with one obvious value~~ (done in Phase 4.5)

**Done in Phase 4.5.** `NewAgent` defaults `ConvertToLLM` to
`DefaultConvertToLLM` when the option is nil; callers only set it for exotic
context shaping.

**Hurts.** Every NewAgent call has to set
`ConvertToLLM: agent.DefaultConvertToLLM` or pay with a runtime
error. The agent should default to `DefaultConvertToLLM` and let
callers override only when they need something exotic.

**Change.** Default `ConvertToLLM` to `DefaultConvertToLLM` inside
`NewAgent` when the option is nil.

## 6. `DefaultStreamFn` is a global variable

**Hurts.** Test isolation is fiddly — examples have to save/restore
the global to avoid leaking state across tests. Hosts must wire it
exactly once and pray nothing else touches it.

**Change.** Pre-v0.1.0, decide between (a) keeping the global with
explicit save/restore helpers (`SetDefaultStreamFn(fn) (restore func())`)
and (b) removing the fallback entirely and making `StreamFn` strictly
required on `AgentOptions`. Option (b) is cleaner but breaks every
fir test that omits StreamFn.

---

**Phase 4.5 status.** Keepers #1, #2, #4, and #5 are done (see the
strikethrough sections above). #3 was considered and rejected; #6
(`DefaultStreamFn` global) is deferred to the pre-v0.1.0 prep in Phase 5.
When Phase 5 begins, revisit #6 and finalise the boundary before going
public.
