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


def _prime_idle(mod):
    """Force the next turn_end to nudge by simulating prior idle turns."""
    mod.idle_turns = mod.IDLE_TURN_THRESHOLD - 1
    mod.tool_used_this_turn = False
    mod.last_active_time = time.monotonic()


class TestRegistration(unittest.TestCase):
    def test_registers_expected_events(self):
        _, handlers = _load_plan_nudger()
        for event in ("session_update", "turn_end", "agent_end", "tool_execution_end"):
            self.assertIn(event, handlers, f"{event} handler should be registered")


class TestSessionUpdateHandler(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_plan_nudger()

    def test_resets_idle_counter(self):
        handler = self.handlers["session_update"]
        ctx = mock.MagicMock()
        self.mod.idle_turns = 10
        handler({"plan": {"total": 5, "completed": 2}}, ctx)
        self.assertEqual(self.mod.idle_turns, 0)
        self.assertEqual(self.mod.plan_total, 5)
        self.assertEqual(self.mod.plan_completed, 2)

    def test_ignores_missing_plan(self):
        handler = self.handlers["session_update"]
        ctx = mock.MagicMock()
        self.mod.idle_turns = 7
        handler({}, ctx)
        self.assertEqual(self.mod.idle_turns, 7)

    def test_updates_last_active_time(self):
        handler = self.handlers["session_update"]
        ctx = mock.MagicMock()
        old_time = self.mod.last_active_time
        time.sleep(0.01)
        handler({"plan": {"total": 1, "completed": 0}}, ctx)
        self.assertGreater(self.mod.last_active_time, old_time)

    def test_progress_resets_stagnation(self):
        handler = self.handlers["session_update"]
        ctx = mock.MagicMock()
        self.mod.plan_completed = 1
        self.mod.nudges_without_progress = 3
        handler({"plan": {"total": 5, "completed": 2}}, ctx)
        self.assertEqual(self.mod.nudges_without_progress, 0)

    def test_no_progress_keeps_stagnation(self):
        handler = self.handlers["session_update"]
        ctx = mock.MagicMock()
        self.mod.plan_completed = 2
        self.mod.nudges_without_progress = 3
        handler({"plan": {"total": 5, "completed": 2}}, ctx)
        self.assertEqual(self.mod.nudges_without_progress, 3)


class TestToolExecutionEndHandler(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_plan_nudger()

    def test_marks_tool_used_this_turn(self):
        handler = self.handlers["tool_execution_end"]
        ctx = mock.MagicMock()
        self.mod.tool_used_this_turn = False
        handler({"tool_name": "Bash", "is_error": False}, ctx)
        self.assertTrue(self.mod.tool_used_this_turn)
        self.assertEqual(self.mod.last_tool_name, "Bash")
        self.assertFalse(self.mod.last_tool_is_error)

    def test_records_error_status(self):
        handler = self.handlers["tool_execution_end"]
        ctx = mock.MagicMock()
        handler({"tool_name": "Read", "is_error": True}, ctx)
        self.assertTrue(self.mod.last_tool_is_error)
        self.assertEqual(self.mod.last_tool_name, "Read")

    def test_updates_last_active_time(self):
        handler = self.handlers["tool_execution_end"]
        ctx = mock.MagicMock()
        self.mod.last_active_time = time.monotonic() - 100
        handler({"tool_name": "Bash"}, ctx)
        self.assertGreater(
            self.mod.last_active_time, time.monotonic() - 1,
        )


class TestTurnEndHandler(unittest.TestCase):
    """The core anti-stuck logic."""

    def setUp(self):
        self.mod, self.handlers = _load_plan_nudger()
        # A plan with remaining work, so nudges are possible.
        self.mod.plan_total = 5
        self.mod.plan_completed = 2

    def test_tool_turn_is_never_a_nudge(self):
        """A turn that ran a tool is a healthy loop tick — no nudge.

        This is the key fix: previously a turn-end with tool calls could
        still trip the nudge because the threshold was a raw turn counter.
        """
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        # Simulate many past idle turns, then a tool-using turn.
        self.mod.idle_turns = 100
        self.mod.tool_used_this_turn = True
        self.mod.last_active_time = time.monotonic() - 10_000
        handler({}, ctx)
        ctx.send_message.assert_not_called()
        ctx.prepend.assert_not_called()
        # Counter was reset by the active tick.
        self.assertEqual(self.mod.idle_turns, 0)
        # Per-turn flag was cleared for the next turn.
        self.assertFalse(self.mod.tool_used_this_turn)

    def test_no_nudge_on_first_idle_turn(self):
        """Nudge only after N consecutive idle turns, not the first."""
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.idle_turns = 0
        self.mod.tool_used_this_turn = False
        self.mod.last_active_time = time.monotonic()
        handler({}, ctx)
        ctx.send_message.assert_not_called()
        ctx.prepend.assert_not_called()
        self.assertEqual(self.mod.idle_turns, 1)

    def test_nudge_after_idle_threshold(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        _prime_idle(self.mod)
        handler({}, ctx)
        ctx.send_message.assert_called_once()
        args = ctx.send_message.call_args
        self.assertEqual(args[0][0], "nudge")
        msg = args[0][1]
        # Neutral tag, never the word "stuck".
        self.assertIn("[CONTINUE]", msg)
        self.assertNotIn("stuck", msg.lower())
        self.assertNotIn("STUCK", msg)
        # Unskippable: must contain a tool call.
        self.assertIn("MUST contain a tool call", msg)
        # No "rewrite from scratch" advice.
        self.assertNotIn("rewrite", msg.lower())
        self.assertNotIn("from scratch", msg.lower())

    def test_nudge_after_time_threshold(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.idle_turns = 0
        self.mod.tool_used_this_turn = False
        self.mod.last_active_time = (
            time.monotonic() - self.mod.NUDGE_TIME_THRESHOLD - 1
        )
        handler({}, ctx)
        ctx.send_message.assert_called_once()

    def test_nudge_includes_last_tool_hint(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.last_tool_name = "Bash"
        self.mod.last_tool_is_error = False
        _prime_idle(self.mod)
        handler({}, ctx)
        msg = ctx.send_message.call_args[0][1]
        self.assertIn("Bash", msg)
        self.assertIn("ok", msg)

    def test_nudge_includes_error_hint(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.last_tool_name = "Bash"
        self.mod.last_tool_is_error = True
        _prime_idle(self.mod)
        handler({}, ctx)
        msg = ctx.send_message.call_args[0][1]
        self.assertIn("Bash", msg)
        self.assertIn("error", msg)

    def test_nudge_without_prior_tool(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.last_tool_name = ""
        _prime_idle(self.mod)
        handler({}, ctx)
        msg = ctx.send_message.call_args[0][1]
        self.assertIn("No tool has run yet", msg)

    def test_nudge_resets_counters(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        _prime_idle(self.mod)
        old_time = self.mod.last_active_time
        time.sleep(0.01)
        handler({}, ctx)
        self.assertEqual(self.mod.idle_turns, 0)
        self.assertGreater(self.mod.last_active_time, old_time)

    def test_no_nudge_when_plan_complete(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.plan_total = 3
        self.mod.plan_completed = 3
        _prime_idle(self.mod)
        handler({}, ctx)
        ctx.send_message.assert_not_called()
        ctx.prepend.assert_not_called()

    def test_escalation_to_warn(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.nudges_without_progress = self.mod.STAGNATION_WARN_THRESHOLD - 1
        _prime_idle(self.mod)
        handler({}, ctx)
        msg = ctx.send_message.call_args[0][1]
        # WARN copy mentions the pause count ("X times").
        self.assertIn("times", msg)
        self.assertIn("MUST contain a tool call", msg)
        self.assertNotIn("rewrite", msg.lower())
        ctx.prepend.assert_not_called()

    def test_escalation_to_crit_uses_prepend(self):
        handler = self.handlers["turn_end"]
        ctx = mock.MagicMock()
        self.mod.nudges_without_progress = self.mod.STAGNATION_CRIT_THRESHOLD - 1
        _prime_idle(self.mod)
        handler({}, ctx)
        ctx.prepend.assert_called_once()
        msg = ctx.prepend.call_args[0][0]
        self.assertIn("[CONTINUE]", msg)
        self.assertIn("MUST contain a tool call", msg)
        self.assertNotIn("rewrite", msg.lower())
        self.assertNotIn("stuck", msg.lower())
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
        msg = args[0][1]
        self.assertIn("[CONTINUE]", msg)
        self.assertIn("incomplete", msg.lower())
        self.assertIn("MUST contain a tool call", msg)
        self.assertNotIn("stuck", msg.lower())

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
