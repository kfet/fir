#!/usr/bin/env python3
"""Unit tests for the schedule extension (multiple task support)."""

from __future__ import annotations

import os
import signal
import sys
import threading
import unittest
from datetime import datetime, timedelta, timezone
from unittest import mock

# Add the extension dir and SDK to the path so we can import schedule helpers.
_ext_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "builtin_extensions")
sys.path.insert(0, _ext_dir)
_sdk_dir = os.path.join(
    os.path.dirname(_ext_dir), "..", "pkg", "extension", "sdk", "python",
)
sys.path.insert(0, os.path.normpath(_sdk_dir))

# We need to prevent fir_ext.run() from blocking when schedule.py is imported.
# Import the real fir_ext SDK first, then import schedule with run() patched.
import importlib

import fir_ext

with mock.patch.object(fir_ext, "run"):
    schedule = importlib.import_module("schedule")


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _ctx():
    return mock.MagicMock(
        spec=["set_status", "continue_session", "send_user_message"],
    )


def _reset():
    """Reset module state between tests."""
    with schedule._lock:
        for e in schedule._schedules.values():
            e.stop_event.set()
        schedule._schedules.clear()


class _Timeout:
    """Per-test SIGALRM timeout (Unix only). No-op on Windows."""

    def __init__(self, seconds: int = 5):
        self.seconds = seconds
        self._old = None

    def __enter__(self):
        if hasattr(signal, "SIGALRM"):
            def _handler(signum, frame):
                raise TimeoutError(
                    f"Test timed out after {self.seconds}s"
                )
            self._old = signal.signal(signal.SIGALRM, _handler)
            signal.alarm(self.seconds)
        return self

    def __exit__(self, *exc):
        if hasattr(signal, "SIGALRM"):
            signal.alarm(0)
            if self._old is not None:
                signal.signal(signal.SIGALRM, self._old)
        return False


# ---------------------------------------------------------------------------
# Pure-function tests (no threads, no timeout needed)
# ---------------------------------------------------------------------------


class TestFormatCountdown(unittest.TestCase):
    def test_seconds_only(self):
        self.assertEqual(schedule._format_countdown(timedelta(seconds=5)), "5s")

    def test_minutes_seconds(self):
        self.assertEqual(
            schedule._format_countdown(timedelta(minutes=3, seconds=7)), "3m07s",
        )

    def test_hours_minutes_seconds(self):
        self.assertEqual(
            schedule._format_countdown(timedelta(hours=1, minutes=2, seconds=3)),
            "1h02m03s",
        )

    def test_zero(self):
        self.assertEqual(schedule._format_countdown(timedelta(0)), "0s")

    def test_negative_clamps(self):
        self.assertEqual(
            schedule._format_countdown(timedelta(seconds=-10)), "0s",
        )


class TestFormatTime(unittest.TestCase):
    def test_am(self):
        dt = datetime(2026, 3, 9, 9, 5, tzinfo=timezone.utc)
        self.assertEqual(schedule._format_time(dt), "9:05 AM")

    def test_pm(self):
        dt = datetime(2026, 3, 9, 14, 30, tzinfo=timezone.utc)
        self.assertEqual(schedule._format_time(dt), "2:30 PM")

    def test_noon(self):
        dt = datetime(2026, 3, 9, 12, 0, tzinfo=timezone.utc)
        self.assertEqual(schedule._format_time(dt), "12:00 PM")

    def test_midnight(self):
        dt = datetime(2026, 3, 9, 0, 0, tzinfo=timezone.utc)
        self.assertEqual(schedule._format_time(dt), "12:00 AM")


