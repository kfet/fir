#!/usr/bin/env python3
# ---
# name: schedule
# description: Schedule the agent to continue at a future time
# builtin: false
# modes: tui
# commands: schedule: Schedule the agent to continue at a future time
# ---
"""Schedule the agent session to resume at a future time.

Supports multiple concurrent scheduled tasks, each identified by a
short auto-generated ID.

Usage:
  /schedule 30s             — resume in 30 seconds
  /schedule 45m             — resume in 45 minutes
  /schedule 1h30m           — resume in 1 hour 30 minutes
  /schedule 2pm             — resume at 2:00 PM local time
  /schedule 14:00           — resume at 14:00 local time
  /schedule 2:30pm          — resume at 2:30 PM local time
  /schedule 45m <msg>       — send <msg> as a user message in 45 min
  /schedule 2pm <msg>       — send <msg> as a user message at 2:00 PM
  /schedule cancel <id>     — cancel a specific scheduled task
  /schedule cancel all      — cancel all scheduled tasks
  /schedule cancel          — cancel if only one task, else list them
  /schedule                 — show all active schedules
"""

from __future__ import annotations

import contextlib
import itertools
import re
import threading
from datetime import UTC, datetime, timedelta

import fir_ext

# ---------------------------------------------------------------------------
# Module-level schedule state (protected by _lock)
# ---------------------------------------------------------------------------

_lock = threading.Lock()
_schedules: dict[str, _ScheduleEntry] = {}
_id_counter = itertools.count(1)


class _ScheduleEntry:
    """A single scheduled task."""

    __slots__ = ("id", "message", "stop_event", "target", "thread")

    def __init__(
        self,
        id: str,  # noqa: A002
        target: datetime,
        message: str | None,
        stop_event: threading.Event,
        thread: threading.Thread | None = None,
    ):
        self.id = id
        self.target = target
        self.message = message
        self.stop_event = stop_event
        self.thread = thread


def _next_id() -> str:
    """Generate a short sequential ID like 's1', 's2', etc."""
    return f"s{next(_id_counter)}"


# ---------------------------------------------------------------------------
# Formatting helpers
# ---------------------------------------------------------------------------


def _now() -> datetime:
    """Return the current local time (timezone-aware)."""
    return datetime.now(tz=UTC).astimezone()


def _format_time(dt: datetime) -> str:
    """Format datetime as '2:00 PM' (no leading zero)."""
    h = dt.hour % 12 or 12
    ampm = "AM" if dt.hour < 12 else "PM"
    return f"{h}:{dt.minute:02d} {ampm}"


def _format_countdown(remaining: timedelta) -> str:
    """Format a timedelta as '1h23m45s', '12m34s', or '45s'."""
    total = max(0, int(remaining.total_seconds()))
    hours, rest = divmod(total, 3600)
    minutes, seconds = divmod(rest, 60)
    if hours > 0:
        return f"{hours}h{minutes:02d}m{seconds:02d}s"
    if minutes > 0:
        return f"{minutes}m{seconds:02d}s"
    return f"{seconds}s"


# ---------------------------------------------------------------------------
# Status bar update
# ---------------------------------------------------------------------------


def _update_status(ctx: fir_ext.Context) -> None:
    """Update the status bar to reflect all active schedules."""
    with _lock:
        entries = sorted(_schedules.values(), key=lambda e: e.target)

    if not entries:
        with contextlib.suppress(Exception):
            ctx.set_status("")
        return

    parts = []
    for e in entries:
        remaining = e.target - _now()
        if remaining.total_seconds() <= 0:
            continue
        desc = f"[{e.id}] {_format_countdown(remaining)}"
        if e.message:
            desc += f": {e.message[:30]}"
        parts.append(desc)

    if parts:
        with contextlib.suppress(Exception):
            ctx.set_status("⏰ " + " | ".join(parts))
    else:
        with contextlib.suppress(Exception):
            ctx.set_status("")


