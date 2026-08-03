#!/usr/bin/env python3
# ---
# name: autoresearch
# description: Autonomous experiment loop — run benchmarks, log results, and drive optimisation
# builtin: true
# ---
"""autoresearch.py — autonomous experiment loop for fir.

Provides three tools the agent uses to drive an iterative optimisation loop:

  run_experiment   Run autoresearch_bench.sh, parse METRIC lines, return results.
  log_experiment   Append a JSONL record to autoresearch.jsonl.
  lock_benchmark   Freeze the benchmark script's sha256 so experiments can't tamper with it.

And one slash command:

  /autoresearch    Print a summary of the experiment log.

Benchmark scripts should emit lines of the form:
    METRIC name=value
for each metric they measure.
"""

from __future__ import annotations

import contextlib
import hashlib
import json
import os
import re
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import fir_ext

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_METRIC_RE = re.compile(r"^METRIC\s+(\w+)\s*=\s*([0-9.eE+\-]+)\s*$", re.MULTILINE)


def _parse_metrics(output: str) -> dict[str, float]:
    """Extract all METRIC name=value lines from output."""
    result: dict[str, float] = {}
    for m in _METRIC_RE.finditer(output):
        with contextlib.suppress(ValueError):
            result[m.group(1)] = float(m.group(2))
    return result


def _jsonl_path(cwd: str) -> Path:
    return Path(cwd) / "autoresearch.jsonl"


def _next_experiment_number(cwd: str) -> int:
    path = _jsonl_path(cwd)
    if not path.exists():
        return 1
    count = 0
    with path.open() as f:
        for line in f:
            line = line.strip()
            if line:
                count += 1
    return count + 1


def _git(cwd: str, *args: str) -> str:
    """Run a git command in cwd, returning stdout (empty string on any failure)."""
    try:
        result = subprocess.run(
            ["git", *args],
            cwd=cwd,
            capture_output=True,
            text=True,
            timeout=5,
        )
    except Exception:
        return ""
    return result.stdout if result.returncode == 0 else ""


def _current_commit(cwd: str) -> str:
    return _git(cwd, "rev-parse", "--short", "HEAD").strip()


def _worktrees(cwd: str) -> list[str]:
    """All worktree roots of the repo containing cwd, main worktree first."""
    return [
        line[len("worktree ") :].strip()
        for line in _git(cwd, "worktree", "list", "--porcelain").splitlines()
        if line.startswith("worktree ")
    ]


def _own_worktree(cwd: str) -> str:
    """Root of the worktree containing cwd (cwd itself if git is unavailable)."""
    return _git(cwd, "rev-parse", "--show-toplevel").strip() or cwd


def _sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def _collect_locks(cwd: str) -> list[Path]:
    """Every autoresearch.lock across the repo's worktrees (git order, cwd last)."""
    seen: set[str] = set()
    found: list[Path] = []
    for root in [*_worktrees(cwd), _own_worktree(cwd), cwd]:
        real = os.path.realpath(root)
        if real in seen:
            continue
        seen.add(real)
        candidate = Path(root) / "autoresearch.lock"
        if candidate.exists():
            found.append(candidate)
    return found


def _read_lock_sha(path: Path) -> str:
    """Read a lock's sha256, refusing anything unverifiable."""
    try:
        lock = json.loads(path.read_text())
    except Exception as exc:
        raise fir_ext.ToolError(
            f"Benchmark lock at {path} is unreadable: {exc}. "
            "Fix or remove the lock, then re-baseline and call lock_benchmark again."
        ) from exc
    sha = lock.get("sha256") if isinstance(lock, dict) else None
    if not sha:
        raise fir_ext.ToolError(
            f"Benchmark lock at {path} has no sha256 field — it is malformed. "
            "Refusing to run against an unverifiable lock. Remove it, re-baseline, "
            "and call lock_benchmark again."
        )
    return str(sha)


