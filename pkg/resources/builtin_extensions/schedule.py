#!/usr/bin/env python3
# ---
# name: schedule
# description: Schedule the agent to continue at a future time
# builtin: true
# modes: tui
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
import json
import re
import threading
from datetime import datetime, timedelta, timezone

import fir_ext

# Tick interval for countdown loop (seconds). Tests may override for speed.
_TICK = 1.0

# ---------------------------------------------------------------------------
# Module-level schedule state (protected by _lock)
# ---------------------------------------------------------------------------

_lock = threading.Lock()
_schedules: dict[str, _ScheduleEntry] = {}
_id_counter = itertools.count(1)


class _ScheduleEntry:
    """A single scheduled task."""

    __slots__ = ("auto", "id", "message", "stop_event", "target", "thread")

    def __init__(
        self,
        id: str,  # noqa: A002
        target: datetime,
        message: str | None,
        stop_event: threading.Event,
        thread: threading.Thread | None = None,
        auto: bool = False,
    ):
        self.id = id
        self.target = target
        self.message = message
        self.stop_event = stop_event
        self.thread = thread
        self.auto = auto


def _next_id() -> str:
    """Generate a short sequential ID like 's1', 's2', etc."""
    return f"s{next(_id_counter)}"


# ---------------------------------------------------------------------------
# Formatting helpers
# ---------------------------------------------------------------------------


def _now() -> datetime:
    """Return the current time in UTC (timezone-aware)."""
    return datetime.now(tz=timezone.utc)


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
        stop.wait(_TICK)

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

    # Announce the firing in the transcript so the user can clearly tell
    # the schedule actually ran (the original "Scheduled" notice stays put
    # and would otherwise look like nothing happened).
    action = f"sending message: {message}" if message else "continuing session"
    with contextlib.suppress(Exception):
        ctx.send_message(
            custom_type="schedule_fired",
            content=f"🔥 [{entry_id}] Schedule fired — {action}.",
            display=True,
        )

    with contextlib.suppress(Exception):
        if message:
            ctx.send_user_message(message)
        else:
            # Use send_user_message instead of continue_session — some models
            # don't support assistant-last conversations (prefill).
            ctx.send_user_message("continue")


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
        local_now = _now().astimezone()
        target = local_now.replace(
            hour=hour, minute=minute, second=0, microsecond=0,
        )
        if target <= local_now:
            target += timedelta(days=1)
        return target

    m = _RE_24H.fullmatch(raw)
    if m:
        hour, minute = int(m.group(1)), int(m.group(2))
        local_now = _now().astimezone()
        target = local_now.replace(
            hour=hour, minute=minute, second=0, microsecond=0,
        )
        if target <= local_now:
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
            f"⏰ [{sid}] Scheduled — will {action} in "
            f"{_format_countdown(remaining)} "
            f"(at {_format_time(target)}). "
            f"This notice will not update; when the timer fires "
            f"you'll see a [schedule_fired] entry below."
        )
    }


# ---------------------------------------------------------------------------
# Session persistence (survive /reexec)
# ---------------------------------------------------------------------------

_SESSION_DATA_KEY = "schedules"


def _serialize_schedules() -> str:
    """Serialize active schedules to a JSON string for session storage."""
    with _lock:
        entries = list(_schedules.values())
    records = [
        {"id": e.id, "target_iso": e.target.isoformat(), "message": e.message}
        for e in entries
        if not e.auto  # auto-resume entries are transient; never persist them
    ]
    return json.dumps(records)


def _restore_schedules(data: str, ctx: fir_ext.Context) -> int:
    """Deserialize and restart countdown threads for previously active schedules.

    Returns the number of schedules successfully restored.
    """
    try:
        records = json.loads(data)
    except Exception:
        return 0

    restored = 0
    max_n = 0
    for rec in records:
        try:
            target = datetime.fromisoformat(rec["target_iso"])
            message = rec.get("message")
            sid = rec["id"]
        except (KeyError, ValueError):
            continue

        # Skip already-elapsed schedules.
        if target <= _now():
            continue

        # Track the highest numeric suffix so we can bump the counter.
        with contextlib.suppress(ValueError):
            max_n = max(max_n, int(sid.lstrip("s")))

        stop = threading.Event()
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
        restored += 1

    # Advance the global ID counter past all restored IDs so new schedules
    # don't collide with them.
    if max_n > 0:
        global _id_counter
        _id_counter = itertools.count(max_n + 1)

    return restored


@fir_ext.on("session_start")
def on_session_start(params: dict, ctx: fir_ext.Context) -> None:
    """Restore schedules that were active before a /reexec."""
    session_data = params.get("session_data") if params else None
    if not session_data:
        return
    serialized = session_data.get(_SESSION_DATA_KEY)
    if not serialized:
        return

    restored = _restore_schedules(serialized, ctx)
    if restored:
        _update_status(ctx)


@fir_ext.on("session_shutdown")
def on_session_shutdown(params: dict, ctx: fir_ext.Context) -> None:
    """Save active schedules so they can be restored after /reexec."""
    with _lock:
        if not _schedules:
            return
    serialized = _serialize_schedules()
    with contextlib.suppress(Exception):
        ctx.set_session_data(_SESSION_DATA_KEY, serialized)


