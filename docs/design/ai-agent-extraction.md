# AI / Agent extraction plan

Status: **Phases 1, 2, 3, and 3.5 shipped; Phase 4 underway (in-tree bake-in); Phases 4.5 and 5 await one fir release.**
Owner: kfet.

This document tracks the multi-phase refactor that carves a portable,
model-agnostic Go coding-agent runtime out of fir. The refactor itself
improves fir's architecture even if no external extraction ever ships;
extraction is the *option* the refactor creates.

## Goal

Two new external repos, eventually:

- `github.com/kfet/ai` — portable AI primitives. One module, types at the
  root, specialisations in subpackages (`anthropic/`, `openai/`,
  `gemini/`, `ratelimit/`, `overflow/`, `jsonparse/`, …). Pattern
  mirrors `golang.org/x/oauth2`.
- `github.com/kfet/agent` — the agent runtime: `Agent`, `AgentLoop`,
  `AgentTool`, `ToolSet`, plus a `tools/` subpackage with the standard
  coding toolbox (bash, read, write, edit, editdiff, find, grep,
  imageresize, plan).

`kfet/agent` imports only `kfet/ai`, stdlib, and `kfet/pinexec`.

## What stays in fir

Everything that makes fir a product, not a library:

- TUI (`pkg/tui/`), modes (`pkg/modes/`).
- Session store, sidecars, compaction (`pkg/session/...`).
- Extension host (`pkg/extension/...`).
- MCP runtime (`pkg/mcp/...`).
- Provider catalog (`pkg/ai/models_generated.go`, `pkg/ai/providers/...`),
  config (`pkg/config`), auth (`pkg/auth`), models (`pkg/models`).
- All resources (`pkg/resources/...`).

## Dependency rules (enforced by tests)

Direction of allowed imports under `pkg/`:

```
pkg/agent          ←  pkg/agent/tools  ←  fir-side adapters
pkg/agent          ←  pkg/session/*    (store may import agent)
pkg/ai             ←  pkg/agent
```

Forbidden edges, asserted by `TestForbiddenImports` in `pkg/agent/`:
`pkg/agent` and `pkg/agent/tools` must not import any of
`pkg/session`, `pkg/mcp`, `pkg/extension`, `pkg/tui`, `pkg/config`,
`pkg/auth`, `pkg/modes`, `pkg/resources`, `pkg/models`.

`pkg/log` is forbidden from Phase 3 onward. `pkg/ai` remains allowed
only because `pkg/agent/clamp.go` and `pkg/agent/agent.go` still call
four fir-policy helpers — Phase 3.5 closes that gap, after which
`pkg/ai` joins the forbidden set too.

## Pre-flight (Phase 0)

Every `pkg/agent/*.go` carries a `// Ported from packages/agent/src/*.ts`
header. Before any code can be published externally, the upstream
license must be verified and the headers reconciled:

- If the upstream license permits re-licensing under whatever we choose
  for `kfet/agent`, phases 1–5 proceed as written.
- If it forbids re-licensing, phases 1–4 still ship (the architectural
  cleanup is valuable to fir on its own). Phase 5 then becomes a
  clean-room rewrite using the in-tree-validated API, deferred to its
  own design doc.

Phase 0 is the **only** publication blocker; it does not gate the
in-tree refactor.

## Phased execution

The *implementation* order deviates from the *strategic* order. We
ship the smallest, lowest-risk cut first so the harder type-rewiring in
Phase 2 lands on a clean tools boundary.

### Phase 1 — Decouple `pkg/agent/tools` from `pkg/session/store`

Smallest viable slice. Removes the only forbidden import inside
`pkg/agent/tools` today.

Concrete edits:

1. Replace `PlanUpdater` (which exposed `Observables() *store.ObservableStore`)
   with a smaller `PlanSink` interface:

   ```go
   type PlanSink interface {
       UpdatePlan(title string, entries []agent.PlanEntry, metadata map[string]string)
   }
   ```

2. Add a functional `CardPublisher` option for the fir-specific
   observable-card publishing:

   ```go
   type CardPublisher func(title string, entries []agent.PlanEntry,
       metadata map[string]string, entryID string)
   ```

   The publisher may be `nil` — the plan tool is silent when no
   publisher is provided. No optional-interface type-assertion (silent
   feature loss).

3. New signature: `func NewPlanTool(sink PlanSink, publisher CardPublisher) agent.AgentTool`.

