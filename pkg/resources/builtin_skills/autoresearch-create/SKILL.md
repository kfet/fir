---
name: autoresearch-create
description: Optimise a measurable metric via autonomous experiment loop — writes a project-specific benchmark, proposes hypotheses, runs experiments in disposable worktrees, keeps wins, reverts losses.
builtin: true
override: true
---

# Autoresearch — Autonomous Experiment Loop

Requires the `autoresearch` builtin extension (`run_experiment`, `log_experiment` tools).

## Phase 0 — Gather Context

Ask (or infer) the following; confirm before proceeding:

| Field | Example |
|---|---|
| Optimisation goal | "Make the test suite faster" |
| Primary metric name | `test_ms` |
| Direction | **minimise** or **maximise** |
| In-scope files/dirs | `pkg/parser/`, `pkg/core/` |
| Hard constraints (optional) | "Don't change the public API" |
| Target (optional) | `test_ms < 800` — stop when reached |

## Phase 1 — Setup

### 1a. Isolate work

Use a dedicated git worktree or branch (e.g. `autoresearch/<short-slug>`). Skip if already in one.

### 1b. Write `autoresearch_bench.sh`

Create a **project-specific** benchmark script at repo root. Inspect the project's language, build system, and what you're measuring, then write the simplest shell script that runs the relevant command and emits the metric.

**Requirements:**
- Emit one or more `METRIC name=value` lines on stdout (`name` matches Phase 0, `value` is numeric).
- Exit 0 on success, non-zero on failure.
- Fast enough to run repeatedly (ideally < 60 s).

**Patterns by measurement type:**
- **Time** — wrap the command with epoch-millis: `START=$(date +%s%3N)` … `echo "METRIC ms=$((END-START))"`.
- **Size** — build then `stat`/`wc -c` the artifact.
- **Accuracy/score** — run the eval command, parse its output.

**Examples** (adapt, don't copy):

```bash
#!/usr/bin/env bash
# Go test suite timing
START=$(date +%s%3N)
go test ./... -count=1 -timeout 120s > /dev/null 2>&1
END=$(date +%s%3N)
echo "METRIC test_ms=$((END - START))"
```

```bash
#!/usr/bin/env bash
# Rust binary size
cargo build --release 2>/dev/null
SIZE=$(stat -f%z target/release/mybin 2>/dev/null || stat -c%s target/release/mybin)
echo "METRIC binary_bytes=$SIZE"
```

After writing, `chmod +x autoresearch_bench.sh` and run manually — confirm at least one `METRIC` line appears before continuing.

### 1c. Write `autoresearch.md`

Living memory — re-read on every context reset. Use the template in `./autoresearch-template.md`.

### 1d. Run baseline

Call `run_experiment`, then `log_experiment` with `status="baseline"`. Set `best_so_far` to the primary metric value. Record it in `autoresearch.md`.

## Phase 2 — Experiment Loop

Repeat until stopped, target reached, or hypotheses exhausted.

Each experiment runs in a **disposable sub-worktree** branched from the current best. Data files (`autoresearch.md`, `autoresearch.jsonl`) live in the main worktree only.

### A — Pick hypothesis

Read `autoresearch.md` (main worktree). Take the top unchecked item. If empty, generate 3–5 new hypotheses informed by wins and dead ends, append, then pick. Mark in-progress.

### B — Create sub-worktree and make changes

Create a worktree for this experiment (e.g. `git worktree add ../exp-<n> -b exp/<slug>`). Apply changes there — only in-scope files, one hypothesis per experiment.

### C — Commit and run

Commit in the sub-worktree, then run `run_experiment(cwd=<sub-worktree>)`.

On failure (`success` false, non-zero exit, or no METRIC lines): log from the main worktree with `log_experiment(..., status="error")`, mark hypothesis ❌, remove the sub-worktree, continue.

### D — Decide and log

- **Minimise:** keep if `primary_value < best_so_far`.
- **Maximise:** keep if `primary_value > best_so_far`.
- Negligible (|Δ| < 0.5%) → revert.

**Keep:** Cherry-pick or merge the experiment commit into the main worktree's branch. `log_experiment(..., status="keep")` from main worktree. Update `autoresearch.md` wins table and current best. Set `best_so_far = primary_value`. Stop if target reached.

**Revert:** `log_experiment(..., status="revert")` from main worktree. Add to dead ends in `autoresearch.md`.

Always remove the sub-worktree after deciding.

### E — Reflect every 3–5 experiments

Re-read `autoresearch.md`. Identify patterns. Generate fresh hypotheses. Report brief status: experiments run, current best, Δ% vs baseline.

## Phase 3 — Wrap Up

1. Update `autoresearch.md` fully.
2. Report: total experiments, best value vs baseline (absolute + %), top 3 wins, remaining hypotheses.
3. Offer to merge the worktree/branch back.

## Rules

- Never edit `autoresearch.jsonl` — append-only via `log_experiment`.
- Always commit before running an experiment.
- Re-read `autoresearch.md` on every context reset.
- Never modify out-of-scope files without explicit user approval.
- One hypothesis per experiment — compound changes obscure causality.
- Always pass the original `baseline_value` to `log_experiment`, not the current best.
