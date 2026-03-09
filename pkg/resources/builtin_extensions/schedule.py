#!/usr/bin/env python3
# ---
# name: schedule
# description: Schedule the agent to continue at a future time
# builtin: false
# modes: tui
# ---
"""Schedule the agent session to resume at a future time.

Usage:
  /schedule 45m         — resume in 45 minutes
  /schedule 1h30m       — resume in 1 hour 30 minutes
  /schedule 2pm         — resume at 2:00 PM local time
  /schedule 14:00       — resume at 14:00 local time
  /schedule 2:30pm      — resume at 2:30 PM local time
  /schedule cancel      — cancel a pending scheduled resume
  /schedule             — show current schedule status
"""

from __future__ import annotations

import contextlib
import re
import threading
from datetime import UTC, datetime, timedelta

import fir_ext

# ---------------------------------------------------------------------------
# Module-level schedule state (protected by _lock)
# ---------------------------------------------------------------------------

_lock = threading.Lock()
_stop_event: threading.Event | None = None
_target: datetime | None = None
_thread: threading.Thread | None = None


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
# Background countdown thread
# ---------------------------------------------------------------------------


def _run_countdown(target: datetime, stop: threading.Event, ctx: fir_ext.Context) -> None:
    """Tick the countdown status every second, then fire continue_session."""
    global _stop_event, _thread, _target

    at_str = _format_time(target)

    while not stop.is_set():
        remaining = target - _now()
        if remaining.total_seconds() <= 0:
            break
        with contextlib.suppress(Exception):
            ctx.set_status(
                f"⏰ Scheduled — executing in {_format_countdown(remaining)} (at {at_str})"
            )
        # Sleep up to 1 second, waking early if cancelled.
        stop.wait(1.0)

    if stop.is_set():
        # Explicitly cancelled — clear status and exit.
        with contextlib.suppress(Exception):
            ctx.set_status("")
        return

    # Timer fired naturally — clear module state, then continue the session.
    with _lock:
        _stop_event = None
        _thread = None
        _target = None

    with contextlib.suppress(Exception):
        ctx.set_status("")

    with contextlib.suppress(Exception):
        ctx.continue_session()


# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

# Relative: 45m, 1h, 1h30m, 90m
_RE_RELATIVE = re.compile(r'^(?:(\d+)h)?(?:(\d+)m)?$', re.IGNORECASE)

# Absolute 12-hour: 2pm, 2:30pm
_RE_12H = re.compile(r'^(\d{1,2})(?::(\d{2}))?\s*(am|pm)$', re.IGNORECASE)

# Absolute 24-hour: 14:00
_RE_24H = re.compile(r'^(\d{1,2}):(\d{2})$')


def _parse_target(raw: str) -> datetime | None:
    """Return the target datetime for a raw argument string, or None."""
    raw = raw.strip()

    m = _RE_RELATIVE.fullmatch(raw)
    if m and (m.group(1) or m.group(2)):
        hours = int(m.group(1) or 0)
        minutes = int(m.group(2) or 0)
        return _now() + timedelta(hours=hours, minutes=minutes)

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
        target = now.replace(hour=hour, minute=minute, second=0, microsecond=0)
        if target <= now:
            target += timedelta(days=1)
        return target

    m = _RE_24H.fullmatch(raw)
    if m:
        hour, minute = int(m.group(1)), int(m.group(2))
        now = _now()
        target = now.replace(hour=hour, minute=minute, second=0, microsecond=0)
        if target <= now:
            target += timedelta(days=1)
        return target

    return None


# ---------------------------------------------------------------------------
# Command handler
# ---------------------------------------------------------------------------

_USAGE = (
    "No schedule active.\n\n"
    "Usage:\n"
    "  /schedule 45m      — in 45 minutes\n"
    "  /schedule 1h30m    — in 1 hour 30 minutes\n"
    "  /schedule 2pm      — at 2:00 PM\n"
    "  /schedule 14:00    — at 14:00\n"
    "  /schedule 2:30pm   — at 2:30 PM\n"
    "  /schedule cancel   — cancel active schedule"
)


@fir_ext.command(
    name="schedule",
    description=(
        "Schedule the agent to continue at a future time"
        " (e.g. /schedule 45m, /schedule 2pm, /schedule cancel)"
    ),
)
def cmd_schedule(args: list[str], ctx: fir_ext.Context):
    global _stop_event, _thread, _target

    # ── no args: show current status or usage ──────────────────────────────
    if not args:
        with _lock:
            t = _target
        if t is not None:
            remaining = t - _now()
            if remaining.total_seconds() > 0:
                return {
                    "message": (
                        f"⏰ Schedule active: executing in {_format_countdown(remaining)} "
                        f"(at {_format_time(t)}). Use `/schedule cancel` to cancel."
                    )
                }
        return {"message": _USAGE}

    # ── cancel ─────────────────────────────────────────────────────────────
    if args[0].lower() == "cancel":
        with _lock:
            had = _target is not None
            if _stop_event is not None:
                _stop_event.set()
            _stop_event = None
            _thread = None
            _target = None
        if had:
            # set_status("") is sent by the countdown thread on stop; call it
            # here too in case the thread hasn't run yet.
            with contextlib.suppress(Exception):
                ctx.set_status("")
            return {"message": "Schedule cancelled."}
        return {"message": "No schedule to cancel."}

    # ── parse target time ───────────────────────────────────────────────────
    raw = " ".join(args)
    target = _parse_target(raw)
    if target is None:
        return {
            "message": (
                f"Could not parse '{raw}'.\n"
                "Try: 45m, 1h30m, 2pm, 14:00, 2:30pm"
            )
        }

    # ── cancel existing schedule and start new one ─────────────────────────
    with _lock:
        if _stop_event is not None:
            _stop_event.set()
        stop = threading.Event()
        _stop_event = stop
        _target = target

    t = threading.Thread(target=_run_countdown, args=(target, stop, ctx), daemon=True)
    with _lock:
        _thread = t
    t.start()

    remaining = target - _now()
    return {
        "message": (
            f"⏰ Scheduled: will execute in {_format_countdown(remaining)} "
            f"(at {_format_time(target)})."
        )
    }


fir_ext.run(name="schedule")
