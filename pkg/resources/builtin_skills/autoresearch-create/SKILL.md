---
name: autoresearch-create
description: Run an autonomous optimisation loop — propose hypotheses, benchmark, log results, keep wins, revert losses. Use this when asked to optimise speed, size, accuracy, or any measurable metric automatically.
builtin: true
---

# Autoresearch — Autonomous Experiment Loop

This skill drives a self-directed optimisation loop. You propose a hypothesis,
make code changes, run a benchmark, log the result, and decide to keep or revert —
repeating until the user stops you or a target is reached.

The `autoresearch` extension must be active (it is a fir builtin). It provides
two tools: `run_experiment` and `log_experiment`.

---

## Phase 0 — Gather Context

Ask the user (or infer from context) the following. Confirm before proceeding.

| Question | Example answer |
|---|---|
| What are we optimising? | "Make the test suite faster" |
| Primary metric name | `test_ms` (lower is better) |
| Optimise direction | **minimise** or **maximise** |
| Which files/dirs are in scope? | `pkg/parser/`, `pkg/core/` |
| Hard constraint (optional) | "Don't change the public API" |
| Target (optional) | "Stop when test_ms < 800" |

---

## Phase 1 — Setup

### 1a. Create a git branch

```
autoresearch/<short-slug>
```

Example: `autoresearch/test-speed`. All experiment commits land here so they are
isolated and easy to review or discard.

### 1b. Write `autoresearch.sh`

Create a benchmark script at the repo root. It **must** output one or more lines:

```
METRIC name=value
```

Example for test speed:

```bash
#!/usr/bin/env bash
set -euo pipefail
START=$(date +%s%3N)
go test ./... -count=1 -timeout 120s > /dev/null 2>&1
END=$(date +%s%3N)
echo "METRIC test_ms=$((END - START))"
```

Make it executable: `chmod +x autoresearch.sh`.

Verify it runs and produces at least one `METRIC` line before proceeding.

### 1c. Write `autoresearch.md`

This is the **living memory** of the experiment loop. Update it after every
experiment. It must survive context window resets — always re-read it when
resuming a loop.

Template:

```markdown
# Autoresearch: <objective>

## Objective
<one-sentence description of what we are optimising and why>

## Primary Metric
`<metric_name>` — **minimise** | **maximise** (baseline: <value>)

## Optimisation Direction
minimise | maximise

## In-Scope Files
- <file or directory>
- <file or directory>

## Hard Constraints
- <constraint>

## Target (optional)
<metric_name> < 800 (stop when reached)

## Hypotheses Queue
- [ ] <idea 1>
- [ ] <idea 2>
- [ ] <idea 3>

## Wins
| # | Description | Δ% | Commit |
|---|---|---|---|

## Dead Ends
| # | Description | Why it failed |
|---|---|---|

## Current Best
Experiment #1 — baseline — `<metric_name>` = <value>
```

### 1d. Run baseline

Call `run_experiment` with no arguments (uses `autoresearch.sh` in cwd).

Call `log_experiment`:
```
description: "Baseline measurement"
hypothesis:  ""
metrics:     <from run_experiment>
primary_metric: "<name>"
baseline_value: <same as primary_value — this IS the baseline>
status:      "baseline"
```

Update `autoresearch.md` with the baseline value.

---

## Phase 2 — Experiment Loop

Repeat until the user says stop, a target is reached, or there are no more
hypotheses to try.

### Step A — Pick a hypothesis

Read `autoresearch.md`. Select the top unchecked hypothesis from the queue.
If the queue is empty, generate 3–5 new hypotheses based on what has been
learned so far (wins and dead ends). Append them before proceeding.

Mark the chosen hypothesis as in-progress in `autoresearch.md`.

### Step B — Make changes

Edit only files within the in-scope list. Do not touch files outside scope
unless there is a strong reason (state it explicitly).

Keep changes focused on the hypothesis. Avoid bundling unrelated improvements
into one experiment — that makes causality unclear.

### Step C — Commit

```bash
git add -A
git commit -m "exp: <short description of hypothesis>"
```

Committing before running the experiment means a bad result can be cleanly
reverted with `git reset --hard HEAD~1`.

### Step D — Run and log

1. Call `run_experiment` — capture `metrics` and `success`.
2. If `success` is false or exit_code is non-zero:
   - Log with `status: "error"`, notes describing the failure.
   - Revert: `git reset --hard HEAD~1`
   - Mark hypothesis as dead end in `autoresearch.md`.
   - Continue to next hypothesis.
3. Call `log_experiment` with all fields filled in, including `baseline_value`
   (use the value from the baseline experiment, not a previous keep).

### Step E — Decide: keep or revert

**Maximise metric** → keep if `primary_value > best_so_far` (strictly better).
**Minimise metric** → keep if `primary_value < best_so_far` (strictly better).

Ties or negligible deltas (<0.5%) → revert (noise, not signal).

**If keeping:**
- Update `autoresearch.md` Wins table.
- Update "Current Best" section.
- Update `best_so_far` (for future comparisons, compare against the new best,
  not the original baseline).
- Mark hypothesis as ✅ in the queue.
- Check whether the target has been reached; if so, stop and report.

**If reverting:**
```bash
git reset --hard HEAD~1
```
- Re-log with `status: "revert"` (update the record if already logged,
  or log a second entry noting the revert).
- Add the hypothesis to the Dead Ends table in `autoresearch.md` with a
  reason.
- Mark hypothesis as ❌ in the queue.

### Step F — Reflect and loop

After every 3–5 experiments, pause and:
1. Re-read `autoresearch.md` in full.
2. Identify patterns in what works and what doesn't.
3. Generate fresh hypotheses informed by those patterns.
4. Report a brief status to the user: experiments run, current best, Δ% vs
   baseline.

Then continue the loop.

---

## Phase 3 — Wrap Up

When stopping (user request, target reached, or hypotheses exhausted):

1. Ensure `autoresearch.md` is fully up to date.
2. Report a final summary:
   - Total experiments run
   - Current best value vs baseline (absolute and %)
   - Top 3 most impactful changes
   - Remaining promising hypotheses (if any)
3. Offer to merge the branch or leave it for the user to review:
   ```bash
   git -C <project_root> merge --ff-only autoresearch/<slug>
   ```

---

## Rules

- **Never** edit `autoresearch.jsonl` — it is append-only.
- **Always** commit before running an experiment.
- **Always** re-read `autoresearch.md` when resuming after a context reset.
- **Never** modify files outside the declared scope without explicit user approval.
- Keep each experiment focused on one hypothesis. Compound experiments are hard
  to interpret.
- If a run produces no `METRIC` lines, treat it as `status: "error"` and revert.
- The baseline is fixed — always use the original baseline value for `baseline_value`
  in `log_experiment`, regardless of how many wins have accumulated.
