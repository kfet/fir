#!/usr/bin/env python3
"""Tests for the plan-nudger builtin extension.

Single trigger (``progress_metric`` stagnation across consecutive
plan updates), single output channel (model's reply injected as a
``followUp``). All judgement is delegated to the model; these tests
pin the gating logic and the escape valves, not the model's wording.

The side_query always runs on the current session model — no
advisor escalation, no separate config to test.
"""

import importlib
import json
import os
import sys
import unittest
from unittest import mock

_EXT_DIR = os.path.join(
    os.path.dirname(os.path.abspath(__file__)),
    "..",
    "builtin_extensions",
)
_SDK_DIR = os.path.join(
    os.path.dirname(os.path.abspath(__file__)),
    "..",
    "..",
    "extension",
    "sdk",
    "python",
)
sys.path.insert(0, _EXT_DIR)
sys.path.insert(0, _SDK_DIR)

import fir_ext


def _load():
    """(Re-)import plan_nudger with a fresh module namespace, returning
    the module and its registered event handlers."""
    sys.modules.pop("plan_nudger", None)
    before = dict(fir_ext._event_handlers)
    with mock.patch.object(fir_ext, "run"):
        importlib.import_module("plan_nudger")
    mod = sys.modules["plan_nudger"]
    handlers = {
        k: v
        for k, v in fir_ext._event_handlers.items()
        if k not in before or v is not before.get(k)
    }
    return mod, handlers


def _plan_update(handler, *, total, completed, metric=None, extra=None):
    metadata = {}
    if metric is not None:
        metadata["progress_metric"] = metric
    if extra:
        metadata.update(extra)
    handler(
        {
            "type": "plan_update",
            "plan": {"total": total, "completed": completed, "metadata": metadata},
        },
        mock.MagicMock(),
    )


class _FakeCtx:
    """Stand-in for fir_ext.Context — records side_query and
    send_user_message calls, returns a scripted side_query reply."""

    def __init__(self, reply=""):
        self._reply = reply
        self.side_query_calls = []
        self.send_user_message_calls = []

    def side_query(self, prompt):
        self.side_query_calls.append({"prompt": prompt})
        return self._reply

    def send_user_message(self, content, deliver_as=None):
        self.send_user_message_calls.append(
            {"content": content, "deliver_as": deliver_as},
        )


class TestRegistration(unittest.TestCase):
    def test_subscribes_only_to_session_update_and_turn_end(self):
        _, handlers = _load()
        self.assertIn("session_update", handlers)
        self.assertIn("turn_end", handlers)
        for unused in ("tool_execution_end", "agent_end", "user_prompt"):
            self.assertNotIn(unused, handlers)


class TestStateTracking(unittest.TestCase):
    def setUp(self):
        self.mod, handlers = _load()
        self.update = handlers["session_update"]

    def test_non_plan_update_is_ignored(self):
        self.update(
            {"type": "session_named", "session_name": "x"}, mock.MagicMock(),
        )
        self.assertEqual(self.mod.plan_total, 0)
        self.assertEqual(self.mod.stagnation, 0)

    def test_first_update_is_baseline(self):
        _plan_update(self.update, total=3, completed=0)
        self.assertEqual(self.mod.plan_total, 3)
        self.assertEqual(self.mod.stagnation, 0)

    def test_stagnation_ticks_when_metric_unchanged(self):
        _plan_update(self.update, total=3, completed=0, metric="x=0")
        _plan_update(self.update, total=3, completed=0, metric="x=0")
        self.assertEqual(self.mod.stagnation, 1)
        _plan_update(self.update, total=3, completed=0, metric="x=0")
        self.assertEqual(self.mod.stagnation, 2)

    def test_metric_change_resets_stagnation(self):
        for _ in range(3):
            _plan_update(self.update, total=3, completed=0, metric="x=0")
        self.assertEqual(self.mod.stagnation, 2)
        _plan_update(self.update, total=3, completed=1, metric="x=1")
        self.assertEqual(self.mod.stagnation, 0)

    def test_missing_metric_ticks_after_baseline(self):
        # No progress_metric set → empty string. First update is
        # baseline; the second tick stagnates.
        _plan_update(self.update, total=3, completed=0)
        _plan_update(self.update, total=3, completed=0)
        self.assertEqual(self.mod.stagnation, 1)


