#!/usr/bin/env python3
"""Tests for the handoff-nudger builtin extension.

Pins the gating arithmetic (absolute vs percent threshold, re-nudge
interval, off switch) and — the expensive lesson this extension was born
from — that the delivery call is a *real* SDK method. ``ctx.prepend`` is
the wire method ``prepend_context``; an earlier prototype called
``ctx.prepend_context(...)`` inside a bare ``except`` and shipped a
silent no-op. Every fake ctx here is ``spec``'d against
:class:`fir_ext.Context`, so a misspelled method fails the test.
"""

import importlib
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
    """(Re-)import handoff_nudger with fresh module state, returning the
    module and its turn_end handler."""
    sys.modules.pop("handoff_nudger", None)
    with mock.patch.object(fir_ext, "run"):
        importlib.import_module("handoff_nudger")
    mod = sys.modules["handoff_nudger"]
    return mod, fir_ext._event_handlers["turn_end"]


def _ctx(tokens, window=1_000_000):
    ctx = mock.MagicMock(spec=fir_ext.Context)
    ctx.agent_info.return_value = {"context": {"tokens": tokens, "window": window}}
    return ctx


class HandoffNudgerTest(unittest.TestCase):
    def setUp(self):
        self.mod, self.handler = _load()
        patcher = mock.patch.object(self.mod, "_config", return_value={})
        self.config = patcher.start()
        self.addCleanup(patcher.stop)

    def turn(self, tokens, window=1_000_000):
        ctx = _ctx(tokens, window)
        self.handler({}, ctx)
        return ctx

    # --- delivery ---------------------------------------------------------

    def test_prepend_is_a_real_sdk_method(self):
        """Regression: the SDK method is prepend(), not prepend_context()."""
        self.assertTrue(callable(getattr(fir_ext.Context, "prepend", None)))

    def test_nudge_prepends_actionable_note(self):
        ctx = self.turn(200_000)
        ctx.prepend.assert_called_once()
        note = ctx.prepend.call_args[0][0]
        self.assertIn("self_handoff", note)
        self.assertIn("200,000 tokens", note)
        self.assertIn("(20% of a 1,000,000 window)", note)

    def test_note_omits_window_clause_when_window_unknown(self):
        note = self.turn(200_000, window=0).prepend.call_args[0][0]
        self.assertIn("200,000 tokens.", note)
        self.assertNotIn("window)", note)

    def test_nudge_does_not_start_a_turn(self):
        """followUp delivery makes the agent answer the nudge out loud,
        which leaks an unsolicited message into relayed chats."""
        ctx = self.turn(200_000)
        ctx.send_user_message.assert_not_called()

    # --- threshold arithmetic --------------------------------------------

    def test_below_threshold_is_silent(self):
        self.turn(149_999).prepend.assert_not_called()

    def test_absolute_threshold_fires(self):
        self.turn(150_000).prepend.assert_called_once()

    def test_percent_threshold_wins_on_small_window(self):
        # 60% of 200k = 120k, below the 150k absolute.
        self.assertEqual(self.mod._threshold({}, 200_000), 120_000)
        self.turn(120_000, window=200_000).prepend.assert_called_once()

    def test_absolute_threshold_wins_on_large_window(self):
        self.assertEqual(self.mod._threshold({}, 1_000_000), 150_000)

    def test_unknown_window_falls_back_to_absolute(self):
        self.assertEqual(self.mod._threshold({}, 0), 150_000)
        self.turn(150_000, window=0).prepend.assert_called_once()

    def test_config_overrides_thresholds(self):
        self.config.return_value = {"atTokens": 50_000, "atPercent": 90}
        self.assertEqual(self.mod._threshold(self.config.return_value, 1_000_000), 50_000)

    def test_garbage_config_falls_back_to_defaults(self):
        self.config.return_value = {"atTokens": "lots", "atPercent": None}
        self.assertEqual(self.mod._threshold(self.config.return_value, 1_000_000), 150_000)

    # --- re-nudge interval ------------------------------------------------

    def test_does_not_renudge_before_interval(self):
        self.turn(200_000).prepend.assert_called_once()
        self.turn(210_000).prepend.assert_not_called()
        self.turn(239_999).prepend.assert_not_called()

    def test_renudges_after_interval(self):
        self.turn(200_000)
        self.turn(240_000).prepend.assert_called_once()

    def test_interval_is_configurable(self):
        self.config.return_value = {"nudgeEvery": 5_000}
        self.turn(200_000)
        self.turn(205_000).prepend.assert_called_once()

    def test_shrinking_context_rearms_the_interval(self):
        """After a compaction the high-water mark must not strand us."""
        self.turn(500_000)
        self.turn(160_000).prepend.assert_called_once()

    # --- off switch -------------------------------------------------------

    def test_off_switch(self):
        self.config.return_value = {"off": True}
        ctx = self.turn(900_000)
        ctx.prepend.assert_not_called()
        ctx.agent_info.assert_not_called()

    # --- robustness -------------------------------------------------------

    def test_agent_info_failure_never_breaks_the_turn(self):
        ctx = mock.MagicMock(spec=fir_ext.Context)
        ctx.agent_info.side_effect = RuntimeError("bridge down")
        self.handler({}, ctx)  # must not raise
        ctx.prepend.assert_not_called()

    def test_missing_context_block_is_silent(self):
        ctx = mock.MagicMock(spec=fir_ext.Context)
        ctx.agent_info.return_value = {}
        self.handler({}, ctx)
        ctx.prepend.assert_not_called()


if __name__ == "__main__":
    unittest.main()
