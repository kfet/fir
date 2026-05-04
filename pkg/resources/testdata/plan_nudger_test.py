#!/usr/bin/env python3
"""Tests for the plan_nudger builtin extension.

The redesigned plan-nudger keeps main's firing rules (idle-turn
threshold, wall-clock backstop, stagnation tracking) but replaces the
loud ``[CONTINUE]`` voice with a calm, collaborator-to-collaborator
steer.  These tests exercise both surfaces:

* **firing rules** — never on tool-using turns, fires after
  ``IDLE_TURN_THRESHOLD`` consecutive idle turns or
  ``NUDGE_TIME_THRESHOLD`` seconds of wall-clock idleness, only when
  a plan is in flight, and ``agent_end`` always fires when in flight;
* **body composition** — fixed tag, counter line that grows with
  whichever counters are non-trivial, optional ``progress_metric``
  tip when the AI hasn't set one, and the ``self_handoff`` line only
  once stagnation is real.

The "must not contain" assertions are the hardest commitment in the
file — they pin the *psychology* fix (no MUST, no [CONTINUE], no
"stuck", no "do not narrate") so a future copy-edit can't quietly
re-introduce the parental voice.
"""

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
    """(Re-)import plan_nudger.py with a fresh module state and capture
    the event handlers it registered during import.

    Forcing a re-import is what gives every test an isolated copy of
    the module-level mutable globals (``plan_total``, ``idle_turns``,
    etc.) without us having to manually reset them in each setUp.
    """
    if "plan_nudger" in sys.modules:
        del sys.modules["plan_nudger"]
    before = {k: id(v) for k, v in fir_ext._event_handlers.items()}
    with mock.patch.object(fir_ext, "run"):
        import plan_nudger
    new_handlers = {
        k: v
        for k, v in fir_ext._event_handlers.items()
        if k not in before or id(v) != before[k]
    }
    return plan_nudger, new_handlers


def _prime_idle(mod):
    """Force the next ``turn_end`` to fire by simulating prior idleness."""
    mod.idle_turns = mod.IDLE_TURN_THRESHOLD - 1
    mod.tool_used_this_turn = False
    mod.last_active_time = time.monotonic()


# ---------------------------------------------------------------------------
# Registration
# ---------------------------------------------------------------------------


class TestRegistration(unittest.TestCase):
    """Confirm the extension subscribes to exactly the events it needs.

    ``user_prompt`` is deliberately *not* subscribed — the AI composes
    the plan with whatever metadata it wants reflected back; the
    extension never sniffs user input.
    """

    def test_registers_expected_events(self):
        _, handlers = _load_plan_nudger()
        for event in ("session_update", "tool_execution_end", "turn_end", "agent_end"):
            self.assertIn(event, handlers, f"{event} handler should be registered")

    def test_does_not_register_user_prompt(self):
        _, handlers = _load_plan_nudger()
        self.assertNotIn("user_prompt", handlers)


# ---------------------------------------------------------------------------
# session_update — bookkeeping only, never fires a steer
# ---------------------------------------------------------------------------


