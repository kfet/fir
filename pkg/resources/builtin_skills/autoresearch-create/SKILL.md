---
name: autoresearch-create
description: Run an autonomous optimisation loop — create a benchmark, propose hypotheses, run experiments, log results, keep wins, revert losses. Use this when asked to optimise speed, size, accuracy, or any measurable metric.
builtin: true
---

# Autoresearch — Autonomous Experiment Loop

Drives a self-directed optimisation loop: propose hypothesis → make changes →
commit → benchmark → log result → keep or revert → repeat.

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

### 1a. Create branch

```bash
git checkout -b autoresearch/<short-slug>
```

### 1b. Write `autoresearch_bench.sh`

Create a **project-specific** benchmark script at repo root. You must write this
fresh for every project — do NOT copy a generic template blindly.

**Requirements:**
- The script must emit one or more lines matching `METRIC name=value` on stdout.
- `name` must match the primary metric agreed in Phase 0.
- `value` must be numeric (integer or float).
- The script must exit 0 on success, non-zero on failure.
- It should be fast enough to run repeatedly (ideally < 60 s).

**How to write it:**
1. Look at the project: What language? What build system? What are you measuring?
2. Write the simplest possible shell script that runs the relevant command and
   extracts/computes the metric.
3. If measuring **time**, wrap the command with epoch-millis math.
4. If measuring **size**, use `stat`, `wc`, or similar after a build step.
5. If measuring **accuracy/score**, run the eval command and parse its output.

**Examples for inspiration** (adapt, don't copy):

```bash
#!/usr/bin/env bash
# Timing a Go test suite
START=$(date +%s%3N)
go test ./... -count=1 -timeout 120s > /dev/null 2>&1
END=$(date +%s%3N)
echo "METRIC test_ms=$((END - START))"
```

```bash
#!/usr/bin/env bash
# Binary size after build
cargo build --release 2>/dev/null
SIZE=$(stat -f%z target/release/mybin 2>/dev/null || stat -c%s target/release/mybin)
echo "METRIC binary_bytes=$SIZE"
```

```bash
#!/usr/bin/env bash
# Python test suite timing + pass rate
START=$(date +%s%3N)
OUTPUT=$(python -m pytest tests/ -q 2>&1)
EXIT=$?
END=$(date +%s%3N)
echo "METRIC test_ms=$((END - START))"
PASSED=$(echo "$OUTPUT" | grep -oP '\d+ passed' | grep -oP '\d+')
TOTAL=$(echo "$OUTPUT" | grep -oP '\d+ (passed|failed)' | grep -oP '\d+' | paste -sd+ | bc)
[ -n "$TOTAL" ] && [ "$TOTAL" -gt 0 ] && echo "METRIC pass_rate=$(echo "scale=2; $PASSED/$TOTAL*100" | bc)"
exit $EXIT
```

```bash
#!/usr/bin/env bash
# JS bundle size
npm run build --silent 2>/dev/null
SIZE=$(wc -c < dist/index.js | tr -d ' ')
echo "METRIC bundle_bytes=$SIZE"
```

After writing the script:

```bash
chmod +x autoresearch_bench.sh
```

Run it manually to confirm at least one `METRIC` line appears before continuing.
If it fails or emits no metrics, fix it before moving on.

### 1c. Write `autoresearch.md`

Living memory — re-read this whenever resuming after a context reset.

Read the template here: `./autoresearch-template.md`

### 1d. Run baseline

```
run_experiment()
log_experiment(
  description="Baseline measurement",
  hypothesis="",
  metrics=<from run_experiment>,
  primary_metric="<name>",
  baseline_value=<primary_value>,   # same value — this IS the baseline
  status="baseline"
)
```

Set `best_so_far = baseline_value`. Update `autoresearch.md` with the baseline value.

## Phase 2 — Experiment Loop

Repeat until stopped, target reached, or hypotheses exhausted.

### A — Pick hypothesis

Read `autoresearch.md`. Take the top unchecked item. If the queue is empty,
generate 3–5 new hypotheses informed by wins and dead ends, append them, then pick.

Mark the chosen hypothesis as in-progress.

### B — Make changes

Edit only in-scope files. One hypothesis per experiment — no bundling.

### C — Commit

```bash
git add -A && git commit -m "exp: <hypothesis summary>"
```

### D — Run experiment

```
result = run_experiment()
```

If `result.success` is false or `exit_code != 0`, or no METRIC lines were produced:

```
log_experiment(..., status="error", notes="<failure description>")
git reset --hard HEAD~1
```

Mark hypothesis ❌ in dead ends. Continue to next hypothesis.

### E — Decide and log

**Maximise:** keep if `primary_value > best_so_far`
**Minimise:** keep if `primary_value < best_so_far`
Negligible change (|Δ| < 0.5%) → revert.

**Keep:**

```
log_experiment(..., status="keep")
```

Update `autoresearch.md`: add to Wins table, update Current Best, mark hypothesis ✅.
Set `best_so_far = primary_value`.
If target reached, stop.

**Revert:**

```bash
git reset --hard HEAD~1
```

```
log_experiment(..., status="revert")
```

Add to Dead Ends in `autoresearch.md`, mark hypothesis ❌.

### F — Reflect every 3–5 experiments

Re-read `autoresearch.md`. Identify patterns. Generate fresh hypotheses. Report
brief status to user: experiments run, current best, Δ% vs baseline.

## Phase 3 — Wrap Up

1. Update `autoresearch.md` fully.
2. Report: total experiments, best value vs baseline (absolute + %), top 3 wins, remaining hypotheses.
3. Offer to merge:
   ```bash
   git merge --ff-only autoresearch/<slug>
   ```

## Rules

- Never edit `autoresearch.jsonl` — it is append-only.
- Always commit before running an experiment.
- Always re-read `autoresearch.md` when resuming after a context reset.
- Never modify out-of-scope files without explicit user approval.
- One hypothesis per experiment — compound changes obscure causality.
- Always pass the original `baseline_value` to `log_experiment`, not the current best.
