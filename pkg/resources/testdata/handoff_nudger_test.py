#!/usr/bin/env python3
"""Tests for the handoff-nudger builtin extension.

Pins the gating arithmetic (ceiling vs cold-cache trigger, re-nudge
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
    module and its event handlers."""
    sys.modules.pop("handoff_nudger", None)
    with mock.patch.object(fir_ext, "run"):
        importlib.import_module("handoff_nudger")
    mod = sys.modules["handoff_nudger"]
    return mod, fir_ext._event_handlers["turn_end"], fir_ext._event_handlers["turn_start"]


def _ctx(tokens, window=1_000_000):
    ctx = mock.MagicMock(spec=fir_ext.Context)
    ctx.agent_info.return_value = {"context": {"tokens": tokens, "window": window}}
    return ctx


class HandoffNudgerTest(unittest.TestCase):
    def setUp(self):
        self.mod, self.handler, self.start = _load()
        patcher = mock.patch.object(self.mod, "_config", return_value={})
        self.config = patcher.start()
        self.addCleanup(patcher.stop)
        # Default to a warm cache so tests must opt in to the cold trigger.
        self.warm()

    def warm(self):
        """Pretend the previous turn ended seconds ago — cache alive."""
        self.mod._idle_seconds = 5.0

    def cold(self, minutes=90):
        self.mod._idle_seconds = minutes * 60.0

    def idle_unknown(self):
        """As after a process start — no previous turn_end to measure from."""
        self.mod._idle_seconds = None

    def turn(self, tokens, window=1_000_000):
        ctx = _ctx(tokens, window)
        self.handler({}, ctx)
        return ctx

    # --- delivery ---------------------------------------------------------

    def test_prepend_is_a_real_sdk_method(self):
        """Regression: the SDK method is prepend(), not prepend_context()."""
        self.assertTrue(callable(getattr(fir_ext.Context, "prepend", None)))

    def test_ceiling_nudge_prepends_actionable_note(self):
        ctx = self.turn(600_000)
        ctx.prepend.assert_called_once()
        note = ctx.prepend.call_args[0][0]
        self.assertIn("self_handoff", note)
        self.assertIn("600,000 tokens", note)
        self.assertIn("(60% of a 1,000,000 window)", note)
        self.assertIn("Auto-compaction", note)
        self.assertNotIn("cache has expired", note)

    def test_cold_note_explains_the_expired_cache(self):
        self.cold(minutes=125)
        note = self.turn(200_000).prepend.call_args[0][0]
        self.assertIn("idle for 2h05m", note)
        self.assertIn("cache has expired", note)
        self.assertIn("self_handoff", note)

    def test_note_omits_window_clause_when_window_unknown(self):
        note = self.turn(600_000, window=0).prepend.call_args[0][0]
        self.assertIn("600,000 tokens.", note)
        self.assertNotIn("window)", note)

    def test_nudge_does_not_start_a_turn(self):
        """followUp delivery makes the agent answer the nudge out loud,
        which leaks an unsolicited message into relayed chats."""
        ctx = self.turn(600_000)
        ctx.send_user_message.assert_not_called()

    # --- ceiling arithmetic ----------------------------------------------

    def test_below_ceiling_is_silent_while_warm(self):
        self.turn(499_999).prepend.assert_not_called()

    def test_ceiling_fires(self):
        self.turn(500_000).prepend.assert_called_once()

    def test_percent_ceiling_wins_on_small_window(self):
        # 65% of 200k = 130k, below the 500k absolute.
        self.assertEqual(self.mod._threshold({}, 200_000), 130_000)
        self.turn(130_000, window=200_000).prepend.assert_called_once()

    def test_absolute_ceiling_wins_on_large_window(self):
        self.assertEqual(self.mod._threshold({}, 1_000_000), 500_000)

    def test_unknown_window_falls_back_to_absolute(self):
        self.assertEqual(self.mod._threshold({}, 0), 500_000)
        self.turn(500_000, window=0).prepend.assert_called_once()

    def test_config_overrides_thresholds(self):
        self.config.return_value = {"atTokens": 50_000, "atPercent": 90}
        self.assertEqual(self.mod._threshold(self.config.return_value, 1_000_000), 50_000)

    def test_garbage_config_falls_back_to_defaults(self):
        self.config.return_value = {"atTokens": "lots", "atPercent": None}
        self.assertEqual(self.mod._threshold(self.config.return_value, 1_000_000), 500_000)

    # --- cold-cache trigger ----------------------------------------------

    def test_idle_threshold_tracks_the_ceiling(self):
        """One knob moves both triggers — 30% of whichever ceiling applies."""
        self.assertEqual(self.mod._idle_threshold({}, 1_000_000), 150_000)

    def test_idle_threshold_has_an_absolute_floor(self):
        """30% of a small window's ceiling would nudge absurdly early."""
        self.assertEqual(self.mod._threshold({}, 200_000), 130_000)
        self.assertEqual(self.mod._idle_threshold({}, 200_000), 100_000)

    def test_idle_threshold_never_exceeds_the_ceiling(self):
        """On a tiny window the ceiling is the only trigger."""
        self.assertEqual(self.mod._idle_threshold({}, 64_000), self.mod._threshold({}, 64_000))

    def test_cold_cache_fires_below_the_ceiling(self):
        self.cold()
        self.turn(150_000).prepend.assert_called_once()

    def test_warm_cache_stays_quiet_below_the_ceiling(self):
        self.warm()
        self.turn(499_999).prepend.assert_not_called()

    def test_cold_cache_ignores_trivial_sessions(self):
        """A small cold re-read is cheaper than the context a handoff loses."""
        self.cold()
        self.turn(149_999).prepend.assert_not_called()

    def test_idle_minutes_is_configurable(self):
        self.config.return_value = {"idleMinutes": 10}
        self.cold(minutes=11)
        self.turn(200_000).prepend.assert_called_once()

    def test_just_under_the_idle_window_is_still_warm(self):
        self.cold(minutes=64)
        self.turn(200_000).prepend.assert_not_called()

    def test_unknown_idle_counts_as_cold(self):
        """First turn after a process start: the cache cannot have survived."""
        self.idle_unknown()
        self.turn(200_000).prepend.assert_called_once()

    def test_slow_cadence_is_nudged_once_not_every_turn(self):
        """A user messaging every two hours is cold on every single turn.
        Nagging them each time is the behaviour this extension exists to
        stop, so only the first turn of a cold streak skips the throttle."""
        self.cold()
        self.turn(200_000).prepend.assert_called_once()
        self.cold()
        self.turn(220_000).prepend.assert_not_called()
        self.cold()
        self.turn(240_000).prepend.assert_not_called()

    def test_cold_streak_still_renudges_on_the_token_interval(self):
        self.cold()
        self.turn(200_000).prepend.assert_called_once()
        self.cold()
        self.turn(300_000).prepend.assert_called_once()

    def test_warm_activity_rearms_the_cold_exemption(self):
        self.cold()
        self.turn(200_000).prepend.assert_called_once()
        self.warm()
        self.turn(210_000).prepend.assert_not_called()
        self.cold()
        self.turn(220_000).prepend.assert_called_once()

    # --- idle measurement -------------------------------------------------

    def test_idle_is_measured_end_to_start_not_end_to_end(self):
        """A single long turn keeps the cache warm with its own API calls,
        so its duration must not be counted as idle."""
        clock = [1000.0]
        with mock.patch.object(self.mod.time, "monotonic", lambda: clock[0]):
            self.handler({}, _ctx(10_000))  # turn ends at t=1000
            clock[0] = 1010.0
            self.start({}, _ctx(10_000))  # next turn starts 10s later
            self.assertAlmostEqual(self.mod._idle_seconds, 10.0)
            clock[0] = 99_000.0  # ...and runs for hours
            ctx = _ctx(200_000)
            self.handler({}, ctx)
            ctx.prepend.assert_not_called()

    def test_turn_start_records_a_real_gap(self):
        clock = [1000.0]
        with mock.patch.object(self.mod.time, "monotonic", lambda: clock[0]):
            self.handler({}, _ctx(10_000))
            clock[0] += 90 * 60
            self.start({}, _ctx(10_000))
            self.turn(200_000).prepend.assert_called_once()

    def test_turn_start_before_any_turn_end_leaves_idle_unknown(self):
        self.mod._last_turn_end = None
        self.start({}, _ctx(10_000))
        self.assertIsNone(self.mod._idle_seconds)

    def test_humanise(self):
        self.assertEqual(self.mod._humanise(None), "a while")
        self.assertEqual(self.mod._humanise(90), "1m")
        self.assertEqual(self.mod._humanise(3600), "1h00m")
        self.assertEqual(self.mod._humanise(7 * 3600 + 5 * 60), "7h05m")

    # --- re-nudge interval ------------------------------------------------

    def test_does_not_renudge_before_interval(self):
        self.turn(600_000).prepend.assert_called_once()
        self.turn(610_000).prepend.assert_not_called()
        self.turn(699_999).prepend.assert_not_called()

    def test_renudges_after_interval(self):
        self.turn(600_000)
        self.turn(700_000).prepend.assert_called_once()

    def test_interval_is_configurable(self):
        self.config.return_value = {"nudgeEvery": 5_000}
        self.turn(600_000)
        self.turn(605_000).prepend.assert_called_once()

    def test_cold_trigger_is_exempt_from_the_token_interval(self):
        """The one cheap cut-over moment must not be eaten by a cooldown
        left over from a ceiling nudge 20k tokens ago."""
        self.turn(600_000).prepend.assert_called_once()
        self.cold()
        self.turn(620_000).prepend.assert_called_once()

    def test_cold_exemption_does_not_repeat_while_warm(self):
        self.cold()
        self.turn(600_000).prepend.assert_called_once()
        self.warm()
        self.turn(620_000).prepend.assert_not_called()

    def test_shrinking_context_rearms_the_interval(self):
        """After a compaction the high-water mark must not strand us."""
        self.turn(900_000)
        self.turn(510_000).prepend.assert_called_once()

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

    def test_failed_turn_still_records_the_clock(self):
        """Otherwise one bridge hiccup makes every later turn look idle."""
        ctx = mock.MagicMock(spec=fir_ext.Context)
        ctx.agent_info.side_effect = RuntimeError("bridge down")
        self.handler({}, ctx)
        self.assertIsNotNone(self.mod._last_turn_end)

    def test_missing_context_block_is_silent(self):
        ctx = mock.MagicMock(spec=fir_ext.Context)
        ctx.agent_info.return_value = {}
        self.handler({}, ctx)
        ctx.prepend.assert_not_called()


if __name__ == "__main__":
    unittest.main()
