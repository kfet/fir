#!/usr/bin/env python3
# ---
# name: doctor
# description: Black-box failure recorder and diagnostic query tool
# builtin: true
# ---
"""fir doctor — black-box failure recorder and diagnostic query tool.

Records tool errors and session failures to ~/.config/fir/doctor.jsonl.
Exposes query tools so any session can inspect past failures.
"""

from __future__ import annotations

import json
import os
import time
from pathlib import Path
from typing import Any

import fir_ext

# ---------------------------------------------------------------------------
# Storage
# ---------------------------------------------------------------------------

# Doctor log is a cross-project failure history, so it lives in the global
# (lowest-priority) config dir advertised by the host — typically
# ~/.config/fir. Falls back to ~/.config/fir when running outside fir.
def _doctor_dir() -> Path:
    if fir_ext.config_dirs:
        return Path(fir_ext.config_dirs[-1])
    return Path.home() / ".config" / "fir"


def _doctor_log() -> Path:
    return _doctor_dir() / "doctor.jsonl"

# In-memory buffer for the current session (flushed on session_end).
_session: dict[str, Any] = {}
_tool_errors: list[dict[str, Any]] = []
_session_end_fired: bool = False


def _append_record(record: dict[str, Any]) -> None:
    """Append a single JSON record to the doctor log."""
    log = _doctor_log()
    log.parent.mkdir(parents=True, exist_ok=True)
    with open(log, "a") as f:
        f.write(json.dumps(record, separators=(",", ":")) + "\n")


def _read_records(limit: int = 200) -> list[dict[str, Any]]:
    """Read the last *limit* records from the log."""
    log = _doctor_log()
    if not log.exists():
        return []
    lines = log.read_text().strip().splitlines()
    records = []
    for line in lines[-limit:]:
        try:
            records.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return records


# ---------------------------------------------------------------------------
# Events
# ---------------------------------------------------------------------------


@fir_ext.on("session_start")
def on_session_start(params, ctx: fir_ext.Context) -> None:
    global _session_end_fired
    if params is None:
        params = {}
    _session.update({
        "session_id": params.get("session_id", "unknown"),
        "start_time": time.time(),
        "cwd": params.get("cwd", os.getcwd()),
    })
    _tool_errors.clear()
    _session_end_fired = False


@fir_ext.on("tool_execution_end")
def on_tool_execution_end(params: dict, ctx: fir_ext.Context) -> None:
    if params.get("is_error"):
        _tool_errors.append({
            "tool": params.get("tool_name", ""),
            "tool_call_id": params.get("tool_call_id", ""),
            "error_text": params.get("error_text", ""),
            "ts": time.time(),
        })


@fir_ext.on("session_end")
def on_session_end(params: dict, ctx: fir_ext.Context) -> None:
    global _session_end_fired
    _session_end_fired = True
    reason = params.get("reason", "unknown")
    error_msg = params.get("error", "")

    has_errors = bool(_tool_errors) or reason == "error" or bool(error_msg)

    if has_errors:
        record = {
            "type": "session_failure",
            "session_id": _session.get("session_id", "unknown"),
            "cwd": _session.get("cwd", ""),
            "start_time": _session.get("start_time"),
            "end_time": time.time(),
            "exit_reason": reason,
            "exit_error": error_msg,
            "tool_errors": _tool_errors[:50],  # cap to avoid huge records
            "tool_error_count": len(_tool_errors),
        }
        _append_record(record)


# Also record on legacy session_shutdown in case session_end isn't available yet.
@fir_ext.on("session_shutdown")
def on_session_shutdown(params: dict, ctx: fir_ext.Context) -> None:
    if _tool_errors and not _session_end_fired:
        # session_end didn't fire (older core), flush what we have.
        record = {
            "type": "session_failure",
            "session_id": _session.get("session_id", "unknown"),
            "cwd": _session.get("cwd", ""),
            "start_time": _session.get("start_time"),
            "end_time": time.time(),
            "exit_reason": "shutdown",
            "exit_error": "",
            "tool_errors": _tool_errors[:50],
            "tool_error_count": len(_tool_errors),
        }
        _append_record(record)


# ---------------------------------------------------------------------------
# Tools
# ---------------------------------------------------------------------------


@fir_ext.tool(
    name="doctor_query",
    description=(
        "Search past session failures recorded by fir doctor. "
        "Filter by tool name, error pattern, or date range. "
        "Returns matching failure records as JSON."
    ),
    parameters={
        "type": "object",
        "properties": {
            "tool_name": {
                "type": "string",
                "description": "Filter to failures involving this tool",
            },
            "pattern": {
                "type": "string",
                "description": "Substring to match in error text",
            },
            "limit": {
                "type": "integer",
                "description": "Max records to return (default 20)",
            },
        },
    },
)
def doctor_query(params: dict, ctx: fir_ext.Context) -> str:
    tool_filter = params.get("tool_name", "")
    pattern = params.get("pattern", "").lower()
    limit = params.get("limit", 20)

    records = _read_records(500)
    matches = []

    for r in reversed(records):
        if tool_filter:
            tool_errors = r.get("tool_errors", [])
            if not any(e.get("tool") == tool_filter for e in tool_errors):
                continue
        if pattern:
            blob = json.dumps(r).lower()
            if pattern not in blob:
                continue
        matches.append(r)
        if len(matches) >= limit:
            break

    if not matches:
        return "No matching failures found."

    return json.dumps(matches, indent=2)


@fir_ext.tool(
    name="doctor_summary",
    description="Show a summary of recent session failures: counts by tool, most common errors, last failure time.",
    parameters={"type": "object", "properties": {}},
)
def doctor_summary(params: dict, ctx: fir_ext.Context) -> str:
    records = _read_records(200)
    if not records:
        return "No failures recorded."

    total = len(records)
    tool_counts: dict[str, int] = {}
    error_snippets: dict[str, int] = {}

    for r in records:
        for e in r.get("tool_errors", []):
            tool = e.get("tool", "unknown")
            tool_counts[tool] = tool_counts.get(tool, 0) + 1
            err = e.get("error_text", "")[:80]
            if err:
                error_snippets[err] = error_snippets.get(err, 0) + 1

    top_tools = sorted(tool_counts.items(), key=lambda x: -x[1])[:10]
    top_errors = sorted(error_snippets.items(), key=lambda x: -x[1])[:5]

    last = records[-1]
    last_time = last.get("end_time", 0)
    last_ago = ""
    if last_time:
        ago = time.time() - last_time
        if ago < 3600:
            last_ago = f"{int(ago / 60)}m ago"
        elif ago < 86400:
            last_ago = f"{int(ago / 3600)}h ago"
        else:
            last_ago = f"{int(ago / 86400)}d ago"

    lines = [
        f"Total failed sessions: {total}",
        f"Last failure: {last_ago or 'unknown'}",
        "",
        "Top failing tools:",
    ]
    for tool, count in top_tools:
        lines.append(f"  {tool}: {count}")

    if top_errors:
        lines.append("")
        lines.append("Most common errors:")
        for err, count in top_errors:
            lines.append(f"  ({count}x) {err}")

    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Slash command
# ---------------------------------------------------------------------------


@fir_ext.command(name="doctor", description="Show recent failure diagnostics")
def cmd_doctor(args: list[str], ctx: fir_ext.Context) -> dict:
    summary = doctor_summary({}, ctx)
    return {"message": summary}


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

fir_ext.run(name="doctor")