# ---------------------------------------------------------------------------
# Background countdown thread
# ---------------------------------------------------------------------------


def _run_countdown(
    entry_id: str,
    target: datetime,
    stop: threading.Event,
    ctx: fir_ext.Context,
    message: str | None = None,
) -> None:
    """Tick the countdown, then fire continue_session or send_user_message."""

    while not stop.is_set():
        remaining = target - _now()
        if remaining.total_seconds() <= 0:
            break
        with contextlib.suppress(Exception):
            _update_status(ctx)
        stop.wait(1.0)

    if stop.is_set():
        with _lock:
            _schedules.pop(entry_id, None)
        with contextlib.suppress(Exception):
            _update_status(ctx)
        return

    # Timer fired — remove from schedules, then act.
    with _lock:
        _schedules.pop(entry_id, None)

    with contextlib.suppress(Exception):
        _update_status(ctx)

    with contextlib.suppress(Exception):
        if message:
            ctx.send_user_message(message)
        else:
            ctx.continue_session()


# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

# Relative: 45s, 45m, 1h, 1h30m, 1h30m10s, 90s
_RE_RELATIVE = re.compile(
    r"^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$", re.IGNORECASE,
)

# Absolute 12-hour: 2pm, 2:30pm
_RE_12H = re.compile(
    r"^(\d{1,2})(?::(\d{2}))?\s*(am|pm)$", re.IGNORECASE,
)

# Absolute 24-hour: 14:00
_RE_24H = re.compile(r"^(\d{1,2}):(\d{2})$")


def _parse_target(raw: str) -> datetime | None:
    """Return the target datetime for a raw argument string, or None."""
    raw = raw.strip()

    m = _RE_RELATIVE.fullmatch(raw)
    if m and (m.group(1) or m.group(2) or m.group(3)):
        hours = int(m.group(1) or 0)
        minutes = int(m.group(2) or 0)
        seconds = int(m.group(3) or 0)
        return _now() + timedelta(
            hours=hours, minutes=minutes, seconds=seconds,
        )

    m = _RE_12H.fullmatch(raw)
    if m:
        hour = int(m.group(1))
        minute = int(m.group(2) or 0)
        meridiem = m.group(3).lower()
        if meridiem == "pm" and hour != 12:
            hour += 12
        elif meridiem == "am" and hour == 12:
            hour = 0
        now = _now()
        target = now.replace(
            hour=hour, minute=minute, second=0, microsecond=0,
        )
        if target <= now:
            target += timedelta(days=1)
        return target

    m = _RE_24H.fullmatch(raw)
    if m:
        hour, minute = int(m.group(1)), int(m.group(2))
        now = _now()
        target = now.replace(
            hour=hour, minute=minute, second=0, microsecond=0,
        )
        if target <= now:
            target += timedelta(days=1)
        return target

    return None


# ---------------------------------------------------------------------------
# Command handler
# ---------------------------------------------------------------------------

_USAGE = (
    "No schedules active.\n\n"
    "Usage:\n"
    "  /schedule 30s          — in 30 seconds\n"
    "  /schedule 45m          — in 45 minutes\n"
    "  /schedule 1h30m        — in 1 hour 30 minutes\n"
    "  /schedule 2pm          — at 2:00 PM\n"
    "  /schedule 14:00        — at 14:00\n"
    "  /schedule 2:30pm       — at 2:30 PM\n"
    "  /schedule 45m do stuff — send message in 45 min\n"
    "  /schedule cancel <id>  — cancel a specific schedule\n"
    "  /schedule cancel all   — cancel all schedules\n"
    "  /schedule cancel       — cancel (if only one active)"
)


def _format_entry(e: _ScheduleEntry) -> str:
    """Format a single schedule entry for display."""
    remaining = e.target - _now()
    line = (
        f"  [{e.id}] in {_format_countdown(remaining)} "
        f"(at {_format_time(e.target)})"
    )
    if e.message:
        line += f" — \"{e.message}\""
    else:
        line += " — continue"
    return line


