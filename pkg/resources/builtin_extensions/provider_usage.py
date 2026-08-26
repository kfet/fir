#!/usr/bin/env python3
# ---
# name: provider-usage
# description: Show LLM provider usage/limits in the status bar (Anthropic, Poe)
# builtin: false
# modes: tui
# ---
"""Periodically fetch Anthropic and/or Poe usage stats and display in the status bar.

Refreshes every 5 minutes. Uses a shared file cache with flock so multiple
fir sessions avoid redundant API calls and respect rate limits together.

Also listens for ``provider_error`` and, when a Claude subscription (OAuth)
account hits a usage limit, notifies the user when that limit resets — reusing
the cache this extension already maintains, so the error path stays local.
"""

from __future__ import annotations

import contextlib
import fcntl
import json
import os
import random
import re
import tempfile
import threading
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

import fir_ext

REFRESH_INTERVAL_SECONDS = 30  # how frequently to refresh stats from cache
CACHE_SECONDS_TTL = 300  # seconds — shared across all fir sessions
BACKOFF_BASE = 120  # initial backoff after 429 (seconds)
BACKOFF_MAX = 3600  # max backoff (60 minutes — oauth/usage can 429 for 30min+)

# -- rate-limit reset reporting ---------------------------------------------
# A window's utilization at or above this is taken as the plausible cause of a
# rate-limit error. Below it the cache is no evidence at all — it may belong to
# a different account than the one that was just limited.
NEAR_LIMIT_UTILIZATION = 95.0
# Resets further out than the longest window (7 days, plus slack) are corrupt.
MAX_RESET_HORIZON = 8 * 24 * 3600
# How far a cached window's reset may sit from an instant parsed out of the
# error text and still be considered the same window.
WINDOW_MATCH_SLOP = 300
# A provider-indicated retry delay shorter than this is a transient backoff,
# not a usage-window reset — reporting it as one would be a lie.
MIN_RETRY_AFTER_AS_RESET = 60.0

WINDOW_LABELS = {"five_hour": "5-hour", "seven_day": "7-day"}

# Reset instants as they appear in Anthropic rate-limit bodies:
# "Claude AI usage limit reached|1740506400", `"resetsAt": 1740506400`,
# `"resets_at": "2026-02-25T18:00:00Z"`.
_RESET_PIPE_RE = re.compile(r"\|\s*(\d{10,13})\b")
_RESET_EPOCH_RE = re.compile(r'"resets?_?[aA]t"\s*:\s*"?(\d{10,13})"?')
_RESET_ISO_RE = re.compile(r'"resets?_?[aA]t"\s*:\s*"(\d{4}-\d{2}-\d{2}[Tt][^"]+)"')

# Cache dict keys
_K_FETCHED_AT = "fetched_at"
_K_BACKOFF_UNTIL = "backoff_until"
_K_BACKOFF_DURATION = "backoff_duration"
_K_DATA = "data"

_stop_event = threading.Event()
_thread: threading.Thread | None = None


def _cache_dir() -> Path:
    """Global config dir for cache files — lowest-priority (last) config dir."""
    if fir_ext.config_dirs:
        return Path(fir_ext.config_dirs[-1])
    return Path.home() / ".config" / "fir"


def _config_search_paths() -> list[str]:
    """All host-advertised config dirs (highest priority first) for key lookup."""
    if fir_ext.config_dirs:
        return list(fir_ext.config_dirs)
    return ["~/.config/fir"]


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


class CacheResult:
    """Wraps cached data with staleness / rate-limit metadata."""

    __slots__ = ("data", "is_rate_limited", "is_stale")

    def __init__(self, data: dict | None, *, is_stale: bool = False, is_rate_limited: bool = False):
        self.data = data
        self.is_stale = is_stale
        self.is_rate_limited = is_rate_limited

    def status_suffix(self) -> str:
        if self.is_rate_limited:
            return " ⚠ rate-limited"
        if self.is_stale:
            return " ⚠ stale"
        return ""