# ---------------------------------------------------------------------------
# Auto-resume on transient provider errors
# ---------------------------------------------------------------------------
#
# When an LLM turn ends in a *retryable* provider error (Anthropic Overloaded /
# 529, rate limit / 429, transient 5xx) that the agent's own in-loop transport
# retry does NOT cover, the session would otherwise sit stuck waiting for a
# human. We auto-resume it with a backoff, bounded by a give-up window.
#
# Policy (user-locked — see DESIGN-provider-error-resume.md):
#   • Only retryable errors. Terminal errors (auth/400/context-length) are left
#     for the user; we just post a notice.
#   • Honour the provider's retry_after when given; otherwise use the fixed
#     escalating schedule below (no jitter — we are small-fish traffic).
#   • Give up after _AR_GIVEUP_WINDOW of continuous failure.
#   • Always on when this extension is loaded; no toggle.
#   • Reset on the first successful assistant turn.

# Fixed backoff schedule (seconds) used when the provider gives no retry_after.
# Escalates then holds steady at the last value.
_AR_BACKOFFS = [30, 60, 105, 120]

# Give up after this much continuous failure (first failure → now).
_AR_GIVEUP_WINDOW = timedelta(minutes=20)

_ar_lock = threading.Lock()
_ar_consecutive: int = 0
_ar_first_failure: datetime | None = None
_ar_entry_id: str | None = None


def _ar_backoff_seconds(consecutive: int, retry_after_ms: int) -> float:
    """Delay before the next resume attempt. Honour retry_after when given."""
    if retry_after_ms and retry_after_ms > 0:
        return retry_after_ms / 1000.0
    idx = min(max(consecutive - 1, 0), len(_AR_BACKOFFS) - 1)
    return float(_AR_BACKOFFS[idx])


def _reset_autoresume(ctx: fir_ext.Context | None = None) -> None:
    """Clear auto-resume state and cancel any pending resume timer."""
    global _ar_consecutive, _ar_first_failure, _ar_entry_id
    entry_id = None
    with _ar_lock:
        if _ar_consecutive == 0 and _ar_first_failure is None and _ar_entry_id is None:
            return
        _ar_consecutive = 0
        _ar_first_failure = None
        entry_id = _ar_entry_id
        _ar_entry_id = None
    if entry_id:
        with _lock:
            e = _schedules.pop(entry_id, None)
        if e is not None:
            e.stop_event.set()
        if ctx is not None:
            with contextlib.suppress(Exception):
                _update_status(ctx)


@fir_ext.on("provider_error")
def on_provider_error(params: dict, ctx: fir_ext.Context) -> None:
    """Auto-resume the session after a retryable provider/LLM turn error."""
    global _ar_consecutive, _ar_first_failure, _ar_entry_id
    params = params or {}
    kind = params.get("kind", "unknown")
    err_text = (params.get("error_text") or "").strip()

    if not params.get("retryable"):
        # Terminal error (auth/400/context-length): never auto-resume. Surface
        # it so the user can act.
        with contextlib.suppress(Exception):
            ctx.send_message(
                custom_type="provider_error",
                content=(
                    f"⛔ Provider error ({kind}) is not retryable — auto-resume "
                    f"skipped. {err_text[:200]}"
                ),
                display=True,
            )
        return

    now = _now()
    with _ar_lock:
        if _ar_first_failure is None:
            _ar_first_failure = now
        _ar_consecutive += 1
        n = _ar_consecutive
        first = _ar_first_failure
    elapsed = now - first

    # Give-up check: stop once we've been failing continuously past the window.
    if elapsed > _AR_GIVEUP_WINDOW:
        with contextlib.suppress(Exception):
            ctx.send_message(
                custom_type="provider_error",
                content=(
                    f"🛑 Provider has been failing ({kind}) for "
                    f"{_format_countdown(elapsed)} across {n} attempts — "
                    f"auto-resume given up. Prompt to retry manually. "
                    f"Last error: {err_text[:200]}"
                ),
                display=True,
            )
        _reset_autoresume(ctx)
        return

    retry_after_ms = int(params.get("retry_after_ms") or 0)
    delay = _ar_backoff_seconds(n, retry_after_ms)
    target = now + timedelta(seconds=delay)

    # Cancel any prior pending resume (shouldn't normally exist) and schedule a
    # fresh one that re-prompts "continue".
    stop = threading.Event()
    sid = _next_id()
    entry = _ScheduleEntry(
        id=sid, target=target, message=None, stop_event=stop, auto=True,
    )
    t = threading.Thread(
        target=_run_countdown,
        args=(sid, target, stop, ctx, None),
        daemon=True,
    )
    entry.thread = t

    old_id = None
    with _ar_lock:
        old_id = _ar_entry_id
        _ar_entry_id = sid
    if old_id:
        with _lock:
            old = _schedules.pop(old_id, None)
        if old is not None:
            old.stop_event.set()

    with _lock:
        _schedules[sid] = entry
    t.start()

    with contextlib.suppress(Exception):
        _update_status(ctx)
    with contextlib.suppress(Exception):
        src = "provider retry_after" if retry_after_ms > 0 else "backoff"
        ctx.send_message(
            custom_type="provider_error",
            content=(
                f"⏳ [{sid}] Provider error ({kind}, attempt {n}) — auto-resuming "
                f"in {_format_countdown(target - now)} ({src}). "
                f"Failing for {_format_countdown(elapsed)}; will give up after "
                f"{_format_countdown(_AR_GIVEUP_WINDOW)}."
            ),
            display=True,
        )


@fir_ext.on("message_end")
def on_message_end(params: dict, ctx: fir_ext.Context) -> None:
    """Reset auto-resume state on the first successful assistant turn."""
    params = params or {}
    if params.get("role") != "assistant":
        return
    stop_reason = params.get("stop_reason")
    # A successful (or merely non-error) assistant message means the provider
    # recovered — clear the failure counter/timer.
    if stop_reason and stop_reason != "error":
        _reset_autoresume(ctx)


fir_ext.run(name="schedule")