@fir_ext.command(
    name="schedule",
    description=(
        "Schedule the agent to continue at a future time"
        " (e.g. /schedule 45m, /schedule 2pm,"
        " /schedule cancel, /schedule cancel s1)"
    ),
)
def cmd_schedule(args: list[str], ctx: fir_ext.Context):
    # ── no args: show current status or usage ─────────────────────────
    if not args:
        with _lock:
            entries = sorted(
                _schedules.values(), key=lambda e: e.target,
            )
        if not entries:
            return {"message": _USAGE}
        lines = ["⏰ Active schedules:"]
        lines.extend(_format_entry(e) for e in entries)
        lines.append("\nUse `/schedule cancel <id>` or `/schedule cancel all`.")
        return {"message": "\n".join(lines)}

    # ── cancel ────────────────────────────────────────────────────────
    if args[0].lower() == "cancel":
        # /schedule cancel all
        if len(args) >= 2 and args[1].lower() == "all":
            with _lock:
                count = len(_schedules)
                for e in _schedules.values():
                    e.stop_event.set()
                _schedules.clear()
            if count:
                with contextlib.suppress(Exception):
                    ctx.set_status("")
                return {
                    "message": f"Cancelled all {count} schedule(s).",
                }
            return {"message": "No schedules to cancel."}

        # /schedule cancel <id>
        if len(args) >= 2:
            sid = args[1]
            with _lock:
                entry = _schedules.pop(sid, None)
            if entry is not None:
                entry.stop_event.set()
                with contextlib.suppress(Exception):
                    _update_status(ctx)
                return {"message": f"Schedule [{sid}] cancelled."}
            return {"message": f"No schedule with id '{sid}'."}

        # /schedule cancel (no id) — cancel if exactly one
        with _lock:
            if len(_schedules) == 0:
                return {"message": "No schedules to cancel."}
            if len(_schedules) == 1:
                sid, entry = next(iter(_schedules.items()))
                del _schedules[sid]
            else:
                # Multiple — list them
                entries = sorted(
                    _schedules.values(), key=lambda e: e.target,
                )
                lines = [
                    "Multiple schedules active. "
                    "Specify an id or use `cancel all`:",
                ]
                for e in entries:
                    lines.append(_format_entry(e))
                return {"message": "\n".join(lines)}

        # Outside the lock — stop the entry and update status.
        entry.stop_event.set()
        with contextlib.suppress(Exception):
            _update_status(ctx)
        return {"message": f"Schedule [{sid}] cancelled."}

    # ── parse target time ─────────────────────────────────────────────
    target = _parse_target(args[0])
    if target is not None:
        message = " ".join(args[1:]) if len(args) > 1 else None
    else:
        # Try first two args combined (e.g. "2:30" "pm" split).
        if len(args) >= 2:
            target = _parse_target(args[0] + args[1])
            if target is not None:
                message = (
                    " ".join(args[2:]) if len(args) > 2 else None
                )
        if target is None:
            raw = " ".join(args)
            return {
                "message": (
                    f"Could not parse '{raw}'.\n"
                    "Try: 45m, 1h30m, 2pm, 14:00, 2:30pm"
                )
            }

    # ── create new schedule entry ─────────────────────────────────────
    stop = threading.Event()
    sid = _next_id()
    entry = _ScheduleEntry(
        id=sid, target=target, message=message, stop_event=stop,
    )

    t = threading.Thread(
        target=_run_countdown,
        args=(sid, target, stop, ctx, message),
        daemon=True,
    )
    entry.thread = t

    with _lock:
        _schedules[sid] = entry

    t.start()

    remaining = target - _now()
    action = f"send message: {message}" if message else "continue"
    return {
        "message": (
            f"⏰ [{sid}] Scheduled: will {action} in "
            f"{_format_countdown(remaining)} "
            f"(at {_format_time(target)})."
        )
    }


fir_ext.run(name="schedule")
