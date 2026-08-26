#!/usr/bin/env python3
"""Tests for the provider-usage builtin extension.

Focus is the ``provider_error`` path: a Claude subscription rate limit must
say *when* it resets, and must say nothing at all when it cannot. The
expensive failure mode here is noise — guessing a window, or reporting a
30-second transient backoff as a usage-window reset — so the gates are
pinned individually. Every fake ctx is ``spec``'d against
:class:`fir_ext.Context`, so a misspelled surfacing method fails the test.
"""

import importlib
import json
import os
import pathlib
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
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

NOW = 1740506400.0  # 2025-02-25T18:00:00Z


def _load():
    """(Re-)import provider_usage with fresh module state, returning the
    module and its provider_error handler."""
    sys.modules.pop("provider_usage", None)
    with mock.patch.object(fir_ext, "run"):
        importlib.import_module("provider_usage")
    mod = sys.modules["provider_usage"]
    return mod, fir_ext._event_handlers["provider_error"]


def _iso(offset_seconds: float) -> str:
    """An ISO-8601 instant `offset_seconds` from NOW, UTC with a Z suffix."""
    dt = datetime.fromtimestamp(NOW, timezone.utc) + timedelta(seconds=offset_seconds)
    return dt.strftime("%Y-%m-%dT%H:%M:%SZ")


def _windows(**kw: dict) -> dict:
    return dict(kw)


