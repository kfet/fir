#!/usr/bin/env python3
"""Unit tests for the schedule extension (multiple task support)."""

from __future__ import annotations

import os
import sys
import unittest
from datetime import UTC, datetime, timedelta
from unittest import mock

# Add the extension dir and SDK to the path so we can import schedule helpers.
_ext_dir = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, _ext_dir)
_sdk_dir = os.path.join(
    os.path.dirname(_ext_dir), "..", "pkg", "extension", "sdk", "python",
)
sys.path.insert(0, os.path.normpath(_sdk_dir))

# We need to mock fir_ext before importing schedule, since schedule calls
# fir_ext.run() and decorators at import time.
fir_ext_mock = mock.MagicMock()
fir_ext_mock.command = lambda **kw: lambda fn: fn
fir_ext_mock.run = mock.MagicMock()
sys.modules["fir_ext"] = fir_ext_mock

# Now import the module under test.
import importlib  # noqa: E402

schedule = importlib.import_module("schedule")


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
        dt = datetime(2026, 3, 9, 9, 5, tzinfo=UTC)
        self.assertEqual(schedule._format_time(dt), "9:05 AM")

    def test_pm(self):
        dt = datetime(2026, 3, 9, 14, 30, tzinfo=UTC)
        self.assertEqual(schedule._format_time(dt), "2:30 PM")

    def test_noon(self):
        dt = datetime(2026, 3, 9, 12, 0, tzinfo=UTC)
        self.assertEqual(schedule._format_time(dt), "12:00 PM")

    def test_midnight(self):
        dt = datetime(2026, 3, 9, 0, 0, tzinfo=UTC)
        self.assertEqual(schedule._format_time(dt), "12:00 AM")


class TestParseTarget(unittest.TestCase):
    """Test _parse_target with a fixed _now()."""

    def setUp(self):
        self._fixed = datetime(2026, 3, 9, 10, 0, 0, tzinfo=UTC)
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
        self.assertEqual(t.minute, 0)

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
        self.assertEqual(t.minute, 0)

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


class TestCmdSchedule(unittest.TestCase):
    """Test cmd_schedule with multiple schedule support."""

    def setUp(self):
        self._fixed = datetime(2026, 3, 9, 10, 0, 0, tzinfo=UTC)
        self._patch = mock.patch.object(
            schedule, "_now", return_value=self._fixed,
        )
        self._patch.start()
        _reset()

    def tearDown(self):
        self._patch.stop()
        _reset()

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
        ctx = _ctx()
        result = schedule.cmd_schedule(["30m"], ctx)
        self.assertIn("Scheduled", result["message"])
        self.assertIn("continue", result["message"])
        # Should contain an ID like [s1]
        self.assertRegex(result["message"], r"\[s\d+\]")

    def test_schedule_with_message(self):
        ctx = _ctx()
        result = schedule.cmd_schedule(["10m", "run", "the", "tests"], ctx)
        self.assertIn("send message: run the tests", result["message"])

    def test_multiple_schedules(self):
        ctx = _ctx()
        r1 = schedule.cmd_schedule(["30m"], ctx)
        r2 = schedule.cmd_schedule(["1h"], ctx)
        # Both should succeed with different IDs
        id1 = r1["message"].split("[")[1].split("]")[0]
        id2 = r2["message"].split("[")[1].split("]")[0]
        self.assertNotEqual(id1, id2)
        # Status should list both
        with schedule._lock:
            self.assertEqual(len(schedule._schedules), 2)

    def test_list_multiple(self):
        ctx = _ctx()
        schedule.cmd_schedule(["30m"], ctx)
        schedule.cmd_schedule(["1h", "deploy"], ctx)
        result = schedule.cmd_schedule([], ctx)
        self.assertIn("Active schedules", result["message"])
        # Should show both entries
        self.assertIn("continue", result["message"])
        self.assertIn("deploy", result["message"])

    def test_cancel_by_id(self):
        ctx = _ctx()
        r1 = schedule.cmd_schedule(["30m"], ctx)
        r2 = schedule.cmd_schedule(["1h"], ctx)
        id1 = r1["message"].split("[")[1].split("]")[0]
        id2 = r2["message"].split("[")[1].split("]")[0]
        # Cancel first
        result = schedule.cmd_schedule(["cancel", id1], ctx)
        self.assertIn(f"[{id1}] cancelled", result["message"])
        # Second should still be active
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
        """cancel with no id and multiple schedules should list them."""
        ctx = _ctx()
        schedule.cmd_schedule(["30m"], ctx)
        schedule.cmd_schedule(["1h"], ctx)
        result = schedule.cmd_schedule(["cancel"], ctx)
        self.assertIn("Multiple schedules", result["message"])

    def test_cancel_single_no_id(self):
        """cancel with no id and exactly one schedule should cancel it."""
        ctx = _ctx()
        schedule.cmd_schedule(["30m"], ctx)
        result = schedule.cmd_schedule(["cancel"], ctx)
        self.assertIn("cancelled", result["message"])
        with schedule._lock:
            self.assertEqual(len(schedule._schedules), 0)

    def test_cancel_bad_id(self):
        ctx = _ctx()
        schedule.cmd_schedule(["30m"], ctx)
        result = schedule.cmd_schedule(["cancel", "s999"], ctx)
        self.assertIn("No schedule with id", result["message"])

    def test_cancel_all_empty(self):
        result = schedule.cmd_schedule(["cancel", "all"], _ctx())
        self.assertEqual(result["message"], "No schedules to cancel.")


class TestCountdownThread(unittest.TestCase):
    """Test _run_countdown fires the right action."""

    def setUp(self):
        _reset()

    def tearDown(self):
        _reset()

    def test_fires_continue(self):
        ctx = mock.MagicMock()
        stop = threading.Event()
        target = datetime.now(tz=UTC) - timedelta(seconds=1)
        schedule._run_countdown("test1", target, stop, ctx)
        ctx.continue_session.assert_called_once()
        ctx.send_user_message.assert_not_called()

    def test_fires_message(self):
        ctx = mock.MagicMock()
        stop = threading.Event()
        target = datetime.now(tz=UTC) - timedelta(seconds=1)
        schedule._run_countdown("test2", target, stop, ctx, message="do it")
        ctx.send_user_message.assert_called_once_with("do it")
        ctx.continue_session.assert_not_called()

    def test_cancel_no_fire(self):
        ctx = mock.MagicMock()
        stop = threading.Event()
        stop.set()
        target = datetime.now(tz=UTC) + timedelta(hours=1)
        schedule._run_countdown("test3", target, stop, ctx)
        ctx.continue_session.assert_not_called()
        ctx.send_user_message.assert_not_called()


import threading  # noqa: E402

if __name__ == "__main__":
    unittest.main()
