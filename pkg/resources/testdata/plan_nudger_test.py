#!/usr/bin/env python3
"""Tests for the plan-nudger builtin extension.

Counts turns since the last plan update; every EVERY turns
with an unfinished plan it injects NUDGE via ctx.prepend.
"""

import importlib
import os
import sys
import unittest
from unittest import mock

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "builtin_extensions"))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "extension", "sdk", "python"))

import fir_ext


def _load():
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


class PlanNudgerTest(unittest.TestCase):
    def setUp(self):
        self.mod, h = _load()
        self.update = h["session_update"]
        self.turn_end = h["turn_end"]
        self.ctx = mock.Mock()

    def plan(self, total, completed):
        self.update(
            {"type": "plan_update", "plan": {"total": total, "completed": completed}}, self.ctx
        )
        self.turn_end(None, self.ctx)  # the turn that made the update

    def turns(self, n):
        for _ in range(n):
            self.turn_end(None, self.ctx)

    def test_subscribes_to_session_update_and_turn_end(self):
        _, h = _load()
        self.assertEqual(set(h), {"session_update", "turn_end"})

    def test_no_plan_is_noop(self):
        self.turns(10)
        self.ctx.prepend.assert_not_called()

    def test_nudges_after_every_turns(self):
        self.plan(5, 1)
        self.turns(self.mod.EVERY - 1)
        self.ctx.prepend.assert_not_called()
        self.turns(1)
        self.ctx.prepend.assert_called_once_with(
            self.mod.NUDGE.format(completed=1, total=5, turns=self.mod.EVERY)
        )

    def test_repeats_with_distinct_text(self):
        self.plan(5, 1)
        self.turns(self.mod.EVERY * 2)
        texts = [c.args[0] for c in self.ctx.prepend.call_args_list]
        self.assertEqual(len(texts), 2)
        self.assertNotEqual(texts[0], texts[1])

    def test_plan_update_resets_count(self):
        self.plan(5, 1)
        self.turns(self.mod.EVERY - 1)
        self.plan(5, 2)
        self.turns(self.mod.EVERY - 1)
        self.ctx.prepend.assert_not_called()

    def test_completed_plan_is_noop(self):
        self.plan(3, 3)
        self.turns(10)
        self.ctx.prepend.assert_not_called()

    def test_non_plan_update_is_ignored(self):
        self.update({"type": "session_named", "session_name": "x"}, self.ctx)
        self.turns(10)
        self.ctx.prepend.assert_not_called()

    def test_prepend_exception_is_swallowed(self):
        self.ctx.prepend.side_effect = RuntimeError("boom")
        self.plan(5, 0)
        self.turns(self.mod.EVERY)  # must not raise


if __name__ == "__main__":
    unittest.main()