class TestTurnEndGating(unittest.TestCase):
    def setUp(self):
        self.mod, handlers = _load()
        self.update = handlers["session_update"]
        self.turn_end = handlers["turn_end"]

    def _stagnate_to(self, n):
        for _ in range(n + 1):
            _plan_update(self.update, total=3, completed=0, metric="stuck")
        self.assertEqual(self.mod.stagnation, n)

    # ── escape valves ──────────────────────────────────────────────────

    def test_no_plan_is_noop(self):
        ctx = _FakeCtx(reply="would say something")
        self.turn_end({}, ctx)
        self.assertEqual(ctx.side_query_calls, [])
        self.assertEqual(ctx.send_user_message_calls, [])

    def test_completed_plan_is_noop(self):
        _plan_update(self.update, total=3, completed=3, metric="done")
        ctx = _FakeCtx(reply="would say something")
        self.turn_end({}, ctx)
        self.assertEqual(ctx.side_query_calls, [])

    def test_below_threshold_is_noop(self):
        _plan_update(self.update, total=3, completed=0, metric="x")
        _plan_update(self.update, total=3, completed=0, metric="x")
        self.assertEqual(self.mod.stagnation, 1)
        ctx = _FakeCtx(reply="would say something")
        self.turn_end({}, ctx)
        self.assertEqual(ctx.side_query_calls, [])

    # ── happy paths ────────────────────────────────────────────────────

    def test_stagnation_consults_model_and_injects(self):
        self._stagnate_to(2)
        ctx = _FakeCtx(reply="  step 2 lacks evidence — show the diff?  ")
        self.turn_end({}, ctx)

        self.assertEqual(len(ctx.side_query_calls), 1)
        prompt = ctx.side_query_calls[0]["prompt"]
        # Prompt carries the plan-state JSON verbatim.
        self.assertIn("\"total\": 3", prompt)
        self.assertIn("\"stagnation_count\": 2", prompt)

        self.assertEqual(len(ctx.send_user_message_calls), 1)
        msg = ctx.send_user_message_calls[0]
        self.assertEqual(msg["deliver_as"], "followUp")
        self.assertEqual(msg["content"], "step 2 lacks evidence — show the diff?")

    def test_empty_reply_is_noop(self):
        self._stagnate_to(2)
        ctx = _FakeCtx(reply="")
        self.turn_end({}, ctx)
        self.assertEqual(len(ctx.side_query_calls), 1)
        self.assertEqual(ctx.send_user_message_calls, [])

    def test_whitespace_only_reply_is_noop(self):
        self._stagnate_to(2)
        ctx = _FakeCtx(reply="   \n\t  ")
        self.turn_end({}, ctx)
        self.assertEqual(ctx.send_user_message_calls, [])

    # ── re-fire guard ──────────────────────────────────────────────────

    def test_second_turn_end_at_same_stagnation_does_not_reconsult(self):
        self._stagnate_to(2)
        ctx = _FakeCtx(reply="first")
        self.turn_end({}, ctx)
        self.turn_end({}, ctx)
        self.assertEqual(len(ctx.side_query_calls), 1)

    def test_further_stagnation_re_consults(self):
        self._stagnate_to(2)
        ctx = _FakeCtx(reply="first")
        self.turn_end({}, ctx)
        _plan_update(self.update, total=3, completed=0, metric="stuck")
        self.assertEqual(self.mod.stagnation, 3)
        self.turn_end({}, ctx)
        self.assertEqual(len(ctx.side_query_calls), 2)

    def test_metric_change_resets_guard(self):
        self._stagnate_to(2)
        ctx = _FakeCtx(reply="first")
        self.turn_end({}, ctx)
        # Metric bumps → stagnation back to 0; turn_end is below threshold.
        _plan_update(self.update, total=3, completed=1, metric="moved")
        self.turn_end({}, ctx)
        self.assertEqual(len(ctx.side_query_calls), 1)

    def test_oscillating_metric_re_fires_on_fresh_stagnation(self):
        """Regression: if the metric flips back and forth (x=0 → x=1 →
        x=0 → x=0 → x=0) stagnation resets and then re-climbs. The
        re-fire guard must clear on the reset, so the second time we
        cross the threshold the model IS consulted again."""
        self._stagnate_to(2)
        ctx = _FakeCtx(reply="first")
        self.turn_end({}, ctx)
        self.assertEqual(len(ctx.side_query_calls), 1)

        # Metric moves (stagnation → 0) and then re-stagnates back to 2.
        _plan_update(self.update, total=3, completed=0, metric="x=1")
        self.assertEqual(self.mod.stagnation, 0)
        for _ in range(2):
            _plan_update(self.update, total=3, completed=0, metric="x=1")
        self.assertEqual(self.mod.stagnation, 2)

        self.turn_end({}, ctx)
        self.assertEqual(
            len(ctx.side_query_calls), 2,
            "guard must clear on stagnation reset, otherwise the second "
            "real stagnation episode is silently suppressed",
        )

    # ── defence in depth ──────────────────────────────────────────────

    def test_side_query_exception_is_swallowed(self):
        self._stagnate_to(2)

        class _Boom(_FakeCtx):
            def side_query(self, prompt):
                raise RuntimeError("network")

        ctx = _Boom()
        self.turn_end({}, ctx)  # must not raise
        self.assertEqual(ctx.send_user_message_calls, [])

    def test_send_exception_is_swallowed(self):
        self._stagnate_to(2)

        class _Boom(_FakeCtx):
            def send_user_message(self, content, deliver_as=None):
                raise RuntimeError("rpc")

        ctx = _Boom(reply="would inject")
        self.turn_end({}, ctx)  # must not raise


class TestPrompt(unittest.TestCase):
    """Pin the load-bearing parts of the prompt."""

    def setUp(self):
        self.mod, _ = _load()

    def test_carries_plan_state(self):
        prompt = self.mod.PROMPT.format(
            plan_state=json.dumps({"total": 4, "stagnation_count": 3}),
        )
        self.assertIn("\"total\": 4", prompt)
        self.assertIn("\"stagnation_count\": 3", prompt)

    def test_offers_empty_response_as_valid(self):
        # The "empty response = noop" affordance is what lets the
        # model say "no nudge needed" without injecting noise; don't
        # let a future edit silently remove it.
        self.assertIn('"" for noop', self.mod.PROMPT)


if __name__ == "__main__":
    unittest.main()