class RateLimitedError(Exception):
    """Raised when the API returns HTTP 429."""

    def __init__(self, retry_after: float | None = None):
        self.retry_after = retry_after


def _read_json(path: Path) -> dict | None:
    """Read a JSON file, returning None on any error."""
    try:
        with open(path) as f:
            return json.load(f)
    except Exception:
        return None


def _get_nested(d: dict | None, *keys) -> str | None:
    """Traverse nested dicts, returning None if any key is missing."""
    for k in keys:
        if not isinstance(d, dict):
            return None
        d = d.get(k)
    return d if isinstance(d, str) else None


def _search_configs(filename: str, *keys) -> str | None:
    """Search config paths for a nested key."""
    for base in _config_search_paths():
        val = _get_nested(_read_json(Path(f"{base}/{filename}").expanduser()), *keys)
        if val:
            return val
    return None


def _http_get_json(url: str, headers: dict) -> dict:
    """Make HTTP GET request, raising RateLimitedError on 429."""
    req = urllib.request.Request(url, headers=headers)  # noqa: S310
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:  # noqa: S310
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        if e.code == 429:
            retry_after = None
            with contextlib.suppress(Exception):
                retry_after = float(e.headers.get("Retry-After", ""))
            raise RateLimitedError(retry_after) from e
        raise


# ---------------------------------------------------------------------------
# Caching
# ---------------------------------------------------------------------------


def _cached_fetch(cache_name: str, fetch_fn) -> CacheResult:
    """Fetch data using a shared file cache with flock to prevent thundering herd."""
    cd = _cache_dir()
    cache_file = cd / f"{cache_name}-cache.json"
    lock_file = cd / f"{cache_name}-cache.lock"
    cd.mkdir(parents=True, exist_ok=True)

    def _is_fresh(cached: dict) -> bool:
        return (time.time() - cached.get(_K_FETCHED_AT, 0)) < CACHE_SECONDS_TTL

    def _is_backed_off(cached: dict) -> bool:
        return time.time() < cached.get(_K_BACKOFF_UNTIL, 0)

    def _effective_backoff_duration(cached: dict) -> float:
        return cached.get(_K_BACKOFF_DURATION, 0) if _is_backed_off(cached) else 0

    def _write_cache(obj: dict) -> None:
        tmp_path = None
        try:
            tmp_fd, tmp_path = tempfile.mkstemp(dir=str(cd), suffix=".tmp")
            with os.fdopen(tmp_fd, "w") as tmp_f:
                json.dump(obj, tmp_f)
            os.replace(tmp_path, str(cache_file))
            tmp_path = None
        except Exception:
            if tmp_path:
                with contextlib.suppress(OSError):
                    os.unlink(tmp_path)

    def _cache_to_result(cached: dict | None) -> CacheResult | None:
        """Convert cached dict to CacheResult if usable, else None to signal refetch needed."""
        if not cached:
            return None
        if _is_backed_off(cached):
            return CacheResult(cached.get(_K_DATA), is_stale=True, is_rate_limited=True)
        if _is_fresh(cached) and cached.get(_K_DATA) is not None:
            return CacheResult(cached[_K_DATA])
        return None

    def _stale_result(cached: dict | None) -> CacheResult:
        """Return stale data as a fallback."""
        stale_data = (cached or {}).get(_K_DATA)
        return CacheResult(stale_data, is_stale=stale_data is not None)

    # Fast path: no lock needed if cache is fresh or backed off
    if result := _cache_to_result(_read_json(cache_file)):
        return result

    # Slow path: acquire lock, re-check, fetch if still stale
    try:
        with open(lock_file, "w") as lf:
            fcntl.flock(lf, fcntl.LOCK_EX)
            try:
                cached = _read_json(cache_file)
                if result := _cache_to_result(cached):
                    return result

                try:
                    data = fetch_fn()
                except RateLimitedError as e:
                    prev = _effective_backoff_duration(cached or {})
                    base = min(max(prev * 2, BACKOFF_BASE), BACKOFF_MAX)
                    if e.retry_after and e.retry_after > 0:
                        base = max(base, e.retry_after)
                    jitter = random.uniform(0, base * 0.5)  # noqa: S311
                    obj = dict(cached or {})
                    obj[_K_BACKOFF_UNTIL] = time.time() + base + jitter
                    obj[_K_BACKOFF_DURATION] = base  # store base, not jittered
                    _write_cache(obj)
                    return CacheResult(obj.get(_K_DATA), is_stale=True, is_rate_limited=True)
                except Exception:
                    return _stale_result(cached)

                _write_cache({_K_FETCHED_AT: time.time(), _K_DATA: data})
                return CacheResult(data)
            finally:
                fcntl.flock(lf, fcntl.LOCK_UN)
    except Exception:
        return _stale_result(_read_json(cache_file))


