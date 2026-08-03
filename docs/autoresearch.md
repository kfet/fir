# fir-autoresearch — Research Notes

## What is pi-autoresearch?

[pi-autoresearch](https://github.com/davebcn87/pi-autoresearch) is an extension +
skill pair for **pi** (Anthropic's internal AI coding agent, similar in concept to
fir). It provides an **autonomous experiment loop**: the agent proposes a hypothesis,
makes code changes, runs a benchmark, records the result, and decides whether to keep
or revert — looping indefinitely.

The design is domain-agnostic. The user supplies a benchmark script that outputs one
or more `METRIC name=number` lines. The agent drives the loop.

---

## Architecture of pi-autoresearch

### Extension (`extensions/pi-autoresearch/index.ts`)

Two tools are registered:

| Tool | Purpose |
|---|---|
| `run_experiment` | Runs `autoresearch_bench.sh`, captures stdout/stderr, parses every `METRIC name=value` line, returns structured metrics + raw output |
| `log_experiment` | Appends a JSONL record to `autoresearch.jsonl` (timestamp, description, metrics, status: `keep`/`revert`/`baseline`) |

Key design choices:
- `METRIC name=value` convention keeps benchmark scripts simple (just `echo`).
- `autoresearch.jsonl` is the **append-only audit trail** — every experiment, win or loss.
- The extension does no decision-making; it is pure infrastructure.

### Skill (`skills/autoresearch-create/SKILL.md`)

Instructions that tell the agent:

1. **Gather context**: ask for optimization goal, benchmark command (or write one),
   and metric name to maximise/minimise.
2. **Create git branch**: `autoresearch/<name>`.
3. **Create `autoresearch.md`** (living document):
   - Objective
   - Scope: which files are in-play
   - Hypotheses queue
   - Wins (changes that improved the metric)
   - Dead ends (tried and failed)
   - Current best metric
4. **Create `autoresearch_bench.sh`**: the benchmark script. Must output `METRIC name=value`
   for each metric.
5. **Run baseline** via `run_experiment`; log via `log_experiment` with `status=baseline`.
6. **Loop** (until told to stop or target reached):
   a. Pick next hypothesis from `autoresearch.md`.
   b. Make code edits.
   c. Commit (so the change is reversible).
   d. `run_experiment` → `log_experiment`.
   e. If improved: update `autoresearch.md` wins + current best; keep commit.
   f. If regressed: `git revert HEAD` (or `git reset --hard HEAD~1`); log `status=revert`.
   g. Generate new hypotheses based on what was learned.

### Key Properties

- **Context-safe**: `autoresearch.md` is the memory that survives context window resets.
  The agent can re-read it to understand what has been tried.
- **Reversible**: every attempt is committed before the experiment runs; bad runs are
  reverted via git.
- **Append-only log**: `autoresearch.jsonl` is never modified, only appended.
- **Domain-agnostic**: works for test speed, bundle size, binary size, accuracy, latency,
  GPU throughput — anything measurable with a shell script.

---

## fir Equivalent Design

### Extension: `.fir/extensions/autoresearch.py`

Python extension using `fir_ext`. Provides:

| Tool | Maps to |
|---|---|
| `run_experiment` | Run benchmark, parse `METRIC name=value`, return metrics dict + `wall_ms` |
| `log_experiment` | Append JSONL to `autoresearch.jsonl` |
| `lock_benchmark` | Freeze `autoresearch_bench.sh`'s sha256 in `autoresearch.lock` |

Additional features over pi-autoresearch:
- `/autoresearch` slash command to show a quick summary of `autoresearch.jsonl`
  (experiment count, current best, last N experiments, wall-time efficiency).
- **Benchmark integrity lock**: `lock_benchmark` records the benchmark's sha256 in the
  campaign root after the baseline; `run_experiment` refuses to run any experiment whose
  `autoresearch_bench.sh` differs, so an experiment cannot rewrite its own benchmark and
  "win". Unlocked campaigns behave exactly as before.

### Skill: `.fir/skills/autoresearch-create/SKILL.md`

Mirrors the pi skill, adapted for fir conventions:
- Uses fir's worktree pattern for git branching.
- References the extension tools by their exact names.
- Tells the agent how to keep `autoresearch.md` updated as the living doc.
- Specifies the loop protocol (hypothesis → edit → commit → run → log → keep/revert).

---

## METRIC Line Convention

Benchmark scripts output lines of the form:

```
METRIC name=value
```

- `name`: alphanumeric + underscore, e.g. `throughput`, `test_ms`, `loss`
- `value`: a float or integer (higher = better by default; the skill prompt lets the
  user specify min/max)

Multiple metrics are allowed. The agent tracks the primary metric specified at setup.

---

## autoresearch.jsonl Schema

Each line is a JSON object:

```json
{
  "timestamp": "2026-03-14T12:00:00Z",
  "experiment": 1,
  "description": "Try smaller batch size",
  "hypothesis": "Smaller batches reduce memory pressure",
  "commit": "abc1234",
  "metrics": {"throughput": 142.3, "loss": 0.42},
  "primary_metric": "throughput",
  "primary_value": 142.3,
  "baseline_value": 120.0,
  "delta_pct": 18.6,
  "wall_ms": 4210.5,
  "status": "keep"
}
```

`status` is one of: `baseline`, `keep`, `revert`, `error`.

---

## autoresearch.md Template

```markdown
# Autoresearch: <objective>

## Objective
<what we are trying to optimise and why>

## Primary Metric
`<metric_name>` — **maximise** (baseline: <baseline_value>)

## In-Scope Files
- <list of files/dirs the agent may modify>

## Hypotheses Queue
- [ ] <next idea>
- [ ] <next idea>

## Wins
| # | Description | Delta | Commit |
|---|---|---|---|
| 1 | ... | +12% | abc1234 |

## Dead Ends
| # | Description | Result |
|---|---|---|

## Current Best
Experiment #N — `<metric_name>` = <value> (+<pct>% vs baseline)
```

---

## References

- [pi-autoresearch GitHub](https://github.com/davebcn87/pi-autoresearch)
- [karpathy/autoresearch](https://github.com/karpathy/autoresearch) — original ML
  experiment loop that inspired the concept
- [Hacker News discussion](https://news.ycombinator.com/item?id=47358215)