def _resolve_lock(cwd: str) -> tuple[Path | None, str | None]:
    """Find the campaign's lock and its sha256, or (None, None) when unlocked.

    Locks are collected from every worktree of the repo and deduplicated **by
    content**, because the campaign root's lock is routinely committed and so
    every experiment sub-worktree inherits an identical copy — that is one
    logical lock, not a conflict.

    One distinct sha256 → that is the campaign's lock, wherever it was found.
    This is what lets a run from the campaign root and a run from a
    sub-worktree agree, without either trusting a lock merely because it sits
    in the current directory.

    Several distinct sha256s → genuinely ambiguous (a second campaign in the
    repo, a stale lock, or an experiment that re-locked itself). Refuse and
    defer to the user rather than guess; guessing is how an experiment would
    authorise its own rewritten benchmark.
    """
    by_sha: dict[str, Path] = {}
    for path in _collect_locks(cwd):
        by_sha.setdefault(_read_lock_sha(path), path)

    if not by_sha:
        return None, None
    if len(by_sha) == 1:
        sha, path = next(iter(by_sha.items()))
        return path, sha

    listed = "\n".join(f"  {p}  (sha256 {sha[:12]}…)" for sha, p in by_sha.items())
    raise fir_ext.ToolError(
        f"Ambiguous benchmark lock — {len(by_sha)} distinct locks found in this repo:\n"
        f"{listed}\n"
        "Refusing to guess which one governs this campaign. Stop and ask the user "
        "which lock is authoritative; do not pick one yourself. They can remove the "
        "stale lock or supply lock_path."
    )


# ---------------------------------------------------------------------------
# Tool: lock_benchmark
# ---------------------------------------------------------------------------


@fir_ext.tool(
    name="lock_benchmark",
    description=(
        "Freeze the campaign's benchmark. Computes the sha256 of "
        "<cwd>/autoresearch_bench.sh and writes autoresearch.lock in the campaign "
        "root (cwd — next to autoresearch.jsonl). run_experiment then refuses to "
        "run any experiment whose benchmark differs from this hash. Call once after "
        "the baseline. Re-locking is a deliberate, explicit act (it overwrites)."
    ),
    parameters={
        "type": "object",
        "properties": {
            "cwd": {
                "type": "string",
                "description": (
                    "Campaign root — the worktree holding autoresearch_bench.sh and "
                    "autoresearch.jsonl. Defaults to the current directory if omitted."
                ),
            },
        },
        "required": [],
    },
    display_hint={
        "title_args": [{"name": "cwd", "style": "path"}],
    },
)
def lock_benchmark(params: dict[str, Any], ctx: fir_ext.Context) -> dict[str, Any]:
    cwd = params.get("cwd") or os.getcwd()

    script = Path(cwd) / "autoresearch_bench.sh"
    if not script.exists():
        raise fir_ext.ToolError(
            f"No autoresearch_bench.sh found in {cwd} — cannot lock a missing benchmark. "
            "Write and verify the benchmark first (see the autoresearch-create skill)."
        )

    digest = _sha256_file(script)
    lock_path = Path(cwd) / "autoresearch.lock"
    lock = {
        "sha256": digest,
        "path": str(script),
        "timestamp": datetime.now(tz=timezone.utc).isoformat(),
        "commit": _current_commit(cwd),
    }
    lock_path.write_text(json.dumps(lock, indent=2) + "\n")

    ctx.set_status(f"🔒 benchmark locked ({digest[:12]}…)")

    summary = {
        "locked": True,
        "sha256": digest,
        "script": str(script),
        "lock_path": str(lock_path),
        "commit": lock["commit"],
    }
    return {
        "content": [{"type": "text", "text": json.dumps(summary, indent=2)}],
        "is_error": False,
    }


# ---------------------------------------------------------------------------
# Tool: run_experiment
# ---------------------------------------------------------------------------


