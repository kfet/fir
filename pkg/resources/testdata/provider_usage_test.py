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
import time
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


class EarlyWarningTest(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load()
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        mock.patch.object(self.mod, "_cache_dir", return_value=pathlib.Path(self.tmp.name)).start()
        self.addCleanup(mock.patch.stopall)
        mock.patch.object(self.mod.time, "time", return_value=NOW).start()

    def _data(self, five=50.0, seven=50.0, reset5=3600, reset7=86400):
        return {
            "five_hour": {"utilization": five, "resets_at": _iso(reset5)},
            "seven_day": {"utilization": seven, "resets_at": _iso(reset7)},
            "extra_usage": {"utilization": 99.0},
        }

    def test_below_threshold_clears_section_and_says_nothing(self):
        ctx = mock.Mock()
        self.mod._publish_warnings(ctx, self._data(), "acp")
        ctx.clear_section.assert_called_once()
        ctx.set_section.assert_not_called()
        ctx.send_message.assert_not_called()
        ctx.notify.assert_not_called()

    def test_acp_crossing_sets_section_and_triggers_turn_once(self):
        ctx = mock.Mock()
        data = self._data(seven=94.0)
        self.mod._publish_warnings(ctx, data, "acp")
        self.mod._publish_warnings(ctx, data, "acp")
        self.assertEqual(ctx.set_section.call_count, 2)
        sect = ctx.set_section.call_args[0][0]
        self.assertIn("7-day window at ≥80%", sect)
        self.assertNotIn(" in ", sect.split("resets")[1].split(")")[0])
        ctx.send_message.assert_called_once()
        self.assertTrue(ctx.send_message.call_args.kwargs["trigger_turn"])
        ctx.notify.assert_not_called()

    def test_section_stable_as_usage_creeps(self):
        ctx = mock.Mock()
        self.mod._publish_warnings(ctx, self._data(five=81.0), "acp")
        first = ctx.set_section.call_args[0][0]
        self.mod._publish_warnings(ctx, self._data(five=84.0), "acp")
        self.assertEqual(first, ctx.set_section.call_args[0][0])

    def test_second_session_does_not_reannounce(self):
        a, b = mock.Mock(), mock.Mock()
        data = self._data(five=85.0)
        self.mod._publish_warnings(a, data, "acp")
        self.mod._publish_warnings(b, data, "acp")
        a.send_message.assert_called_once()
        b.send_message.assert_not_called()
        b.set_section.assert_called_once()

    def test_higher_level_announces_again(self):
        ctx = mock.Mock()
        self.mod._publish_warnings(ctx, self._data(five=85.0), "acp")
        self.mod._publish_warnings(ctx, self._data(five=96.0), "acp")
        self.assertEqual(ctx.send_message.call_count, 2)

    def test_new_window_reset_announces_again(self):
        ctx = mock.Mock()
        self.mod._publish_warnings(ctx, self._data(five=85.0, reset5=3600), "acp")
        self.mod._publish_warnings(ctx, self._data(five=85.0, reset5=3600 + 5 * 3600), "acp")
        self.assertEqual(ctx.send_message.call_count, 2)

    def test_tui_uses_notify(self):
        ctx = mock.Mock()
        self.mod._publish_warnings(ctx, self._data(five=81.0), "interactive")
        ctx.notify.assert_called_once()
        self.assertEqual(ctx.notify.call_args.kwargs["level"], "warning")
        ctx.send_message.assert_not_called()

    def test_past_reset_is_ignored(self):
        ctx = mock.Mock()
        self.mod._publish_warnings(ctx, self._data(five=99.0, reset5=-60), "acp")
        ctx.clear_section.assert_called_once()
        ctx.send_message.assert_not_called()

    def test_expired_state_is_pruned(self):
        self.state_file = pathlib.Path(self.tmp.name) / "anthropic-usage-warned.json"
        ctx = mock.Mock()
        self.mod._publish_warnings(ctx, self._data(five=85.0, reset5=60), "acp")
        state = json.loads(self.state_file.read_text())
        self.assertEqual(len(state), 1)
        with mock.patch.object(self.mod.time, "time", return_value=NOW + 120):
            self.mod._publish_warnings(ctx, self._data(five=10.0), "acp")
            self.mod._publish_warnings(ctx, self._data(five=85.0, reset5=7200), "acp")
        state = json.loads(self.state_file.read_text())
        self.assertEqual(len(state), 1)

    def test_failed_state_write_leaves_no_tmp(self):
        ctx = mock.Mock()
        with mock.patch.object(self.mod.json, "dump", side_effect=OSError("disk full")):
            self.mod._publish_warnings(ctx, self._data(five=85.0), "acp")
        self.assertEqual(list(pathlib.Path(self.tmp.name).glob("*.tmp")), [])
        ctx.send_message.assert_not_called()


class HealthTest(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load()
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = pathlib.Path(self.tmp.name)
        mock.patch.object(self.mod, "_cache_dir", return_value=self.dir).start()
        self.addCleanup(mock.patch.stopall)
        mock.patch.object(self.mod.time, "time", return_value=NOW).start()

    def _fail(self, exc):
        def boom():
            raise exc

        return self.mod._cached_fetch("anthropic-usage", boom)

    def _cache(self):
        return json.loads((self.dir / "anthropic-usage-cache.json").read_text())

    def test_generic_failure_backs_off_instead_of_refetching(self):
        calls = []

        def boom():
            calls.append(1)
            raise OSError("down")

        self.mod._cached_fetch("anthropic-usage", boom)
        self.mod._cached_fetch("anthropic-usage", boom)
        self.assertEqual(len(calls), 1)
        self.assertGreater(self._cache()["backoff_until"], NOW)

    def test_fresh_failure_is_not_stale_yet(self):
        self._fail(OSError("down"))
        self.assertEqual(self.mod._usage_health(self._cache(), NOW + 60), [])

    def test_long_failure_is_stale_and_announced_once(self):
        self._fail(OSError("down"))
        alerts = self.mod._usage_health(self._cache(), NOW + 3600)
        self.assertEqual(len(alerts), 1)
        self.assertIn("stale", alerts[0][2])
        a, b = mock.Mock(), mock.Mock()
        self.mod._publish_warnings(a, None, "acp", alerts)
        self.mod._publish_warnings(b, None, "acp", alerts)
        a.send_message.assert_called_once()
        b.send_message.assert_not_called()
        self.assertIn("stale", b.set_section.call_args[0][0])

    def test_alert_text_stable_over_time(self):
        self._fail(OSError("down"))
        c = self._cache()

        def strip(alerts):
            return [(a[0], a[2]) for a in alerts]

        h = self.mod._usage_health
        self.assertEqual(strip(h(c, NOW + 3600)), strip(h(c, NOW + 7200)))
        cred = {"access": "x", "refresh": "y", "expires": (NOW - 3600) * 1000}
        ch = self.mod._credential_health
        self.assertEqual(strip(ch(cred, NOW)), strip(ch(cred, NOW + 600)))

    def test_new_token_bypasses_auth_backoff(self):
        calls = []

        def boom():
            calls.append(1)
            raise self.mod.AuthError("401")

        self.mod._cached_fetch("anthropic-usage", boom, key="old")
        self.mod._cached_fetch("anthropic-usage", boom, key="old")
        self.assertEqual(len(calls), 1)
        r = self.mod._cached_fetch("anthropic-usage", lambda: {"ok": 1}, key="new")
        self.assertEqual(r.data, {"ok": 1})

    def test_generic_failure_status_is_not_rate_limited(self):
        self.mod._cached_fetch("anthropic-usage", lambda: {"five_hour": {"utilization": 1}})
        with mock.patch.object(self.mod.time, "time", return_value=NOW + 400):
            r = self._fail(OSError("down"))
            self.assertNotIn("rate-limited", r.status_suffix())
            r = self.mod._cached_fetch("anthropic-usage", dict)
            self.assertNotIn("rate-limited", r.status_suffix())
        with mock.patch.object(self.mod.time, "time", return_value=NOW + 10000):
            r = self._fail(self.mod.AuthError("401"))
        self.assertIn("auth", r.status_suffix())

    def test_transient_error_does_not_mask_auth_error(self):
        self._fail(self.mod.AuthError("401"))
        with mock.patch.object(self.mod.time, "time", return_value=NOW + 10000):
            self._fail(OSError("down"))
        alerts = self.mod._usage_health(self._cache(), NOW + 10001)
        self.assertIn("401", alerts[0][2])

    def test_auth_error_alerts_immediately(self):
        self._fail(self.mod.AuthError("401"))
        alerts = self.mod._usage_health(self._cache(), NOW + 1)
        self.assertEqual(len(alerts), 1)
        self.assertIn("401", alerts[0][2])

    def test_success_clears_episode(self):
        self._fail(OSError("down"))
        with mock.patch.object(self.mod.time, "time", return_value=NOW + 4000):
            self.mod._cached_fetch("anthropic-usage", lambda: {"five_hour": {}})
        self.assertEqual(self.mod._usage_health(self._cache(), NOW + 4001), [])

    def test_http_401_raises_auth_error(self):
        import email.message
        import urllib.error

        err = urllib.error.HTTPError("u", 401, "no", email.message.Message(), None)
        with mock.patch.object(self.mod.urllib.request, "urlopen", side_effect=err):
            with self.assertRaises(self.mod.AuthError):
                self.mod._http_get_json("https://example.com", {})

    def test_unrefreshed_token_warns(self):
        cred = {"access": "x", "refresh": "y", "expires": (NOW - 3600) * 1000}
        alerts = self.mod._credential_health(cred, NOW)
        self.assertEqual(len(alerts), 1)
        self.assertIn("refresh token", alerts[0][2])

    def test_recently_expired_token_within_grace_is_quiet(self):
        cred = {"access": "x", "refresh": "y", "expires": (NOW - 60) * 1000}
        self.assertEqual(self.mod._credential_health(cred, NOW), [])

    def test_login_age_off_by_default_and_configurable(self):
        cred = {
            "access": "x",
            "refresh": "y",
            "expires": (NOW + 40 * 86400) * 1000,
            "extra": {"accountId": "acct"},
        }
        self.assertEqual(self.mod._credential_health(cred, NOW), [])
        self.mod._apply_config({"login_max_age_days": 30})
        self.assertEqual(self.mod._credential_health(cred, NOW), [])
        alerts = self.mod._credential_health(cred, NOW + 31 * 86400)
        self.assertEqual(len(alerts), 1)
        self.assertIn("30 days", alerts[0][2])

    def test_config_thresholds_and_disable(self):
        self.assertTrue(self.mod._apply_config({"warn_levels": [50, 90]}))
        self.assertEqual(self.mod.WARN_LEVELS, (50.0, 90.0))
        ctx = mock.Mock()
        data = {"five_hour": {"utilization": 55.0, "resets_at": _iso(3600)}}
        self.mod._publish_warnings(ctx, data, "acp")
        ctx.send_message.assert_called_once()
        self.assertFalse(self.mod._apply_config({"enabled": False}))
        self.assertTrue(self.mod._apply_config(None))

    def test_frontmatter_is_builtin(self):
        src = pathlib.Path(self.mod.__file__).read_text()
        self.assertIn("# builtin: true", src)


def _unified(five=0.42, seven=0.17, five_reset=NOW + 3600, seven_reset=NOW + 86400):
    return {
        "anthropic-ratelimit-unified-status": "allowed",
        "anthropic-ratelimit-unified-5h-utilization": f"{five:.2f}",
        "anthropic-ratelimit-unified-5h-reset": str(int(five_reset)),
        "anthropic-ratelimit-unified-7d-utilization": f"{seven:.2f}",
        "anthropic-ratelimit-unified-7d-reset": str(int(seven_reset)),
        "anthropic-ratelimit-unified-representative-claim": "five_hour",
        "content-type": "text/event-stream",
    }


class HeaderFeedTest(unittest.TestCase):
    """Quota readings from provider response headers (message_end /
    provider_error ``response_headers``) feed the shared cache."""

    def setUp(self):
        self.mod, self.err_handler = _load()
        self.end_handler = fir_ext._event_handlers["message_end"]
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = pathlib.Path(self.tmp.name)
        self.addCleanup(self.tmp.cleanup)
        self.addCleanup(mock.patch.stopall)
        mock.patch.object(self.mod, "_cache_dir", return_value=self.dir).start()

    def _cache(self):
        return json.loads((self.dir / "anthropic-usage-cache.json").read_text())

    def test_parse_anthropic_unified(self):
        cache_name, windows, extra = self.mod._parse_anthropic_unified(_unified())
        self.assertEqual(cache_name, "anthropic-usage")
        self.assertEqual(windows["five_hour"]["utilization"], 42.0)
        self.assertEqual(
            self.mod._parse_iso(windows["five_hour"]["resets_at"]).timestamp(), NOW + 3600
        )
        self.assertEqual(windows["seven_day"]["utilization"], 17.0)
        self.assertEqual(extra["status"], "allowed")
        self.assertNotIn("content-type", extra)

    def test_parse_ignores_other_providers_and_junk(self):
        self.assertIsNone(self.mod._parse_headers("openai", _unified()))
        # An extra account's slot feeds its own cache, never the default one.
        self.assertEqual(
            self.mod._parse_headers("anthropic#acct2", _unified())[0], "anthropic-usage-acct2"
        )
        self.assertEqual(self.mod._parse_headers("anthropic", _unified())[0], "anthropic-usage")
        self.assertIsNotNone(self.mod._parse_headers("anthropic", _unified()))
        self.assertIsNone(self.mod._parse_headers("anthropic", {"content-type": "x"}))
        self.assertIsNone(self.mod._parse_headers("anthropic", None))
        bad = {"anthropic-ratelimit-unified-5h-utilization": "nope"}
        self.assertIsNone(self.mod._parse_headers("anthropic", bad))

    def test_message_end_merges_into_cache_and_series(self):
        (self.dir / "anthropic-usage-cache.json").write_text(
            json.dumps(
                {
                    "fetched_at": 5,
                    "data": {
                        "five_hour": {"utilization": 10.0, "resets_at": "old"},
                        "seven_day_opus": {"utilization": 3.0},
                    },
                }
            )
        )
        self.end_handler(
            {"role": "assistant", "provider": "anthropic", "response_headers": _unified()},
            mock.Mock(),
        )
        cache = self._cache()
        self.assertEqual(cache["fetched_at"], 5)
        self.assertGreater(cache["headers_at"], 0)
        self.assertEqual(cache["headers"]["representative-claim"], "five_hour")
        self.assertEqual(cache["data"]["five_hour"]["utilization"], 42.0)
        self.assertEqual(cache["data"]["seven_day_opus"]["utilization"], 3.0)
        series = self.mod._read_series()
        self.assertEqual(len(series), 1)
        self.assertEqual(series[0]["five_hour"], 42.0)

    def test_message_end_ignores_non_assistant_and_missing_headers(self):
        self.end_handler({"role": "user"}, mock.Mock())
        self.end_handler({"role": "assistant", "provider": "anthropic"}, mock.Mock())
        self.end_handler({}, mock.Mock())
        self.assertFalse((self.dir / "anthropic-usage-cache.json").exists())

    def test_provider_error_ingests_even_when_not_rate_limit(self):
        self.err_handler(
            {"kind": "server", "provider": "anthropic", "response_headers": _unified(five=1.0)},
            mock.Mock(),
        )
        self.assertEqual(self._cache()["data"]["five_hour"]["utilization"], 100.0)

    def test_older_reading_does_not_overwrite_newer(self):
        self.mod._ingest_reading("anthropic-usage", {"five_hour": {"utilization": 50.0}}, {}, NOW)
        self.mod._ingest_reading(
            "anthropic-usage", {"five_hour": {"utilization": 40.0}}, {}, NOW - 5
        )
        self.assertEqual(self._cache()["data"]["five_hour"]["utilization"], 50.0)

    def test_series_is_bounded(self):
        for i in range(5):
            self.mod._ingest_reading(
                "anthropic-usage",
                {"five_hour": {"utilization": 1.0}},
                {},
                NOW - self.mod.SERIES_MAX_AGE - 100 + i,
            )
        self.mod._ingest_reading("anthropic-usage", {"five_hour": {"utilization": 2.0}}, {}, NOW)
        self.assertEqual(len(self.mod._read_series()), 1)

    def test_fresh_headers_override_endpoint_backoff_and_staleness(self):
        now = time.time()
        cached = {
            "fetched_at": now - 3 * self.mod.STALE_AFTER_SECONDS,
            "first_failure_at": now - 3 * self.mod.STALE_AFTER_SECONDS,
            "backoff_until": now + 600,
            "rate_limited": True,
            "headers_at": now - 5,
            "data": {"five_hour": {"utilization": 42.0}},
        }
        (self.dir / "anthropic-usage-cache.json").write_text(json.dumps(cached))
        res = self.mod._cached_fetch("anthropic-usage", dict)
        self.assertFalse(res.is_stale or res.is_rate_limited)
        self.assertEqual(self.mod._usage_health(cached, now), [])
        # Headers old too: stale again, dated by the newest good reading.
        cached["headers_at"] = now - 2 * self.mod.STALE_AFTER_SECONDS
        alerts = self.mod._usage_health(cached, now)
        self.assertEqual(len(alerts), 1)
        self.assertIn("last good data from", alerts[0][2])
        # An auth error still wins over fresh headers.
        cached.update(headers_at=now - 5, auth_error=True)
        (self.dir / "anthropic-usage-cache.json").write_text(json.dumps(cached))
        self.assertTrue(self.mod._cached_fetch("anthropic-usage", dict).is_auth_error)

    def test_fresh_headers_defer_endpoint_poll(self):
        calls = []

        def fetch():
            calls.append(1)
            return {"five_hour": {"utilization": 1.0}}

        now = time.time()
        (self.dir / "anthropic-usage-cache.json").write_text(
            json.dumps(
                {
                    "fetched_at": now - self.mod.CACHE_SECONDS_TTL - 10,
                    "headers_at": now - 5,
                    "data": {"five_hour": {"utilization": 42.0}},
                }
            )
        )
        res = self.mod._cached_fetch("anthropic-usage", fetch)
        self.assertEqual(calls, [])
        self.assertEqual(res.data["five_hour"]["utilization"], 42.0)

        # Past HEADER_POLL_SECONDS the endpoint is polled again, and the
        # header bookkeeping survives the rewrite.
        (self.dir / "anthropic-usage-cache.json").write_text(
            json.dumps(
                {
                    "fetched_at": now - self.mod.HEADER_POLL_SECONDS - 10,
                    "headers_at": now - 5,
                    "headers": {"status": "allowed"},
                    "data": {},
                }
            )
        )
        self.mod._cached_fetch("anthropic-usage", fetch)
        self.assertEqual(calls, [1])
        self.assertEqual(self._cache()["headers"], {"status": "allowed"})


class BurstTest(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load()

    def _e(self, t, five, reset="R1"):
        return {"t": t, "five_hour": five, "five_hour_reset": reset}

    def test_rise_within_window_is_a_burst(self):
        entries = [self._e(NOW - 500, 10.0), self._e(NOW - 200, 11.0), self._e(NOW, 14.0)]
        rise, start, reset = self.mod._detect_burst(entries, NOW)
        self.assertEqual(rise, 4.0)
        self.assertEqual(start, NOW - 500)
        self.assertEqual(reset, "R1")
        alerts = self.mod._burst_alerts(entries, NOW)
        self.assertEqual(len(alerts), 1)
        self.assertTrue(alerts[0][0].startswith("burst:R1:"))
        self.assertIn("burst", alerts[0][2])

    def test_slow_rise_or_old_readings_are_not(self):
        self.assertIsNone(
            self.mod._detect_burst([self._e(NOW - 500, 10.0), self._e(NOW, 12.0)], NOW)
        )
        self.assertIsNone(
            self.mod._detect_burst([self._e(NOW - 900, 0.0), self._e(NOW, 12.0)], NOW)
        )
        self.assertIsNone(self.mod._detect_burst([self._e(NOW, 12.0)], NOW))
        self.assertEqual(self.mod._burst_alerts([], NOW), [])

    def test_readings_without_reset_are_ignored(self):
        entries = [{"t": NOW - 300, "five_hour": 1.0}, {"t": NOW, "five_hour": 50.0}]
        self.assertIsNone(self.mod._detect_burst(entries, NOW))

    def test_window_reset_is_not_compared_across(self):
        # 60% in the old window, 0 -> 2 in the new one: no burst.
        entries = [
            self._e(NOW - 300, 60.0, "R0"),
            self._e(NOW - 200, 0.0, "R1"),
            self._e(NOW, 2.0, "R1"),
        ]
        self.assertIsNone(self.mod._detect_burst(entries, NOW))
        # ... but 0 -> 5 right after a reset (cold wake) is one.
        entries[-1] = self._e(NOW, 5.0, "R1")
        self.assertIsNotNone(self.mod._detect_burst(entries, NOW))

    def test_alert_text_is_stable_across_the_episode(self):
        a = self.mod._burst_alerts([self._e(NOW - 300, 10.0), self._e(NOW - 60, 14.0)], NOW - 60)
        b = self.mod._burst_alerts(
            [self._e(NOW - 300, 10.0), self._e(NOW - 60, 14.0), self._e(NOW, 19.0)], NOW
        )
        self.assertEqual(a[0][0], b[0][0])
        self.assertEqual(a[0][2], b[0][2])

    def test_config_overrides_burst_thresholds(self):
        self.assertTrue(self.mod._apply_config({"burst_points": 10, "burst_window_seconds": 60}))
        self.assertEqual(self.mod.BURST_POINTS, 10.0)
        self.assertEqual(self.mod.BURST_WINDOW_SECONDS, 60.0)


class ProviderGateTest(unittest.TestCase):
    """Status and warnings appear only for the account the session is on."""

    def setUp(self):
        self.mod, _ = _load()
        self.addCleanup(mock.patch.stopall)
        tokens = {"anthropic": "tok-default", "anthropic#acct2": "tok-2"}
        mock.patch.object(
            self.mod, "_find_anthropic_token", side_effect=lambda slot="anthropic": tokens.get(slot)
        ).start()
        mock.patch.object(self.mod, "_find_poe_key", return_value="poe-key").start()
        self.fetch = mock.patch.object(self.mod, "_fetch_anthropic_result").start()
        self.fetch.return_value = self.mod.CacheResult({"five_hour": {"utilization": 90}})
        self.poe = mock.patch.object(self.mod, "_fetch_poe_usage", return_value="🅿 1k").start()
        self.pub = mock.patch.object(self.mod, "_publish_warnings").start()
        for name in ("_usage_health", "_credential_health", "_burst_alerts"):
            mock.patch.object(self.mod, name, return_value=[]).start()
        self.series = mock.patch.object(self.mod, "_read_series", return_value=[]).start()

    def _run(self, provider):
        ctx = mock.Mock()
        ctx.agent_info.return_value = {"model": {"provider": provider}}
        self.mod._refresh_once(ctx, "poe-key", "acp")
        return ctx

    def test_default_account_reports_its_own_quota(self):
        ctx = self._run("anthropic")
        self.fetch.assert_called_once_with("tok-default", "anthropic")
        self.assertEqual(self.pub.call_args[0][4], "anthropic")
        self.series.assert_called_once_with("anthropic-usage")
        self.poe.assert_not_called()
        self.assertIn("5h", ctx.set_status.call_args[0][0])

    def test_extra_account_reports_its_own_quota(self):
        self._run("anthropic#acct2")
        self.fetch.assert_called_once_with("tok-2", "anthropic#acct2")
        self.assertEqual(self.pub.call_args[0][4], "anthropic#acct2")
        self.series.assert_called_once_with("anthropic-usage-acct2")

    def test_account_without_token_reports_nothing(self):
        ctx = self._run("anthropic#nologin")
        self.fetch.assert_not_called()
        ctx.clear_section.assert_called_once()

    def test_poe_session_reports_poe_only(self):
        ctx = self._run("poe")
        self.fetch.assert_not_called()
        self.pub.assert_not_called()
        ctx.clear_section.assert_called_once()
        ctx.set_status.assert_called_once_with("🅿 1k")

    def test_other_provider_reports_nothing(self):
        for provider in ("openai", ""):
            ctx = self._run(provider)
            ctx.set_status.assert_called_once_with("")
            ctx.clear_section.assert_called_once()
        self.fetch.assert_not_called()
        self.poe.assert_not_called()
        self.pub.assert_not_called()

    def test_crossings_are_claimed_per_account(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        mock.patch.object(self.mod, "_cache_dir", return_value=pathlib.Path(tmp.name)).start()
        w = [("five_hour", 85.0, time.time() + 3600, "5h")]
        self.assertTrue(self.mod._claim_crossings(w, time.time(), slot="anthropic"))
        self.assertTrue(self.mod._claim_crossings(w, time.time(), slot="anthropic#acct2"))
        self.assertFalse(self.mod._claim_crossings(w, time.time(), slot="anthropic#acct2"))


if __name__ == "__main__":
    unittest.main()