class TestParseTarget(unittest.TestCase):
    def setUp(self):
        self._fixed = datetime(2026, 3, 9, 10, 0, 0, tzinfo=timezone.utc)
        self._patch = mock.patch.object(
            schedule, "_now", return_value=self._fixed,
        )
        self._patch.start()

    def tearDown(self):
        self._patch.stop()

    def test_minutes(self):
        t = schedule._parse_target("45m")
        self.assertEqual(t, self._fixed + timedelta(minutes=45))

    def test_hours(self):
        t = schedule._parse_target("2h")
        self.assertEqual(t, self._fixed + timedelta(hours=2))

    def test_hours_minutes(self):
        t = schedule._parse_target("1h30m")
        self.assertEqual(t, self._fixed + timedelta(hours=1, minutes=30))

    def test_seconds(self):
        t = schedule._parse_target("30s")
        self.assertEqual(t, self._fixed + timedelta(seconds=30))

    def test_minutes_seconds(self):
        t = schedule._parse_target("2m30s")
        self.assertEqual(t, self._fixed + timedelta(minutes=2, seconds=30))

    def test_hours_minutes_seconds(self):
        t = schedule._parse_target("1h5m10s")
        self.assertEqual(
            t, self._fixed + timedelta(hours=1, minutes=5, seconds=10),
        )

    def test_pm(self):
        t = schedule._parse_target("2pm")
        assert t is not None
        self.assertEqual(t.hour, 14)

    def test_pm_with_minutes(self):
        t = schedule._parse_target("2:30pm")
        assert t is not None
        self.assertEqual(t.hour, 14)
        self.assertEqual(t.minute, 30)

    def test_am(self):
        t = schedule._parse_target("9am")
        assert t is not None
        self.assertEqual(t.hour, 9)
        self.assertEqual(t.day, 10)

    def test_12pm(self):
        t = schedule._parse_target("12pm")
        assert t is not None
        self.assertEqual(t.hour, 12)
        self.assertEqual(t.day, 9)

    def test_12am(self):
        t = schedule._parse_target("12am")
        assert t is not None
        self.assertEqual(t.hour, 0)
        self.assertEqual(t.day, 10)

    def test_24h(self):
        t = schedule._parse_target("14:00")
        assert t is not None
        self.assertEqual(t.hour, 14)

    def test_24h_past(self):
        t = schedule._parse_target("09:00")
        assert t is not None
        self.assertEqual(t.day, 10)

    def test_empty(self):
        self.assertIsNone(schedule._parse_target(""))

    def test_garbage(self):
        self.assertIsNone(schedule._parse_target("banana"))

    def test_bare_number(self):
        self.assertIsNone(schedule._parse_target("42"))


# ---------------------------------------------------------------------------
# Command tests — mock threading.Thread so no real threads are spawned.
# ---------------------------------------------------------------------------


class _FakeThread:
    """Fake thread that records start() but never actually runs."""

    def __init__(self, target=None, args=(), daemon=False, **kw):
        self.target = target
        self.args = args
        self.daemon = daemon
        self._started = False

    def start(self):
        self._started = True