# ---------------------------------------------------------------------------
# Token / key discovery
# ---------------------------------------------------------------------------


def _find_anthropic_token() -> str | None:
    """Find an OAuth bearer token for the Anthropic usage API."""
    if tok := _search_configs("auth.json", "anthropic", "access"):
        return tok
    creds = _read_json(Path("~/.claude/.credentials.json").expanduser())
    return _get_nested(creds, "claudeAiOauthToken") or _get_nested(creds, "access_token")


def _find_poe_key() -> str | None:
    """Find a Poe API key."""
    if key := os.environ.get("POE_API_KEY"):
        return key
    if key := _search_configs("models.json", "providers", "Poe", "apiKey"):
        return key
    return _search_configs("auth.json", "poe", "key")


# ---------------------------------------------------------------------------
# Formatting helpers
# ---------------------------------------------------------------------------


def _fmt_countdown(total_min: int) -> str:
    """Format minutes remaining as a compact countdown string."""
    if total_min < 60:
        return f"{total_min}m"
    if total_min < 1440:
        h, m = divmod(total_min, 60)
        return f"{h}h{m:02d}m"
    d, rem = divmod(total_min, 1440)
    return f"{d}d{rem // 60}h"


def _parse_iso(value: object) -> datetime | None:
    """Parse an ISO-8601 instant, returning None on anything unparseable.

    ``datetime.fromisoformat`` only learned to accept a trailing ``Z`` in
    3.11, and fir supports Python 3.9 — so normalise it here rather than
    silently dropping every ``resets_at`` the API returns in UTC.
    """
    if not isinstance(value, str) or not value:
        return None
    try:
        dt = datetime.fromisoformat(value.replace("Z", "+00:00").replace("z", "+00:00"))
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt


# ---------------------------------------------------------------------------
# Provider fetch functions
# ---------------------------------------------------------------------------


