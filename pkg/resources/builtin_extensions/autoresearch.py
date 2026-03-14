#!/usr/bin/env python3
# ---
# name: autoresearch
# description: Autonomous experiment loop — run benchmarks, log results, and drive optimisation
# commands: autoresearch: Show experiment log summary
# ---
"""autoresearch.py — autonomous experiment loop for fir.

Provides two tools the agent uses to drive an iterative optimisation loop:

  run_experiment   Run autoresearch.sh, parse METRIC lines, return results.
  log_experiment   Append a JSONL record to autoresearch.jsonl.

And one slash command:

  /autoresearch    Print a summary of the experiment log.

Benchmark scripts should emit lines of the form:
    METRIC name=value
for each metric they measure.
"""

from __future__ import annotations

import contextlib
import json
import os
import re
import subprocess
from datetime import UTC, datetime
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


def _current_commit(cwd: str) -> str:
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=cwd,
            capture_output=True,
            text=True,
            timeout=5,
        )
        return result.stdout.strip() if result.returncode == 0 else ""
    except Exception:
        return ""


# ---------------------------------------------------------------------------
# Tool: run_experiment
# ---------------------------------------------------------------------------


@fir_ext.tool(
    name="run_experiment",
    description=(
        "Run autoresearch.sh (the benchmark script) and return the metrics it reports. "
        "The script must output lines of the form 'METRIC name=value' for each metric. "
        "Returns: metrics (dict), stdout, stderr, exit_code, and success flag."
    ),
    parameters={
        "type": "object",
        "properties": {
            "cwd": {
                "type": "string",
                "description": (
                    "Working directory containing autoresearch.sh. "
                    "Defaults to the current directory if omitted."
                ),
            },
            "timeout": {
                "type": "number",
                "description": "Maximum seconds to wait for the benchmark. Default 300.",
            },
        },
        "required": [],
    },
)
def run_experiment(params: dict[str, Any], ctx: fir_ext.Context) -> dict[str, Any]:
    cwd = params.get("cwd") or os.getcwd()
    timeout = float(params.get("timeout") or 300)

    script = Path(cwd) / "autoresearch.sh"
    if not script.exists():
        raise fir_ext.ToolError(
            f"autoresearch.sh not found in {cwd}. "
            "Create it with a benchmark that outputs 'METRIC name=value' lines."
        )

    ctx.set_status("⚗️  running experiment…")

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

    ctx.set_status("")

    combined = proc.stdout + "\n" + proc.stderr
    metrics = _parse_metrics(combined)

    if proc.returncode != 0 and not metrics:
        return {
            "content": [
                {
                    "type": "text",
                    "text": json.dumps(
                        {
                            "success": False,
                            "exit_code": proc.returncode,
                            "metrics": {},
                            "stdout": proc.stdout,
                            "stderr": proc.stderr,
                            "error": "Benchmark exited with non-zero status and produced no metrics.",
                        },
                        indent=2,
                    ),
                }
            ],
            "is_error": False,
        }

    return {
        "content": [
            {
                "type": "text",
                "text": json.dumps(
                    {
                        "success": proc.returncode == 0,
                        "exit_code": proc.returncode,
                        "metrics": metrics,
                        "stdout": proc.stdout,
                        "stderr": proc.stderr,
                    },
                    indent=2,
                ),
            }
        ],
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
    if (
        primary_value is not None
        and baseline_value is not None
        and baseline_value != 0
    ):
        delta_pct = round(
            (primary_value - baseline_value) / abs(baseline_value) * 100, 2
        )

    record: dict[str, Any] = {
        "timestamp": datetime.now(tz=UTC).isoformat(),
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
        icon = {"baseline": "📏", "keep": "✅", "revert": "❌", "error": "💥"}.get(
            status, "?"
        )
        lines.append(f"| {n} | {icon} {status} | {val_str} | {delta_str} | {desc} |")

    return {"message": "\n".join(lines)}


fir_ext.run(name="autoresearch")