4. Move `publishPlanCard`, `planSlug`, `planDetail` out of
   `pkg/agent/tools/` into a fir-side adapter at
   `pkg/session/plancard.go`. Behaviour is byte-identical.

5. Update `pkg/session/agentsession.go RegisterSessionTools` to
   construct a `CardPublisher` closure that calls the relocated
   helpers and writes to `s.Observables()`.

6. New test `pkg/agent/forbidden_imports_test.go` that calls
   `go list -f '{{join .Imports "\n"}}' ./pkg/agent/...` and fails if
   any forbidden path appears. Keeps the boundary from eroding.

Acceptance:

- `go list -f '{{join .Imports "\n"}}' ./pkg/agent/tools | grep pkg/session/store`
  returns nothing.
- `make all` green.
- Observable card behaviour byte-identical (existing tests pass).
- Plan-tool behaviour byte-identical (existing tests, adapted to the
  new constructor, pass).

Rollback: revert the interface, restore the direct
`Observables()` method on `PlanUpdater`, drop the publisher closure
and the relocated helpers, drop the forbidden-imports test.

### Phase 2 — Split portable types out of `pkg/ai`

Carve `pkg/ai` into:

- Portable types: `Message`, `Tool`, `Usage`, `Context`, `Provider`,
  `AssistantMessageEvent*`, `StreamFunction`, `Model` (shape only — the
  generated catalog stays in fir).
- Fir-resident: `models_generated.go`, `provider_registry_builtins.go`,
  the OAuth registry helpers wired to specific fir providers, `Registry`
  (the API transport registry with its extension-source-tracking
  policy), `stream.go` (uses `Registry`).

**Implementation chosen (Phase 2 part 1):** subpackage move with
re-export aliases.

- `pkg/ai/types.go` and `pkg/ai/eventstream.go` moved verbatim to
  `pkg/ai/core/` (package renamed `ai → core`).
- `pkg/ai/aliases.go` re-exports every public symbol from core via
  `type X = core.X`, `var X = core.X`, `const X = core.X`. The 150+
  existing call sites see no change.
- `pkg/ai/stream.go`, `registry.go`, `models.go`, `models_generated.go`,
  `oauthreg.go`, `provider_registry*.go` stay in `pkg/ai` and continue
  to reference types by their alias names.

`pkg/agent` and `pkg/agent/tools` still import `pkg/ai` after Phase 2
part 1; switching them to `pkg/ai/core` directly is Phase 2 part 2 (or
folded into Phase 3 alongside the `log/slog` rebase).

Acceptance: `pkg/ai/core` builds standalone with no fir-side imports;
all in-tree tests pass unchanged.

Rollback: revert the move; re-merge `pkg/ai/aliases.go` content back
into `types.go`/`eventstream.go`.

### Phase 3 — Rebase `pkg/agent` onto portable AI + `log/slog`

Drop the `pkg/log` import from `pkg/agent` and `pkg/agent/tools`. Use
`log/slog` directly with package-level handler hooks if fir needs to
override.

Acceptance: forbidden-imports test extended to forbid `pkg/ai` (fir
catalog/policy surface) and `pkg/log`; only the portable subset
import remains.

**Status: shipped except for the four fir-policy hooks.** `pkg/log` is
removed; the forbidden-imports test now bans it. `pkg/agent/tools`
imports only `pkg/ai/core` plus `log/slog`. `pkg/agent` still imports
`pkg/ai` for four fir-policy helpers in two files:

- `pkg/agent/clamp.go` uses `ai.SupportsXhigh` and `ai.SupportsMax`
  inside `AvailableThinkingLevelsForModel`. Those helpers encode
  hardcoded knowledge of specific model IDs (gpt-5.2, opus-4-7, etc.)
  and belong on the fir side.
- `pkg/agent/agent.go` uses `ai.StreamSimple` and `ai.DefaultRegistry`
  inside the default-StreamFn closure that runs when callers leave
  `AgentOptions.StreamFn` and `SimplePromptOptions.StreamFn` nil.

Phase 3.5 finishes the job: move `AvailableThinkingLevelsForModel`
fir-side, replace the default StreamFn with an explicit-or-injectable
hook, then add `pkg/ai` to the forbidden-imports list.

### Phase 3.5 — Eliminate residual `pkg/ai` coupling

Two cuts, both shipped:

1. `AvailableThinkingLevelsForModel` moved out of `pkg/agent/clamp.go`
   into `pkg/session/thinkinglevels.go`. The agent keeps the canonical
   ladder plus `IsCanonicalThinkingLevel` and `ClampThinkingLevel`; the
   host computes the available set for any `*core.Model`. Two callers
   (`cmd/fir/app.go` and `pkg/session/agentsession.go`) updated to use
   the session-side helper. Tests moved alongside.

2. The default StreamFn closure in `pkg/agent/agent.go` removed.
   Replaced by `agent.DefaultStreamFn func(ctx context.Context) StreamFn`,
   a package-level factory hook. When a per-call StreamFn is nil and
   `DefaultStreamFn` is also nil, the agent surfaces a clear
   "no stream function configured" error via the agent state.
   `pkg/session/defaultstream.go` installs the fir-side default
   (calling `ai.StreamSimple` against `ai.DefaultRegistry`) in its
   `init()`, so existing fir call sites keep working without changing
   their `AgentOptions`.

After these cuts, `pkg/agent` and `pkg/agent/tools` import only
`pkg/ai/core`, `log/slog`, and stdlib. The forbidden-imports test
gained `pkg/ai` to the banned list (with an `allowedPaths` exemption
for the `pkg/ai/core` subpackage that the prefix-match would otherwise
catch).

### Phase 4 — Bake the boundary in-tree

Live with the new shape for one fir release. Build one internal
second consumer (e.g. a tiny non-interactive CLI that drives
`pkg/agent` for batch jobs) to validate the API outside fir's product
flows.

No external extraction yet. The goal is to find ergonomics issues now,
not after a `v0.1.0` tag freezes them.

**Status: started.** `pkg/agent/doc.go` adds a package-level overview;
`pkg/agent/example_test.go` provides the second internal consumer in
the form of testable examples (`Example`, `ExampleAgent_SimplePrompt`,
`ExampleDefaultStreamFn`, `ExampleClampThinkingLevel`). They drive the
public API with a fake StreamFn and zero fir-side imports, proving
the boundary is self-sufficient.

Ergonomic friction surfaced while writing the examples is captured in
`docs/design/ai-agent-extraction-phase4-feedback.md` for later review.
No API changes land in this phase — that is the explicit point of
baking the boundary in-tree.

### Phase 4.5 — API polish (optional, pre-extraction)

After Phase 4's bake-in, group the keepers from
`docs/design/ai-agent-extraction-phase4-feedback.md` into a single
API-polish commit. Each entry there records what hurt and what'd
change; not every entry will earn a fix, and a "considered and
rejected" section at the bottom documents the negative decisions so
future reviewers don't re-litigate them.

The slice ships before Phase 5 starts. Phase 5 is supposed to be
mechanical extraction; mixing API churn into it would muddy the
boundary between "what fir already lives with" and "what external
consumers see".

If no entries survive review, Phase 4.5 is a no-op and we go straight
to Phase 5.

### Phase 5 — Extract

When Phase 0 has cleared and the in-tree boundary has held for at
least one release:

1. Create `github.com/kfet/ai` with the portable types + `ratelimit/`,
   `overflow/`, `jsonparse/`, and an initial provider subpackage.
2. Create `github.com/kfet/agent` depending on `kfet/ai` and
   `kfet/pinexec`.
3. Switch fir to import the external modules; delete the in-tree
   copies.

Sibling repo conventions (`firpty`, `skipstone`, `pinexec`, `pinoauth`)
apply: README, CHANGELOG, Makefile coverage gate, `.covignore`,
testable examples, `doc.go`, strict static checks.

## Decision log

- **Single module `kfet/ai` over flat `kfet/ai-core`** — sibling
  subpackages (`ratelimit`, `overflow`, `jsonparse`, providers) need
  somewhere to land that isn't its own repo. Single module + dead-code
  elimination keeps consumer binaries small. Migration to multi-module
  is reversible if version coupling becomes painful.
- **Functional `CardPublisher` over optional interface** — a tool that
  silently loses functionality based on a type assertion is a debugging
  trap. Explicit nullable function makes the seam visible.
- **Keep the plan tool in `pkg/agent/tools`** — it is a generic agent
  task-tracking primitive. The fir-specific card rendering moves out;
  the tool itself stays portable.
- **Phase order swaps strategic and implementation order** — Phase 1
  (the cheapest cut) lands first so Phase 2's heavier type rewiring
  meets a clean tools boundary, not two entangled problems at once.

## Acceptance / verification

Every phase ends with `make all` green and the forbidden-imports test
asserting the current set of disallowed edges. Each phase has a
documented rollback above.