def _fetch_anthropic_usage(token: str) -> str | None:
    """Fetch Anthropic usage (cached) and return a short status string."""
    result = _cached_fetch(
        "anthropic-usage",
        lambda: _http_get_json(
            "https://api.anthropic.com/api/oauth/usage",
            {
                "Authorization": f"Bearer {token}",
                "anthropic-beta": "oauth-2025-04-20",
                "Accept": "application/json",
            },
        ),
    )
    if not result.data:
        return "☁ (rate-limited)" if result.is_rate_limited else None

    local_tz = datetime.now().astimezone().tzinfo
    now = datetime.now(timezone.utc).astimezone(local_tz)

    parts: list[tuple[float, str]] = []
    for key, val in result.data.items():
        if key == "extra_usage" or not isinstance(val, dict):
            continue
        util = val.get("utilization")
        if util is None:
            continue

        label = key.replace("_", " ").title().replace("Five Hour", "5h").replace("Seven Day", "7d")

        reset_str = ""
        if (dt := _parse_iso(val.get("resets_at"))) is not None:
            reset_str = _fmt_countdown(
                max(0, int((dt.astimezone(local_tz) - now).total_seconds()) // 60)
            )

        parts.append(
            (util, f"{label} {util:.0f}% ({reset_str})" if reset_str else f"{label} {util:.0f}%")
        )

    if not parts:
        return None

    parts.sort(key=lambda x: -x[0])
    return f"☁ {', '.join(p[1] for p in parts[:3])}{result.status_suffix()}"


def _fetch_poe_usage(api_key: str) -> str | None:
    """Fetch Poe point balance (cached) and return a short status string."""
    result = _cached_fetch(
        "poe-usage",
        lambda: _http_get_json(
            "https://api.poe.com/usage/current_balance", {"Authorization": f"Bearer {api_key}"}
        ),
    )
    if not result.data:
        return "🅿 (rate-limited)" if result.is_rate_limited else None

    balance = result.data.get("current_point_balance")
    if balance is None:
        return None

    if balance >= 1_000_000:
        bal_str = f"{balance / 1_000_000:.1f}M"
    elif balance >= 1_000:
        bal_str = f"{balance / 1_000:.1f}k"
    else:
        bal_str = str(balance)

    return f"🅿 {bal_str}pts{result.status_suffix()}"


# ---------------------------------------------------------------------------
# Rate-limit reset reporting
# ---------------------------------------------------------------------------
#
# When a Claude subscription account hits its 5-hour or 7-day usage limit, the
# error says only that a limit was hit. Everything needed to say *when it
# resets* is already here: the oauth/usage cache this extension maintains, and
# the reset instant Anthropic often puts in the error body itself. Sources are
# consulted cheapest-and-most-authoritative first, and nothing is said at all
# unless one of them yields a plausible instant.


def _cached_usage_windows() -> dict[str, dict]:
    """Read the cached oauth/usage windows — a local file read, never a fetch.

    Deliberately bypasses :func:`_cached_fetch`: that refreshes on expiry,
    which would put a blocking HTTP call on the error path. The background
    refresh loop keeps this file within its TTL anyway, and a window's
    ``resets_at`` does not move while the window is open.
    """
    cached = _read_json(_cache_dir() / "anthropic-usage-cache.json") or {}
    data = cached.get(_K_DATA)
    if not isinstance(data, dict):
        return {}
    return {k: v for k, v in data.items() if isinstance(v, dict) and v.get("resets_at")}


def _reset_from_text(text: str) -> float | None:
    """Extract a reset instant (epoch seconds) from an error body."""
    for pattern in (_RESET_PIPE_RE, _RESET_EPOCH_RE):
        if m := pattern.search(text):
            raw = m.group(1)
            return int(raw) / 1000 if len(raw) > 10 else float(raw)
    if m := _RESET_ISO_RE.search(text):
        dt = _parse_iso(m.group(1))
        if dt is not None:
            return dt.timestamp()
    return None


def _reset_plausible(reset: float, now: float) -> bool:
    """Reject resets already in the past (stale) or absurdly far out (corrupt)."""
    return now < reset <= now + MAX_RESET_HORIZON


def _reset_from_windows(windows: dict[str, dict], now: float) -> tuple[float, str] | None:
    """Pick the window that most likely caused the error: the earliest future
    reset among windows at or above the near-limit utilization."""
    best: tuple[float, str] | None = None
    for name, win in windows.items():
        util = win.get("utilization")
        if not isinstance(util, (int, float)) or util < NEAR_LIMIT_UTILIZATION:
            continue
        dt = _parse_iso(win.get("resets_at"))
        if dt is None or not _reset_plausible(dt.timestamp(), now):
            continue
        if best is None or dt.timestamp() < best[0]:
            best = (dt.timestamp(), WINDOW_LABELS.get(name, ""))
    return best


def _window_label_for(windows: dict[str, dict], reset: float) -> str:
    """Label a reset instant by matching it against the cached windows.

    Returns "" when no window matches — the window is never guessed at.
    """
    for name, win in windows.items():
        dt = _parse_iso(win.get("resets_at"))
        if dt is not None and abs(dt.timestamp() - reset) < WINDOW_MATCH_SLOP:
            return WINDOW_LABELS.get(name, "")
    return ""


def _format_reset_notice(reset: float, label: str, now: float) -> str:
    """Render the user-visible notice, in local time with a relative hint."""
    local = datetime.fromtimestamp(reset).astimezone()
    stamp = f"{local:%b} {local.day}, {local.hour % 12 or 12}:{local:%M %p %Z}"
    countdown = _fmt_countdown(max(0, int((reset - now) // 60)))
    return f"Anthropic {label or 'usage'} limit reached — resets {stamp} (in {countdown})"


def _rate_limit_notice(
    params: fir_ext.ProviderErrorParams, windows: dict[str, dict], now: float
) -> str | None:
    """Build the notice for a rate-limit provider_error, or None to stay quiet."""
    reset = _reset_from_text(params.get("error_text") or "")
    if reset is not None and _reset_plausible(reset, now):
        return _format_reset_notice(reset, _window_label_for(windows, reset), now)

    if found := _reset_from_windows(windows, now):
        return _format_reset_notice(found[0], found[1], now)

    # Last resort: a provider-indicated delay long enough to be a window reset
    # rather than a transient backoff.
    retry_after_ms = params.get("retry_after_ms") or 0
    if retry_after_ms / 1000 >= MIN_RETRY_AFTER_AS_RESET:
        reset = now + retry_after_ms / 1000
        if _reset_plausible(reset, now):
            return _format_reset_notice(reset, _window_label_for(windows, reset), now)
    return None


@fir_ext.on("provider_error")
def on_provider_error(params: fir_ext.ProviderErrorParams, ctx: fir_ext.Context) -> None:
    """Tell the user when an Anthropic subscription usage limit resets."""
    if not params or params.get("kind") != "rate_limit":
        return
    if "anthropic" not in (params.get("provider") or "").lower():
        return
    # OAuth/subscription path only: an API-key account has no oauth/usage
    # windows and no token here. Only the presence of a token is consulted —
    # its value is never read into a message or a log.
    if not _find_anthropic_token():
        return
    if notice := _rate_limit_notice(params, _cached_usage_windows(), time.time()):
        with contextlib.suppress(Exception):
            ctx.notify(notice, level="warning")


# ---------------------------------------------------------------------------
# Extension lifecycle
# ---------------------------------------------------------------------------


def _refresh_loop(ctx: fir_ext.Context) -> None:
    """Periodically fetch usage and update the status bar."""
    anthropic_token = _find_anthropic_token()
    poe_key = _find_poe_key()

    if not anthropic_token and not poe_key:
        return

    while not _stop_event.is_set():
        parts = []
        if anthropic_token and (s := _fetch_anthropic_usage(anthropic_token)):
            parts.append(s)
        if poe_key and (s := _fetch_poe_usage(poe_key)):
            parts.append(s)
        if parts:
            with contextlib.suppress(Exception):
                ctx.set_status(" │ ".join(parts))
        _stop_event.wait(REFRESH_INTERVAL_SECONDS)


@fir_ext.on("session_start")
def on_session_start(params: dict, ctx: fir_ext.Context) -> None:
    global _thread
    if _thread is not None and _thread.is_alive():
        return
    _stop_event.clear()
    _thread = threading.Thread(target=_refresh_loop, args=(ctx,), daemon=True)
    _thread.start()


@fir_ext.on("session_shutdown")
def on_session_shutdown(params: dict, ctx: fir_ext.Context) -> None:
    _stop_event.set()


fir_ext.run(name="provider-usage")
