# Ship-it workflow + cross-project instruction tuning

Status: design accepted, implementing.

## Problem

After every feature implementation the user manually: (1) runs `review-and-fix`
repeatedly — each pass finds new things; (2) ff-merges the worktree branch to
main; (3) cleans up the worktree. They want this to happen automatically at the
end of a worktree task, and a way to collect feedback on their own instructions
(AGENTS.md, skill files, task prompts) across projects and machines to iterate on
them.

## LLM mechanics that shape the design

A loop expressed only as a static instruction near the top of a skill degrades:
as iterations append diffs, file contents, and tool output, the original loop
spec sits thousands of tokens back. Attention weight is relative and
position-attenuated, so the early instruction loses salience while recent tokens
dominate. Next-token prediction then favours the locally-coherent continuation —
"summarise and conclude" — over re-entering the loop. The loop collapses to a
single pass, and exit conditions are lost.

The fix is **re-injection at a recent position via a tool call**: emit a bash
`echo` of the loop invariant each cycle. This (a) places the invariant at the
position of maximum attention right before the next action, (b) uses the
tool-call/output schema as a structural prime so the best-fitting continuation is
"another iteration", and (c) forces a hard turn boundary that interrupts a glide
into conclusion. The lever is *position + turn boundary + external-looking
tokens* — bash is a convenient vehicle, not the point.

Two rules follow:

- **Exit predicate in full, criteria as slugs.** The exit condition is what
  collapses, so it must be restated in full in the banner each cycle. Review
  criteria are defined in `review-and-fix`; the banner only re-emits their slugs
  (`correctness`, `security`, `simplify`, `test-coverage`, `changelog`, `build`)
  as attention indices that point back to the full definitions. Never duplicate
  the criteria content — duplication drifts and the model averages over copies.
- **Print at the decision point.** The dangerous moment is right after a pass
  looks clean. The banner must be re-emitted immediately before evaluating the
  exit condition, with the predicate as the most salient line.

The existing `loop` skill already uses this mechanic for time-interval loops
(prints a reminder echo each cycle, re-reads itself). The work here generalises
it to condition-driven loops.

## Design

Five skill-level changes. No extension, no core, no sys-prompt or AGENTS.md
additions — the workflow ships with fir's builtin skills and works in any
project.

### 1. `loop` — generalise to two modes

- **interval mode** (existing): repeat every N seconds until told to stop.
- **condition mode** (new): repeat until an exit predicate holds. Each cycle
  prints a banner carrying the loop body as **slugs/pointer** plus the **exit
  predicate in full**, and an incrementing cycle counter as an external state
  anchor. The banner is re-emitted immediately before the exit decision.

### 2. `review-and-fix` — drive its iterate-until-clean via `loop`

Owns the review *content* (start at changed files, fix anything anywhere). Uses
`loop` condition mode for the repeat. Exit predicate: the last pass found zero
new issues. Adds a standalone "Simplify." callout (no checklist — trust the
model). Section headings are exactly the banner slug strings so the banner
indexes them cleanly.

### 3. `ship-it` — orchestration only

`review-and-fix` → ff-merge (`merge-to-main`) → cleanup (only when the task is
tagged final) → friction log. No loop logic of its own; it trusts
`review-and-fix` to loop internally.

Cleanup is gated by an explicit mode tag in the task text — the agent does not
infer it:

- `Mode: do.` → ship, keep worktree (more work coming).
- `Mode: do, final.` → ship and clean up worktree + branch.

Friction log (writer side): if and only if something actively caused trouble
this task, append a one-line JSON entry to a global log. Empty by default —
silence is the signal. No score, no "here are three thoughts."

### 4. `instruction-tune` — the reader side, user-invoked

Reads `~/.config/fir/instruction-feedback.jsonl`, groups entries by target file
(patterns across entries = signal; one-offs = noise), locates each target
(possibly in another project — entries record absolute paths), shows the proposed
edit, applies on consent, archives processed entries to
`instruction-feedback.archive.jsonl`.

Log entry shape:

```json
{
  "ts": "2026-06-01T12:34:56Z",
  "project": "/abs/path/to/project",
  "host": "hostname",
  "branch": "work/feature",
  "target": "AGENTS.md | skills/foo/SKILL.md | task-prompt",
  "issue": "short description",
  "evidence": "what happened that made this friction visible"
}
```

`target: "task-prompt"` entries don't map to a file edit; they aggregate into
patterns for writing better `wt`/`ship-wt` task prompts.

### 5. `ship-wt` — `wt` + ship-it, explicit and opt-in

A named variant of `wt`: spawns a worktree agent whose task text instructs it to
invoke `ship-it` when implementation is complete and `make all` passes. Reuses
`wt`'s `spawn.sh`; appends the ship-it instruction to the task text and carries
the `final` tag through. Plain `wt` stays untouched — no implicit trigger.

## Design rules

- Each skill owns one thing: `loop` = the re-injection mechanic; `review-and-fix`
  = review content + exit predicate; `ship-it` = orchestration; `ship-wt` =
  spawn variant; `instruction-tune` = feedback processing.
- Empty-by-default friction reports.
- Global, cross-project, cross-host feedback log (assumes `~/.config/fir` is
  synced across boxes, else per-box logs merged manually).
- Only `ship-it` writes the log for v1.
- Mode tags are explicit, not inferred.

## Rejected alternatives

- Fold the loop into `review-and-fix` or `merge-to-main` as bespoke logic —
  duplicates the loop mechanic and re-introduces collapse one level up.
- Express the loop as pure static instruction — collapses (see mechanics above).
- Inject the ship-it trigger into plain `wt`'s spawn.sh — too implicit; `ship-wt`
  makes it an opt-in named variant instead.
- Session-end hook / extension — fires when unwanted (discussions, aborted work,
  handoffs); can't judge "is this actually done".
- AGENTS.md rule — works for fir but pollutes every project's instructions; the
  workflow should ship with fir's skills.
- Per-task "rate my instructions" reflection — produces sycophantic noise;
  empty-by-default friction reports replace it.

## Build order

1. `loop` — add condition mode.
2. `review-and-fix` — use `loop`, add Simplify callout, slug headings.
3. `ship-it` — orchestration + friction-log writer.
4. `instruction-tune` — reader/editor.
5. `ship-wt` — spawn variant.