class ParseTest(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load()

    def test_parse_iso_accepts_z_suffix(self):
        """Python 3.9's fromisoformat rejects 'Z'; every resets_at has one."""
        dt = self.mod._parse_iso("2025-02-25T18:00:00Z")
        self.assertIsNotNone(dt)
        self.assertEqual(dt.timestamp(), NOW)

    def test_parse_iso_accepts_offset_and_assumes_utc_when_naive(self):
        self.assertEqual(self.mod._parse_iso("2025-02-25T20:00:00+02:00").timestamp(), NOW)
        self.assertEqual(self.mod._parse_iso("2025-02-25T18:00:00").timestamp(), NOW)

    def test_parse_iso_rejects_junk(self):
        for bad in ("", None, 17, "not a date", "2025-13-45Tnope"):
            self.assertIsNone(self.mod._parse_iso(bad), bad)

    def test_reset_from_text(self):
        cases = [
            ("pipe seconds", "Claude AI usage limit reached|1740506400", NOW),
            ("pipe millis", "Claude AI usage limit reached|1740506400000", NOW),
            ("resetsAt number", '{"error":{"resetsAt":1740506400}}', NOW),
            ("resetsAt string", '{"resetsAt":"1740506400"}', NOW),
            ("resets_at iso", '{"resets_at":"2025-02-25T18:00:00Z"}', NOW),
            ("no timestamp", "429 rate_limit_error: too many requests", None),
            ("pipe but not a timestamp", "a|42 rate limit", None),
            ("unparseable iso", '{"resets_at":"2025-13-45Tnope"}', None),
            ("empty", "", None),
        ]
        for name, text, want in cases:
            with self.subTest(name):
                self.assertEqual(self.mod._reset_from_text(text), want)


class SelectionTest(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load()

    def test_picks_the_near_limit_window(self):
        windows = _windows(
            five_hour={"utilization": 100, "resets_at": _iso(1800)},
            seven_day={"utilization": 40, "resets_at": _iso(3600)},
        )
        self.assertEqual(self.mod._reset_from_windows(windows, NOW), (NOW + 1800, "5-hour"))

    def test_earliest_of_two_near_limit_windows_wins(self):
        windows = _windows(
            five_hour={"utilization": 100, "resets_at": _iso(3600)},
            seven_day={"utilization": 96, "resets_at": _iso(1800)},
        )
        self.assertEqual(self.mod._reset_from_windows(windows, NOW), (NOW + 1800, "7-day"))

    def test_no_near_limit_window_is_no_evidence(self):
        """A cold cache may belong to a different account entirely."""
        windows = _windows(five_hour={"utilization": 80, "resets_at": _iso(1800)})
        self.assertIsNone(self.mod._reset_from_windows(windows, NOW))

    def test_stale_and_corrupt_resets_rejected(self):
        past = _windows(five_hour={"utilization": 100, "resets_at": _iso(-3600)})
        far = _windows(five_hour={"utilization": 100, "resets_at": _iso(30 * 86400)})
        junk = _windows(five_hour={"utilization": "lots", "resets_at": _iso(1800)})
        for name, windows in (("past", past), ("far", far), ("junk", junk)):
            with self.subTest(name):
                self.assertIsNone(self.mod._reset_from_windows(windows, NOW))

    def test_window_label_matching(self):
        windows = _windows(
            five_hour={"utilization": 100, "resets_at": _iso(0)},
            seven_day={"utilization": 10, "resets_at": _iso(48 * 3600)},
        )
        self.assertEqual(self.mod._window_label_for(windows, NOW), "5-hour")
        self.assertEqual(self.mod._window_label_for(windows, NOW + 120), "5-hour")
        self.assertEqual(self.mod._window_label_for(windows, NOW + 48 * 3600), "7-day")
        self.assertEqual(self.mod._window_label_for(windows, NOW + 3600), "")
        self.assertEqual(self.mod._window_label_for({}, NOW), "")


class NoticeTest(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load()

    def _stamp(self, offset):
        local = datetime.fromtimestamp(NOW + offset).astimezone()
        return f"{local:%b} {local.day}, {local.hour % 12 or 12}:{local:%M %p %Z}"

    def test_format_includes_local_time_window_and_countdown(self):
        got = self.mod._format_reset_notice(NOW + 2520, "5-hour", NOW)
        self.assertEqual(
            got,
            f"Anthropic 5-hour limit reached — resets {self._stamp(2520)} (in 42m)",
        )

    def test_unknown_window_is_not_guessed(self):
        got = self.mod._format_reset_notice(NOW + 2520, "", NOW)
        self.assertTrue(got.startswith("Anthropic usage limit reached — resets "))

    def test_text_instant_beats_cache(self):
        windows = _windows(five_hour={"utilization": 100, "resets_at": _iso(9999)})
        notice = self.mod._rate_limit_notice(
            {"error_text": "usage limit reached|" + str(int(NOW + 2520))}, windows, NOW
        )
        self.assertIn("(in 42m)", notice)

    def test_text_instant_is_labelled_from_cache(self):
        windows = _windows(five_hour={"utilization": 100, "resets_at": _iso(2520)})
        notice = self.mod._rate_limit_notice(
            {"error_text": "usage limit reached|" + str(int(NOW + 2520))}, windows, NOW
        )
        self.assertIn("Anthropic 5-hour limit", notice)

    def test_cache_used_when_error_text_has_no_instant(self):
        windows = _windows(seven_day={"utilization": 99, "resets_at": _iso(5400)})
        notice = self.mod._rate_limit_notice({"error_text": "429 slow down"}, windows, NOW)
        self.assertIn("Anthropic 7-day limit reached — resets ", notice)
        self.assertIn("(in 1h30m)", notice)

    def test_long_retry_after_is_a_last_resort(self):
        notice = self.mod._rate_limit_notice(
            {"error_text": "429 slow down", "retry_after_ms": 2_520_000}, {}, NOW
        )
        self.assertIn("(in 42m)", notice)

    def test_short_retry_after_is_not_a_window_reset(self):
        """A 30s transient backoff must not be dressed up as a usage window."""
        self.assertIsNone(
            self.mod._rate_limit_notice(
                {"error_text": "429 slow down", "retry_after_ms": 30_000}, {}, NOW
            )
        )

    def test_stale_text_instant_falls_through_to_cache(self):
        windows = _windows(five_hour={"utilization": 100, "resets_at": _iso(1800)})
        notice = self.mod._rate_limit_notice(
            {"error_text": "usage limit reached|" + str(int(NOW - 3600))}, windows, NOW
        )
        self.assertIn("(in 30m)", notice)

    def test_nothing_known_says_nothing(self):
        self.assertIsNone(self.mod._rate_limit_notice({"error_text": "429 slow down"}, {}, NOW))


class HandlerTest(unittest.TestCase):
    def setUp(self):
        self.mod, self.handler = _load()
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.addCleanup(mock.patch.stopall)
        mock.patch.object(self.mod, "_cache_dir", return_value=pathlib.Path(self.tmp.name)).start()
        mock.patch.object(self.mod, "_find_anthropic_token", return_value="oauth-token").start()
        self._write_cache({"five_hour": {"utilization": 100, "resets_at": self._soon()}})

    def _soon(self):
        return (datetime.now(timezone.utc) + timedelta(minutes=42, seconds=30)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )

    def _write_cache(self, data):
        path = os.path.join(self.tmp.name, "anthropic-usage-cache.json")
        with open(path, "w") as f:
            json.dump({"fetched_at": 0, "data": data}, f)

    def _fire(self, **params):
        ctx = mock.MagicMock(spec=fir_ext.Context)
        base = {"kind": "rate_limit", "provider": "anthropic", "error_text": "429 slow down"}
        self.handler({**base, **params}, ctx)
        return ctx

    def test_notify_is_a_real_sdk_method(self):
        self.assertTrue(callable(getattr(fir_ext.Context, "notify", None)))

    def test_anthropic_rate_limit_notifies_with_reset(self):
        ctx = self._fire()
        ctx.notify.assert_called_once()
        message, kwargs = ctx.notify.call_args[0][0], ctx.notify.call_args[1]
        self.assertIn("Anthropic 5-hour limit reached — resets ", message)
        self.assertIn("(in 42m)", message)
        self.assertEqual(kwargs.get("level"), "warning")

    def test_token_never_appears_in_the_message(self):
        message = self._fire().notify.call_args[0][0]
        self.assertNotIn("oauth-token", message)

    def test_other_error_kinds_ignored(self):
        for kind in ("overloaded", "server", "transport", "terminal"):
            with self.subTest(kind):
                self._fire(kind=kind).notify.assert_not_called()

    def test_other_providers_ignored(self):
        self._fire(provider="openai").notify.assert_not_called()
        self._fire(provider="").notify.assert_not_called()

    def test_api_key_account_ignored(self):
        """No OAuth token means no subscription windows to report."""
        mock.patch.object(self.mod, "_find_anthropic_token", return_value=None).start()
        self._fire().notify.assert_not_called()

    def test_empty_params_ignored(self):
        ctx = mock.MagicMock(spec=fir_ext.Context)
        self.handler({}, ctx)
        self.handler(None, ctx)
        ctx.notify.assert_not_called()

    def test_no_cache_and_no_instant_stays_silent(self):
        os.unlink(os.path.join(self.tmp.name, "anthropic-usage-cache.json"))
        self._fire().notify.assert_not_called()

    def test_corrupt_cache_stays_silent(self):
        with open(os.path.join(self.tmp.name, "anthropic-usage-cache.json"), "w") as f:
            f.write('{"data": ')
        self._fire().notify.assert_not_called()

    def test_error_path_makes_no_network_call(self):
        """The whole point: the cache is read, never refreshed, on this path."""
        http = mock.patch.object(self.mod, "_http_get_json").start()
        fetch = mock.patch.object(self.mod, "_cached_fetch").start()
        self._fire().notify.assert_called_once()
        http.assert_not_called()
        fetch.assert_not_called()

    def test_notify_failure_is_swallowed(self):
        ctx = mock.MagicMock(spec=fir_ext.Context)
        ctx.notify.side_effect = RuntimeError("host went away")
        self.handler({"kind": "rate_limit", "provider": "anthropic", "error_text": ""}, ctx)


if __name__ == "__main__":
    unittest.main()