class TestCmdSchedule(unittest.TestCase):
    def setUp(self):
        self._fixed = datetime(2026, 3, 9, 10, 0, 0, tzinfo=timezone.utc)
        self._now_patch = mock.patch.object(
            schedule, "_now", return_value=self._fixed,
        )
        self._thread_patch = mock.patch(
            "threading.Thread", side_effect=_FakeThread,
        )
        self._now_patch.start()
        self._thread_patch.start()
        _reset()

    def tearDown(self):
        _reset()
        self._thread_patch.stop()
        self._now_patch.stop()

    def test_no_args_no_schedule(self):
        result = schedule.cmd_schedule([], _ctx())
        self.assertIn("No schedules active", result["message"])

    def test_cancel_nothing(self):
        result = schedule.cmd_schedule(["cancel"], _ctx())
        self.assertEqual(result["message"], "No schedules to cancel.")

    def test_invalid_time(self):
        result = schedule.cmd_schedule(["banana"], _ctx())
        self.assertIn("Could not parse", result["message"])

    def test_schedule_relative(self):
        result = schedule.cmd_schedule(["30m"], _ctx())
        self.assertIn("Scheduled", result["message"])
        self.assertIn("continue", result["message"])
        self.assertRegex(result["message"], r"\[s\d+\]")

    def test_schedule_with_message(self):
        result = schedule.cmd_schedule(["10m", "run", "the", "tests"], _ctx())
        self.assertIn("send message: run the tests", result["message"])

    def test_multiple_schedules(self):
        ctx = _ctx()
        r1 = schedule.cmd_schedule(["30m"], ctx)
        r2 = schedule.cmd_schedule(["1h"], ctx)
        id1 = r1["message"].split("[")[1].split("]")[0]
        id2 = r2["message"].split("[")[1].split("]")[0]
        self.assertNotEqual(id1, id2)
        with schedule._lock:
            self.assertEqual(len(schedule._schedules), 2)

    def test_list_multiple(self):
        ctx = _ctx()
        schedule.cmd_schedule(["30m"], ctx)
        schedule.cmd_schedule(["1h", "deploy"], ctx)
        result = schedule.cmd_schedule([], ctx)
        self.assertIn("Active schedules", result["message"])
        self.assertIn("continue", result["message"])
        self.assertIn("deploy", result["message"])

    def test_cancel_by_id(self):
        ctx = _ctx()
        r1 = schedule.cmd_schedule(["30m"], ctx)
        r2 = schedule.cmd_schedule(["1h"], ctx)
        id1 = r1["message"].split("[")[1].split("]")[0]
        id2 = r2["message"].split("[")[1].split("]")[0]
        result = schedule.cmd_schedule(["cancel", id1], ctx)
        self.assertIn(f"[{id1}] cancelled", result["message"])
        with schedule._lock:
            self.assertIn(id2, schedule._schedules)
            self.assertNotIn(id1, schedule._schedules)

    def test_cancel_all(self):
        ctx = _ctx()
        schedule.cmd_schedule(["30m"], ctx)
        schedule.cmd_schedule(["1h"], ctx)
        schedule.cmd_schedule(["2h"], ctx)
        result = schedule.cmd_schedule(["cancel", "all"], ctx)
        self.assertIn("Cancelled all 3", result["message"])
        with schedule._lock:
            self.assertEqual(len(schedule._schedules), 0)

    def test_cancel_ambiguous(self):
        ctx = _ctx()
        schedule.cmd_schedule(["30m"], ctx)
        schedule.cmd_schedule(["1h"], ctx)
        result = schedule.cmd_schedule(["cancel"], ctx)
        self.assertIn("Multiple schedules", result["message"])

    def test_cancel_single_no_id(self):
        ctx = _ctx()
        schedule.cmd_schedule(["30m"], ctx)
        result = schedule.cmd_schedule(["cancel"], ctx)
        self.assertIn("cancelled", result["message"])
        with schedule._lock:
            self.assertEqual(len(schedule._schedules), 0)

    def test_cancel_bad_id(self):
        schedule.cmd_schedule(["30m"], _ctx())
        result = schedule.cmd_schedule(["cancel", "s999"], _ctx())
        self.assertIn("No schedule with id", result["message"])

    def test_cancel_all_empty(self):
        result = schedule.cmd_schedule(["cancel", "all"], _ctx())
        self.assertEqual(result["message"], "No schedules to cancel.")


# ---------------------------------------------------------------------------
# Countdown thread tests — run _run_countdown directly with already-expired
# targets so they complete instantly. Use a timeout as a safety net.
# ---------------------------------------------------------------------------