@fir_ext.tool(
    name="run_experiment",
    description=(
        "Run autoresearch_bench.sh (the benchmark script) and return the metrics it reports. "
        "The script must output lines of the form 'METRIC name=value' for each metric. "
        "Returns: metrics (dict), wall_ms, stdout, stderr, exit_code, and success flag. "
        "If the campaign locked its benchmark (see lock_benchmark), this refuses to run "
        "when autoresearch_bench.sh differs from the locked hash."
    ),
    parameters={
        "type": "object",
        "properties": {
            "cwd": {
                "type": "string",
                "description": (
                    "Working directory containing autoresearch_bench.sh. "
                    "Defaults to the current directory if omitted."
                ),
            },
            "timeout": {
                "type": "number",
                "description": "Maximum seconds to wait for the benchmark. Default 300.",
            },
            "lock_path": {
                "type": "string",
                "description": (
                    "Optional explicit path to the campaign's autoresearch.lock. "
                    "Overrides the automatic lookup (useful when several campaigns "
                    "share one repo). Rarely needed."
                ),
            },
        },
        "required": [],
    },
    display_hint={
        "title_args": [
            {"name": "cwd", "style": "path"},
            {"name": "timeout", "label": "timeout"},
        ],
        "result_max_lines": 15,
    },
)
def run_experiment(params: dict[str, Any], ctx: fir_ext.Context) -> dict[str, Any]:
    cwd = params.get("cwd") or os.getcwd()
    timeout = float(params.get("timeout") or 300)

    # Locate the campaign lock first — a missing benchmark under an active lock
    # is itself tampering (delete-and-replace), not a "no benchmark" mistake.
    # A relative lock_path resolves against this experiment's cwd, not fir's.
    if params.get("lock_path"):
        lock_path = Path(cwd) / Path(params["lock_path"])
        if not lock_path.exists():
            raise fir_ext.ToolError(f"No benchmark lock found at {lock_path} (explicit lock_path).")
        locked_sha: str | None = _read_lock_sha(lock_path)
    else:
        lock_path, locked_sha = _resolve_lock(cwd)

    # Prefer autoresearch_bench.sh, fall back to autoresearch.sh
    script = Path(cwd) / "autoresearch_bench.sh"
    if not script.exists() and lock_path is None:
        script = Path(cwd) / "autoresearch.sh"
    if not script.exists():
        if lock_path is not None:
            raise fir_ext.ToolError(
                f"No autoresearch_bench.sh in {cwd}, but the campaign is locked "
                f"({lock_path}) — the benchmark was deleted or renamed in this "
                "experiment. Restore it, or get explicit user consent to re-baseline "
                "and call lock_benchmark again."
            )
        raise fir_ext.ToolError(
            f"No benchmark script found in {cwd}. "
            "Create autoresearch_bench.sh with a benchmark that outputs 'METRIC name=value' lines."
        )

    # Benchmark integrity check: if the campaign locked the benchmark, refuse
    # to run when the script this experiment sees differs from the locked
    # hash. This closes the self-confirming loop where an experiment rewrites
    # its own benchmark, "wins", and gets merged with the tampered script.
    lock_note: str | None = None
    if lock_path is not None and locked_sha is not None:
        current_sha = _sha256_file(script)
        if current_sha != locked_sha:
            error = (
                f"Benchmark modified since lock — refusing to run.\n"
                f"  script:  {script}\n"
                f"  locked sha256:  {locked_sha}\n"
                f"  current sha256: {current_sha}\n"
                f"  lock file: {lock_path}\n"
                "autoresearch_bench.sh is FROZEN for the campaign. Either revert the "
                "benchmark change in this experiment, or — if the benchmark genuinely "
                "must change — stop, get explicit user consent, re-baseline, and call "
                "lock_benchmark again. (If that lock file belongs to a different "
                "campaign in this repo, ask the user to remove it or supply lock_path.)"
            )
            return {
                "content": [
                    {
                        "type": "text",
                        "text": json.dumps(
                            {
                                "success": False,
                                "metrics": {},
                                "error": error,
                                "locked_sha256": locked_sha,
                                "current_sha256": current_sha,
                                "lock_path": str(lock_path),
                            },
                            indent=2,
                        ),
                    }
                ],
                "is_error": True,
            }
    else:
        lock_note = (
            "No benchmark lock set (autoresearch.lock not found) — running unlocked. "
            "Call lock_benchmark after the baseline to freeze the benchmark."
        )

    ctx.set_status("⚗️  running experiment…")

    wall_start = time.monotonic()
    try:
        proc = subprocess.run(
            ["bash", str(script)],
            cwd=cwd,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        ctx.set_status("")
        raise fir_ext.ToolError(f"Benchmark timed out after {timeout:.0f}s.") from None
    except Exception as exc:
        ctx.set_status("")
        raise fir_ext.ToolError(f"Failed to run benchmark: {exc}") from exc
    wall_ms = round((time.monotonic() - wall_start) * 1000, 1)

    ctx.set_status("")

    combined = proc.stdout + "\n" + proc.stderr
    metrics = _parse_metrics(combined)

    if proc.returncode != 0 and not metrics:
        payload = {
            "success": False,
            "exit_code": proc.returncode,
            "metrics": {},
            "wall_ms": wall_ms,
            "stdout": proc.stdout,
            "stderr": proc.stderr,
            "error": "Benchmark exited with non-zero status and produced no metrics.",
        }
        if lock_note:
            payload["lock_note"] = lock_note
        return {
            "content": [{"type": "text", "text": json.dumps(payload, indent=2)}],
            "is_error": False,
        }

    payload = {
        "success": proc.returncode == 0,
        "exit_code": proc.returncode,
        "metrics": metrics,
        "wall_ms": wall_ms,
        "stdout": proc.stdout,
        "stderr": proc.stderr,
    }
    if lock_note:
        payload["lock_note"] = lock_note
    return {
        "content": [{"type": "text", "text": json.dumps(payload, indent=2)}],
        "is_error": False,
    }


# ---------------------------------------------------------------------------
# Tool: log_experiment
# ---------------------------------------------------------------------------


@fir_ext.tool(
    name="log_experiment",
    description=(
        "Append an experiment record to autoresearch.jsonl. "
        "Call this after every run_experiment to keep a permanent audit trail. "
        "status must be one of: 'baseline', 'keep', 'revert', 'error'."
    ),
    parameters={
        "type": "object",
        "properties": {
            "description": {
                "type": "string",
                "description": "Short description of what was tried in this experiment.",
            },
            "hypothesis": {
                "type": "string",
                "description": "Why you expected this change to help.",
            },
            "metrics": {
                "type": "object",
                "description": "Metrics dict as returned by run_experiment.",
                "additionalProperties": {"type": "number"},
            },
            "primary_metric": {
                "type": "string",
                "description": "Name of the metric being optimised.",
            },
            "baseline_value": {
                "type": "number",
                "description": "Baseline value of the primary metric (from experiment #1).",
            },
            "wall_ms": {
                "type": "number",
                "description": "Benchmark wall-clock time in milliseconds, as returned by run_experiment.",
            },
            "status": {
                "type": "string",
                "enum": ["baseline", "keep", "revert", "error"],
                "description": "Outcome of this experiment.",
            },
            "cwd": {
                "type": "string",
                "description": "Working directory. Defaults to current directory.",
            },
            "notes": {
                "type": "string",
                "description": "Any additional notes or observations.",
            },
        },
        "required": ["description", "metrics", "primary_metric", "status"],
    },
    display_hint={
        "title_args": [
            {"name": "description", "style": "accent"},
            {"name": "status", "label": "status"},
            {"name": "primary_metric", "label": "metric"},
        ],
    },
)
def log_experiment(params: dict[str, Any], ctx: fir_ext.Context) -> dict[str, Any]:
    cwd = params.get("cwd") or os.getcwd()
    path = _jsonl_path(cwd)

    experiment_number = _next_experiment_number(cwd)
    commit = _current_commit(cwd)

    primary_metric: str = params["primary_metric"]
    metrics: dict[str, float] = params.get("metrics") or {}
    primary_value: float | None = metrics.get(primary_metric)
    baseline_value: float | None = params.get("baseline_value")

    delta_pct: float | None = None
    if primary_value is not None and baseline_value is not None and baseline_value != 0:
        delta_pct = round((primary_value - baseline_value) / abs(baseline_value) * 100, 2)

    record: dict[str, Any] = {
        "timestamp": datetime.now(tz=timezone.utc).isoformat(),
        "experiment": experiment_number,
        "description": params["description"],
        "hypothesis": params.get("hypothesis", ""),
        "commit": commit,
        "metrics": metrics,
        "primary_metric": primary_metric,
        "primary_value": primary_value,
        "baseline_value": baseline_value,
        "delta_pct": delta_pct,
        "status": params["status"],
    }
    wall_ms = params.get("wall_ms")
    if wall_ms is not None:
        record["wall_ms"] = wall_ms
    if params.get("notes"):
        record["notes"] = params["notes"]

    # Append to JSONL (one JSON object per line).
    with path.open("a") as f:
        f.write(json.dumps(record) + "\n")

    # Friendly status bar update.
    if delta_pct is not None:
        sign = "+" if delta_pct >= 0 else ""
        ctx.set_status(
            f"⚗️  exp #{experiment_number} [{params['status']}] "
            f"{primary_metric}={primary_value} ({sign}{delta_pct}%)"
        )
    else:
        ctx.set_status(f"⚗️  exp #{experiment_number} [{params['status']}] logged")

    summary = {
        "experiment": experiment_number,
        "status": params["status"],
        "primary_metric": primary_metric,
        "primary_value": primary_value,
        "delta_pct": delta_pct,
        "logged_to": str(path),
    }

    return {
        "content": [{"type": "text", "text": json.dumps(summary, indent=2)}],
        "is_error": False,
    }


# ---------------------------------------------------------------------------
# Slash command: /autoresearch
# ---------------------------------------------------------------------------


def _fmt_wall(ms: float) -> str:
    """Format a millisecond duration compactly (ms / s / m)."""
    if ms < 1000:
        return f"{ms:.0f}ms"
    secs = ms / 1000.0
    if secs < 60:
        return f"{secs:.1f}s"
    mins = int(secs // 60)
    rem = secs - mins * 60
    return f"{mins}m{rem:.0f}s"


def _load_experiments(cwd: str) -> list[dict[str, Any]]:
    path = _jsonl_path(cwd)
    if not path.exists():
        return []
    experiments = []
    with path.open() as f:
        for line in f:
            line = line.strip()
            if line:
                with contextlib.suppress(json.JSONDecodeError):
                    experiments.append(json.loads(line))
    return experiments


@fir_ext.command(
    name="autoresearch",
    description="Show a summary of the autoresearch experiment log",
)
def cmd_autoresearch(args: list[str], ctx: fir_ext.Context) -> dict[str, Any]:
    cwd = args[0] if args else os.getcwd()
    experiments = _load_experiments(cwd)

    if not experiments:
        return {
            "message": (
                "No autoresearch.jsonl found in the current directory.\n\n"
                "Use the `autoresearch-create` skill to set up a new experiment loop:\n"
                "  Tell fir: 'use the autoresearch-create skill to start optimising <goal>'"
            )
        }

    total = len(experiments)
    kept = sum(1 for e in experiments if e.get("status") == "keep")
    reverted = sum(1 for e in experiments if e.get("status") == "revert")
    errors = sum(1 for e in experiments if e.get("status") == "error")

    total_wall_ms = sum(e.get("wall_ms") or 0 for e in experiments)

    # Find baseline and best.
    baseline = next((e for e in experiments if e.get("status") == "baseline"), None)
    baseline_val = baseline.get("primary_value") if baseline else None
    primary = (baseline or experiments[0]).get("primary_metric", "unknown")

    best: dict[str, Any] | None = None
    best_val: float | None = None
    for e in experiments:
        v = e.get("primary_value")
        if v is not None and (best_val is None or v > best_val):
            best_val = v
            best = e

    lines: list[str] = []
    lines.append(f"## Autoresearch — {Path(cwd).name}")
    lines.append("")
    lines.append(
        f"Metric: `{primary}` | "
        f"Experiments: {total} | "
        f"Kept: {kept} | Reverted: {reverted} | Errors: {errors}"
    )
    if baseline_val is not None:
        lines.append(f"Baseline: {baseline_val}")
    if best and best_val is not None and best.get("status") != "baseline":
        delta = best.get("delta_pct")
        sign = "+" if (delta or 0) >= 0 else ""
        lines.append(
            f"**Best: {best_val} ({sign}{delta}%) — exp #{best['experiment']}: "
            f"{best.get('description', '')}**"
        )

    # Efficiency — wall time only (the extension cannot see token cost).
    keep_rate = (kept / total * 100) if total else 0.0
    eff = (
        f"Efficiency: total wall {_fmt_wall(total_wall_ms)} | "
        f"keep rate {keep_rate:.0f}% ({kept}/{total})"
    )
    if kept:
        eff += f" | wall/win {_fmt_wall(total_wall_ms / kept)}"
    lines.append(eff)
    lines.append("")

    # Last 10 experiments.
    recent = experiments[-10:]
    lines.append(f"### Last {len(recent)} experiments")
    lines.append("")
    lines.append("| # | Status | Value | Δ% | Description |")
    lines.append("|---|---|---|---|---|")
    for e in recent:
        n = e.get("experiment", "?")
        status = e.get("status", "?")
        val = e.get("primary_value")
        val_str = f"{val}" if val is not None else "—"
        delta = e.get("delta_pct")
        if delta is not None:
            sign = "+" if delta >= 0 else ""
            delta_str = f"{sign}{delta}%"
        else:
            delta_str = "—"
        desc = (e.get("description") or "")[:60]
        icon = {"baseline": "📏", "keep": "✅", "revert": "❌", "error": "💥"}.get(status, "?")
        lines.append(f"| {n} | {icon} {status} | {val_str} | {delta_str} | {desc} |")

    return {"message": "\n".join(lines)}


fir_ext.run(name="autoresearch")
