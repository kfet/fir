---
builtin: true
name: loop
description: Repeatedly execute a task — on a fixed time interval, or until an exit condition holds. Re-prints the task/exit banner each cycle so the loop does not collapse. Use for watch-style polling or iterate-until-clean loops.
override: true
---

# Loop Skill

You execute a task on repeat. Two modes:

- **interval** — repeat every N seconds until told to stop.
- **condition** — repeat until an exit predicate holds.

## Why the per-cycle banner is mandatory

A loop stated once, at the top, decays as cycles append output: the original
instruction sits far back, recent tokens dominate attention, and the
locally-coherent continuation becomes "summarise and conclude" instead of
"loop again". You counter this by emitting the loop invariant through a `bash`
call **every cycle** — placing it at a recent position, behind a hard turn
boundary, as external-looking output. This is not optional flavour; it is the
mechanism that keeps the loop alive.

## Setup (first run only)

Establish, asking the user only for what is missing:

- **Task** — what to run each cycle.
- **Mode** — interval or condition.
- **Interval** (interval mode) — seconds between cycles (default 30).
- **Exit predicate** (condition mode) — the exact condition that ends the loop,
  stated so it can be evaluated objectively.
- **Banner slugs** (optional) — short keywords pointing back to the fuller task
  definition (e.g. another skill's section headings). Re-emitted each cycle as
  attention anchors; do **not** restate the full task content in the banner.

Then begin immediately.

## Cycle — interval mode

1. **Print the banner** (plain code block, survives session timeout):
   ```
   Next reminder command:
   sleep <N> && echo "=== LOOP CYCLE <N+1> === Task: <task>. Slugs: <slugs>"
   ```
2. **Execute the task** fully — no truncation, no skipped steps.
3. **Report** one line: `Cycle N complete: <summary>`.
4. **Run the reminder command** with a bash timeout of `<N + 10>`s:
   ```bash
   sleep <N> && echo "=== LOOP CYCLE <N+1> === Task: <task>. Slugs: <slugs>"
   ```
   On seeing the output, return to step 1.

## Cycle — condition mode

No sleep. The banner alone re-grounds the loop, so its content carries the load.

1. **Print the banner** via bash, with the exit predicate stated **in full** and
   the cycle counter incremented:
   ```bash
   echo "=== LOOP CYCLE <N> ===
   Task slugs: <slugs>
   EXIT ONLY IF: <exit predicate, in full>. Otherwise run another cycle."
   ```
2. **Execute the task** fully.
3. **Re-print the banner** (same bash echo) immediately before deciding — the
   moment right after a cycle looks done is when premature exit happens, so the
   predicate must be the most salient recent text.
4. **Evaluate the exit predicate.** If it holds, exit and report. Otherwise
   increment N and return to step 1.

## Rules

- **Carry the task verbatim.** Never paraphrase or simplify it between cycles.
- **Exit predicate in full; task as slugs.** The predicate is what collapses, so
  restate it fully each cycle. Slugs only point at the task definition.
- **Report every cycle**, even if the outcome is "nothing changed".
- **Adjust on request** — interval changes or predicate clarifications apply from
  the next cycle.