class TestCountdownThread(unittest.TestCase):
    def setUp(self):
        _reset()

    def tearDown(self):
        _reset()

    def test_fires_continue(self):
        with _Timeout(5):
            ctx = mock.MagicMock()
            stop = threading.Event()
            target = datetime.now(tz=timezone.utc) - timedelta(seconds=1)
            schedule._run_countdown("t1", target, stop, ctx)
            ctx.send_user_message.assert_called_once_with("continue")

    def test_fires_message(self):
        with _Timeout(5):
            ctx = mock.MagicMock()
            stop = threading.Event()
            target = datetime.now(tz=timezone.utc) - timedelta(seconds=1)
            schedule._run_countdown("t2", target, stop, ctx, message="do it")
            ctx.send_user_message.assert_called_once_with("do it")
            ctx.continue_session.assert_not_called()

    def test_cancel_no_fire(self):
        with _Timeout(5):
            ctx = mock.MagicMock()
            stop = threading.Event()
            stop.set()
            target = datetime.now(tz=timezone.utc) + timedelta(hours=1)
            schedule._run_countdown("t3", target, stop, ctx)
            ctx.continue_session.assert_not_called()
            ctx.send_user_message.assert_not_called()


# ---------------------------------------------------------------------------
# Integration test — real threads, real timers (short durations).
# ---------------------------------------------------------------------------


class TestIntegration(unittest.TestCase):
    """End-to-end test using real threads with short timers."""

    def setUp(self):
        _reset()

    def tearDown(self):
        _reset()

    def test_schedule_fires_continue(self):
        """Schedule 1s, wait for the thread to fire send_user_message('continue')."""
        with _Timeout(5):
            ctx = mock.MagicMock()
            result = schedule.cmd_schedule(["1s"], ctx)
            self.assertIn("Scheduled", result["message"])

            # Wait for the countdown thread to finish and fire.
            deadline = datetime.now(tz=timezone.utc) + timedelta(seconds=4)
            while datetime.now(tz=timezone.utc) < deadline:
                if ctx.send_user_message.called:
                    break
                threading.Event().wait(0.1)

            ctx.send_user_message.assert_called_once_with("continue")
            with schedule._lock:
                self.assertEqual(len(schedule._schedules), 0)

    def test_schedule_fires_message(self):
        """Schedule 1s with a message, wait for send_user_message."""
        with _Timeout(5):
            ctx = mock.MagicMock()
            result = schedule.cmd_schedule(["1s", "hello", "world"], ctx)
            self.assertIn("send message", result["message"])

            deadline = datetime.now(tz=timezone.utc) + timedelta(seconds=4)
            while datetime.now(tz=timezone.utc) < deadline:
                if ctx.send_user_message.called:
                    break
                threading.Event().wait(0.1)

            ctx.send_user_message.assert_called_once_with("hello world")
            ctx.continue_session.assert_not_called()

    def test_multiple_schedules_fire_independently(self):
        """Two schedules at different times both fire."""
        with _Timeout(10):
            ctx = mock.MagicMock()
            schedule.cmd_schedule(["1s", "first"], ctx)
            schedule.cmd_schedule(["2s", "second"], ctx)
            with schedule._lock:
                self.assertEqual(len(schedule._schedules), 2)

            # Wait for both to fire.
            deadline = datetime.now(tz=timezone.utc) + timedelta(seconds=5)
            while datetime.now(tz=timezone.utc) < deadline:
                if ctx.send_user_message.call_count >= 2:
                    break
                threading.Event().wait(0.1)

            self.assertEqual(ctx.send_user_message.call_count, 2)
            calls = [c.args[0] for c in ctx.send_user_message.call_args_list]
            self.assertIn("first", calls)
            self.assertIn("second", calls)
            with schedule._lock:
                self.assertEqual(len(schedule._schedules), 0)

    def test_cancel_before_fire(self):
        """Cancel a schedule before it fires; verify no action taken."""
        with _Timeout(5):
            ctx = mock.MagicMock()
            r = schedule.cmd_schedule(["3s"], ctx)
            sid = r["message"].split("[")[1].split("]")[0]

            # Cancel immediately.
            result = schedule.cmd_schedule(["cancel", sid], ctx)
            self.assertIn("cancelled", result["message"])

            # Wait a bit to confirm it doesn't fire.
            threading.Event().wait(1.0)
            ctx.continue_session.assert_not_called()
            ctx.send_user_message.assert_not_called()


if __name__ == "__main__":
    unittest.main()
