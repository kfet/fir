#!/usr/bin/env python3
"""Tests for the plan_nudger builtin extension."""

import os
import sys
import time
import unittest
from unittest import mock

_ext_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "builtin_extensions")
_sdk_dir = os.path.join(
    os.path.dirname(os.path.abspath(__file__)),
    "..",
    "..",
    "extension",
    "sdk",
    "python",
)
sys.path.insert(0, _ext_dir)
sys.path.insert(0, _sdk_dir)

import fir_ext


def _load_plan_nudger():
    """(Re-)import plan_nudger.py, resetting its global state and capturing handlers."""
    if "plan_nudger" in sys.modules:
        del sys.modules["plan_nudger"]
    before = {k: id(v) for k, v in fir_ext._event_handlers.items()}
    with mock.patch.object(fir_ext, "run"):
        import plan_nudger
    # Handlers whose function identity changed (or are new) were registered by this import.
    new_handlers = {
        k: v for k, v in fir_ext._event_handlers.items()
        if k not in before or id(v) != before[k]
    }
    return plan_nudger, new_handlers


class TestRegistration(unittest.TestCase):
    def test_registers_expected_events(self):
        _, handlers = _load_plan_nudger()
        for event in ("session_update", "turn_end", "agent_end"):
            self.assertIn(event, handlers, f"{event} handler should be registered")


class TestSessionUpdateHandler(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_plan_nudger()

    def test_resets_turn_counter(self):
        handler = self.handlers["session_update"]
        ctx = mock.MagicMock()
        self.mod.turns_since_update = 10
        handler({"plan": {"total": 5, "completed": 2}}, ctx)
        self.assertEqual(self.mod.turns_since_update, 0)
        self.assertEqual(self.mod.plan_total, 5)
        self.assertEqual(self.mod.plan_completed, 2)

    def test_ignores_missing_plan(self):
        handler = self.handlers["session_update"]
        ctx = mock.MagicMock()
        self.mod.turns_since_update = 7
        handler({}, ctx)
        self.assertEqual(self.mod.turns_since_update, 7)

    def test_updates_last_update_time(self):
        handler = self.handlers["session_update"]
        ctx = mock.MagicMock()
        old_time = self.mod.last_update_time
        time.sleep(0.01)
        handler({"plan": {"total": 1, "completed": 0}}, ctx)
        self.assertGreater(self.mod.last_update_time, old_time)


class TestTurnEndHandler(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_plan_nudger()

    def test_increments_turn_counter(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.plan_total = 3
        self.mod.plan_completed = 1
        self.assertEqual(self.mod.turns_since_update, 0)
        handler({}, ctx)
        self.assertEqual(self.mod.turns_since_update, 1)
        ctx.send_message.assert_not_called()

    def test_no_nudge_when_plan_complete(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.plan_total = 3
        self.mod.plan_completed = 3
        self.mod.turns_since_update = 100
        handler({}, ctx)
        ctx.send_message.assert_not_called()

    def test_nudge_after_turn_threshold(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.plan_total = 5
        self.mod.plan_completed = 2
        self.mod.turns_since_update = self.mod.NUDGE_TURN_THRESHOLD - 1
        handler({}, ctx)
        ctx.send_message.assert_called_once()
        args = ctx.send_message.call_args
        self.assertEqual(args[0][0], "nudge")
        self.assertIn("plan", args[0][1].lower())

    def test_nudge_after_time_threshold(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.plan_total = 5
        self.mod.plan_completed = 2
        self.mod.turns_since_update = 0
        self.mod.last_update_time = time.monotonic() - self.mod.NUDGE_TIME_THRESHOLD - 1
        handler({}, ctx)
        ctx.send_message.assert_called_once()

    def test_nudge_resets_counters(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.plan_total = 5
        self.mod.plan_completed = 2
        self.mod.turns_since_update = self.mod.NUDGE_TURN_THRESHOLD - 1
        old_time = self.mod.last_update_time
        time.sleep(0.01)
        handler({}, ctx)
        self.assertEqual(self.mod.turns_since_update, 0)
        self.assertGreater(self.mod.last_update_time, old_time)

    def test_no_nudge_below_thresholds(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.plan_total = 5
        self.mod.plan_completed = 2
        self.mod.turns_since_update = 0
        self.mod.last_update_time = time.monotonic()
        handler({}, ctx)
        ctx.send_message.assert_not_called()


class TestAgentEndHandler(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_plan_nudger()

    def test_nudge_when_plan_incomplete(self):
        handler = self.handlers["agent_end"]
        ctx = mock.MagicMock()
        self.mod.plan_total = 5
        self.mod.plan_completed = 3
        handler({}, ctx)
        ctx.send_message.assert_called_once()
        args = ctx.send_message.call_args
        self.assertEqual(args[0][0], "nudge")
        self.assertIn("incomplete", args[0][1].lower())

    def test_no_nudge_when_plan_complete(self):
        handler = self.handlers["agent_end"]
        ctx = mock.MagicMock()
        self.mod.plan_total = 5
        self.mod.plan_completed = 5
        handler({}, ctx)
        ctx.send_message.assert_not_called()

    def test_no_nudge_when_no_plan(self):
        handler = self.handlers["agent_end"]
        ctx = mock.MagicMock()
        self.mod.plan_total = 0
        self.mod.plan_completed = 0
        handler({}, ctx)
        ctx.send_message.assert_not_called()


if __name__ == "__main__":
    unittest.main()