class TestSessionUpdateHandler(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_plan_nudger()
        self.handler = self.handlers["session_update"]

    def _send(self, total, completed, metadata=None):
        self.handler(
            {
                "type": "plan_update",
                "plan": {"total": total, "completed": completed, "metadata": metadata or {}},
            },
            mock.MagicMock(),
        )

    def test_ignores_non_plan_updates(self):
        # session_update fires for several event types — only
        # plan_update should affect counters.
        self.handler({"type": "session_named", "session_name": "x"}, mock.MagicMock())
        self.assertEqual(self.mod.plan_total, 0)

    def test_snapshots_plan_state(self):
        self._send(total=5, completed=2, metadata={"progress_metric": "x"})
        self.assertEqual(self.mod.plan_total, 5)
        self.assertEqual(self.mod.plan_completed, 2)
        self.assertEqual(self.mod.plan_metadata.get("progress_metric"), "x")

    def test_resets_idle_counter(self):
        # A plan update is itself a sign of forward motion.
        self.mod.idle_turns = 3
        self._send(total=2, completed=0)
        self.assertEqual(self.mod.idle_turns, 0)

    def test_progress_resets_stagnation(self):
        self.mod.nudges_without_progress = 4
        self._send(total=5, completed=0)
        self._send(total=5, completed=1)
        self.assertEqual(self.mod.nudges_without_progress, 0)

    def test_no_progress_keeps_stagnation(self):
        # Touching the plan without finishing an item must NOT clear
        # the stagnation counter — that's the whole point of tracking
        # "nudges without plan_completed advancing".
        # Bootstrap state first (so the second call is a true no-op
        # in completion terms), then arm stagnation, then re-touch.
        self._send(total=5, completed=2)
        self.mod.nudges_without_progress = 4
        self._send(total=5, completed=2)
        self.assertEqual(self.mod.nudges_without_progress, 4)

    def test_metric_change_resets_metric_counter(self):
        self._send(total=3, completed=0, metadata={"progress_metric": "a"})
        self._send(total=3, completed=0, metadata={"progress_metric": "a"})
        self.assertEqual(self.mod.updates_since_metric_change, 1)
        self._send(total=3, completed=0, metadata={"progress_metric": "b"})
        self.assertEqual(self.mod.updates_since_metric_change, 0)


# ---------------------------------------------------------------------------
# tool_execution_end — sets the per-turn flag
# ---------------------------------------------------------------------------


class TestToolExecutionEndHandler(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_plan_nudger()

    def test_marks_tool_used_this_turn(self):
        handler = self.handlers["tool_execution_end"]
        self.mod.tool_used_this_turn = False
        handler({"tool_name": "Bash", "is_error": False}, mock.MagicMock())
        self.assertTrue(self.mod.tool_used_this_turn)

    def test_updates_last_active_time(self):
        handler = self.handlers["tool_execution_end"]
        self.mod.last_active_time = time.monotonic() - 100
        handler({"tool_name": "Read"}, mock.MagicMock())
        self.assertGreater(self.mod.last_active_time, time.monotonic() - 1)


# ---------------------------------------------------------------------------
# turn_end firing rules (kept verbatim from main — those proved sound)
# ---------------------------------------------------------------------------


class TestTurnEndFiringRules(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_plan_nudger()
        self.handler = self.handlers["turn_end"]
        # A plan with remaining work, so steers are possible.
        self.mod.plan_total = 5
        self.mod.plan_completed = 2

    def test_tool_turn_never_fires(self):
        """A turn that ran a tool is a healthy loop tick — never steers,
        and resets the idle counter so prior idle history is forgotten."""
        ctx = mock.MagicMock()
        self.mod.idle_turns = 100  # pretend lots of idle history
        self.mod.tool_used_this_turn = True
        self.mod.last_active_time = time.monotonic() - 10_000
        self.handler({}, ctx)
        ctx.send_message.assert_not_called()
        self.assertEqual(self.mod.idle_turns, 0)
        self.assertFalse(self.mod.tool_used_this_turn)

    def test_no_steer_on_first_idle_turn(self):
        """Steer only after N consecutive idle turns, not the first."""
        ctx = mock.MagicMock()
        self.mod.idle_turns = 0
        self.mod.tool_used_this_turn = False
        self.mod.last_active_time = time.monotonic()
        self.handler({}, ctx)
        ctx.send_message.assert_not_called()
        self.assertEqual(self.mod.idle_turns, 1)

    def test_steer_after_idle_threshold(self):
        ctx = mock.MagicMock()
        _prime_idle(self.mod)
        self.handler({}, ctx)
        ctx.send_message.assert_called_once()
        custom_type = ctx.send_message.call_args[0][0]
        self.assertEqual(custom_type, "plan_status")

    def test_steer_after_time_threshold(self):
        """Wall-clock backstop fires even below the idle-turn threshold."""
        ctx = mock.MagicMock()
        self.mod.idle_turns = 0
        self.mod.tool_used_this_turn = False
        self.mod.last_active_time = time.monotonic() - self.mod.NUDGE_TIME_THRESHOLD - 1
        self.handler({}, ctx)
        ctx.send_message.assert_called_once()

    def test_no_steer_when_plan_complete(self):
        ctx = mock.MagicMock()
        self.mod.plan_total = 3
        self.mod.plan_completed = 3
        _prime_idle(self.mod)
        self.handler({}, ctx)
        ctx.send_message.assert_not_called()

    def test_no_steer_when_no_plan(self):
        ctx = mock.MagicMock()
        self.mod.plan_total = 0
        self.mod.plan_completed = 0
        _prime_idle(self.mod)
        self.handler({}, ctx)
        ctx.send_message.assert_not_called()

    def test_firing_resets_idle_counter(self):
        # Otherwise we'd fire on every subsequent turn until a tool runs.
        ctx = mock.MagicMock()
        _prime_idle(self.mod)
        self.handler({}, ctx)
        self.assertEqual(self.mod.idle_turns, 0)

    def test_firing_advances_stagnation(self):
        ctx = mock.MagicMock()
        _prime_idle(self.mod)
        self.handler({}, ctx)
        self.assertEqual(self.mod.nudges_without_progress, 1)
        _prime_idle(self.mod)
        self.handler({}, ctx)
        self.assertEqual(self.mod.nudges_without_progress, 2)


# ---------------------------------------------------------------------------
# agent_end — always fires on in-flight plan
# ---------------------------------------------------------------------------


class TestAgentEndHandler(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_plan_nudger()
        self.handler = self.handlers["agent_end"]

    def test_fires_when_plan_in_flight(self):
        self.mod.plan_total = 5
        self.mod.plan_completed = 3
        ctx = mock.MagicMock()
        self.handler({}, ctx)
        ctx.send_message.assert_called_once()

    def test_no_steer_when_plan_complete(self):
        self.mod.plan_total = 3
        self.mod.plan_completed = 3
        ctx = mock.MagicMock()
        self.handler({}, ctx)
        ctx.send_message.assert_not_called()

    def test_no_steer_when_no_plan(self):
        ctx = mock.MagicMock()
        self.handler({}, ctx)
        ctx.send_message.assert_not_called()


# ---------------------------------------------------------------------------
# Steer body composition
# ---------------------------------------------------------------------------


def _captured_body(ctx):
    ctx.send_message.assert_called_once()
    args, kwargs = ctx.send_message.call_args
    return args[1], kwargs


class TestSteerBody(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_plan_nudger()
        self.mod.plan_total = 5
        self.mod.plan_completed = 2

    def _fire_turn_end(self, **state):
        for k, v in state.items():
            setattr(self.mod, k, v)
        _prime_idle(self.mod)
        # Re-apply state so primed idle doesn't clobber metric/stagnation
        # values the caller wanted to test against.
        for k, v in state.items():
            setattr(self.mod, k, v)
        self.mod.idle_turns = self.mod.IDLE_TURN_THRESHOLD - 1
        self.mod.tool_used_this_turn = False
        self.mod.last_active_time = time.monotonic()
        ctx = mock.MagicMock()
        self.handlers["turn_end"]({}, ctx)
        body, kwargs = _captured_body(ctx)
        return body, kwargs

    def test_delivered_as_steer(self):
        _, kwargs = self._fire_turn_end()
        self.assertEqual(kwargs.get("deliver_as"), "steer")
        self.assertTrue(kwargs.get("display"), "steer should be visible to the user")

    def test_includes_framing_tag(self):
        body, _ = self._fire_turn_end()
        self.assertIn("[plan-status — keeping plan visible to the user]", body)

    def test_counter_line_includes_incomplete(self):
        body, _ = self._fire_turn_end()
        self.assertIn("3 step(s) incomplete", body)  # 5 - 2

    def test_counter_line_includes_idle_turns(self):
        body, _ = self._fire_turn_end()
        # Idle counter at fire time was IDLE_TURN_THRESHOLD before the
        # handler bumped it; we just assert *some* idle count appears.
        self.assertRegex(body, r"\d+ idle turn\(s\)")

    def test_metric_tip_when_unset(self):
        body, _ = self._fire_turn_end(plan_metadata={})
        self.assertIn("progress_metric", body)
        self.assertIn("Tip:", body)

    def test_no_metric_tip_when_set(self):
        body, _ = self._fire_turn_end(
            plan_metadata={"progress_metric": "coverage=80%"},
            last_metric_value="coverage=80%",
        )
        self.assertNotIn("Tip:", body)

    def test_metric_counter_when_set_and_stale(self):
        body, _ = self._fire_turn_end(
            plan_metadata={"progress_metric": "x"},
            last_metric_value="x",
            updates_since_metric_change=4,
        )
        self.assertIn("4 plan-update(s) since progress_metric changed", body)

    def test_handoff_line_only_at_stagnation(self):
        # First fire: not yet stagnant — no handoff line.
        body, _ = self._fire_turn_end(nudges_without_progress=0)
        self.assertNotIn("self_handoff", body)

        # Second/third fire: at the stagnation threshold, handoff line
        # appears.  We pre-load stagnation to STAGNATION_THRESHOLD-1 so
        # the handler bumps it to threshold during the fire.
        body, _ = self._fire_turn_end(
            nudges_without_progress=self.mod.STAGNATION_THRESHOLD - 1,
        )
        self.assertIn("self_handoff", body)
        self.assertIn("Stopping early is not the only escape", body)

    def test_stagnation_counter_in_body(self):
        body, _ = self._fire_turn_end(
            nudges_without_progress=self.mod.STAGNATION_THRESHOLD - 1,
        )
        self.assertRegex(body, r"\d+ consecutive pause\(s\) without plan progress")

    def test_body_is_strictly_neutral(self):
        """The whole point of the redesign — no imperatives, no
        ``[CONTINUE]``, no "MUST", no "stuck", no "do not"."""
        # Drive every block on by maxing out stagnation + missing
        # metric, so the body is at its longest.
        body, _ = self._fire_turn_end(
            plan_metadata={},
            nudges_without_progress=self.mod.STAGNATION_THRESHOLD,
        )
        lower = body.lower()
        for forbidden in ("[continue]", "must ", " must.", "must contain", "stuck",
                          "do not", "rewrite", "from scratch"):
            self.assertNotIn(
                forbidden,
                lower,
                f"calm steer body must not contain {forbidden!r}: {body!r}",
            )

    def test_steady_state_body_is_short(self):
        # In the cleanest case (metric set, fresh, no stagnation),
        # the body should be just the tag + counter line.
        body, _ = self._fire_turn_end(
            plan_metadata={"progress_metric": "x"},
            last_metric_value="x",
            updates_since_metric_change=0,
            nudges_without_progress=0,
        )
        self.assertEqual(len(body.splitlines()), 2)


if __name__ == "__main__":
    unittest.main()
