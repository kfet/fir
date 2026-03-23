#!/usr/bin/env python3
# ---
# name: provider-usage
# description: Show LLM provider usage/limits in the status bar (Anthropic, Poe)
# builtin: true
# modes: tui
# events: session_start, session_shutdown
# ---
"""Periodically fetch Anthropic and/or Poe usage stats and display in the status bar.

Refreshes every 5 minutes. Uses a shared file cache with flock so multiple
fir sessions avoid redundant API calls and respect rate limits together.
"""

from __future__ import annotations

import contextlib
import fcntl
import json
import os
import random
import tempfile
import threading
import time
import urllib.error
import urllib.request
from datetime import UTC, datetime
from pathlib import Path

import fir_ext

REFRESH_INTERVAL = 300  # 5 minutes
CACHE_TTL = 120  # seconds — shared across all fir sessions
BACKOFF_BASE = 60  # initial backoff after 429 (seconds)
BACKOFF_MAX = 3600  # max backoff (60 minutes — oauth/usage can 429 for 30min+)

_stop_event = threading.Event()
_thread: threading.Thread | None = None

_CACHE_DIR = Path.home() / ".fir" / "agent"


class CacheResult:
    """Wraps cached data with staleness / rate-limit metadata."""

    __slots__ = ("data", "is_rate_limited", "is_stale")

    def __init__(self, data: dict | None, *, is_stale: bool = False, is_rate_limited: bool = False):
        self.data = data
        self.is_stale = is_stale
        self.is_rate_limited = is_rate_limited


class RateLimitedError(Exception):
    """Raised when the API returns HTTP 429."""

    def __init__(self, retry_after: float | None = None):
        self.retry_after = retry_after


def _read_json(path: str) -> dict | None:
    """Read a JSON file, returning None on any error."""
    try:
        with open(path) as f:
            return json.load(f)
    except Exception:
        return None


def _cached_fetch(cache_name: str, fetch_fn) -> CacheResult:
    """Fetch data using a shared file cache with flock to prevent thundering herd.

    cache_name: unique name for this cache (e.g. 'anthropic-usage')
    fetch_fn:   callable that returns a dict, or raises RateLimitedError

    Returns a CacheResult with data and staleness/rate-limit metadata.
    """
    cache_file = _CACHE_DIR / f"{cache_name}-cache.json"
    lock_file = _CACHE_DIR / f"{cache_name}-cache.lock"
    _CACHE_DIR.mkdir(parents=True, exist_ok=True)

    def _read_cache() -> dict | None:
        try:
            with open(cache_file) as f:
                return json.load(f)
        except Exception:
            return None

    def _is_fresh(cached: dict) -> bool:
        return (time.time() - cached.get("fetched_at", 0)) < CACHE_TTL

    def _is_backed_off(cached: dict) -> bool:
        return time.time() < cached.get("backoff_until", 0)

    def _effective_backoff_duration(cached: dict) -> float:
        """Return previous backoff duration, but reset to 0 if backoff has expired.

        This ensures that after a backoff window passes and we retry,
        a subsequent 429 starts fresh at BACKOFF_BASE instead of
        staying pinned at BACKOFF_MAX forever.
        """
        if _is_backed_off(cached):
            return cached.get("backoff_duration", 0)
        return 0  # expired — next 429 starts fresh

    def _write_cache(obj: dict) -> None:
        tmp_path = None
        try:
            tmp_fd, tmp_path = tempfile.mkstemp(dir=str(_CACHE_DIR), suffix=".tmp")
            with os.fdopen(tmp_fd, "w") as tmp_f:
                json.dump(obj, tmp_f)
            os.replace(tmp_path, str(cache_file))
            tmp_path = None  # replaced successfully, nothing to clean up
        except Exception:
            if tmp_path:
                with contextlib.suppress(OSError):
                    os.unlink(tmp_path)

    # Fast path: no lock needed if cache is fresh or backed off
    cached = _read_cache()
    if cached:
        if _is_backed_off(cached):
            return CacheResult(cached.get("data"), is_stale=True, is_rate_limited=True)
        if _is_fresh(cached) and cached.get("data") is not None:
            return CacheResult(cached["data"])

    # Slow path: acquire lock, re-check, fetch if still stale
    try:
        with open(lock_file, "w") as lf:
            fcntl.flock(lf, fcntl.LOCK_EX)
            try:
                cached = _read_cache()
                if cached:
                    if _is_backed_off(cached):
                        return CacheResult(cached.get("data"), is_stale=True, is_rate_limited=True)
                    if _is_fresh(cached) and cached.get("data") is not None:
                        return CacheResult(cached["data"])

                try:
                    result = fetch_fn()
                except RateLimitedError as e:
                    # Exponential backoff: double the previous backoff, capped
                    prev = _effective_backoff_duration(cached or {})
                    duration = min(max(prev * 2, BACKOFF_BASE), BACKOFF_MAX)
                    if e.retry_after and e.retry_after > 0:
                        duration = max(duration, e.retry_after)
                    # Add jitter (up to 50%) to desynchronize retries across machines
                    duration += random.uniform(0, duration * 0.5)  # noqa: S311
                    obj = dict(cached or {})
                    obj["backoff_until"] = time.time() + duration
                    obj["backoff_duration"] = duration
                    _write_cache(obj)
                    return CacheResult(obj.get("data"), is_stale=True, is_rate_limited=True)
                except Exception:
                    stale_data = (cached or {}).get("data")
                    return CacheResult(stale_data, is_stale=stale_data is not None)

                # Success — clear any backoff and write fresh data
                _write_cache(
                    {
                        "fetched_at": time.time(),
                        "data": result,
                    }
                )
                return CacheResult(result)
            finally:
                fcntl.flock(lf, fcntl.LOCK_UN)
    except Exception:
        # If locking fails, return stale cached data rather than
        # hammering the API without backoff protection.
        cached = _read_cache()
        stale_data = (cached or {}).get("data")
        return CacheResult(stale_data, is_stale=stale_data is not None)


# ---------------------------------------------------------------------------
# Token / key discovery
# ---------------------------------------------------------------------------


def _find_anthropic_token() -> str | None:
    """Find an OAuth bearer token for the Anthropic usage API."""
    auth = _read_json(os.path.expanduser("~/.fir/agent/auth.json"))
    if auth:
        tok = (auth.get("anthropic") or {}).get("access")
        if tok:
            return tok

    creds = _read_json(os.path.expanduser("~/.claude/.credentials.json"))
    if creds:
        tok = creds.get("claudeAiOauthToken") or creds.get("access_token")
        if tok:
            return tok

    return None


def _find_poe_key() -> str | None:
    """Find a Poe API key."""
    key = os.environ.get("POE_API_KEY")
    if key:
        return key

    models = _read_json(os.path.expanduser("~/.fir/agent/models.json"))
    if models:
        k = (models.get("providers") or {}).get("Poe", {}).get("apiKey")
        if k:
            return k

    auth = _read_json(os.path.expanduser("~/.fir/agent/auth.json"))
    if auth:
        k = (auth.get("poe") or {}).get("key")
        if k:
            return k

    return None


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
    d = total_min // 1440
    h = (total_min % 1440) // 60
    return f"{d}d{h}h"


# ---------------------------------------------------------------------------
# Provider fetch functions
# ---------------------------------------------------------------------------


def _fetch_anthropic_raw(token: str) -> dict:
    """Raw API call to Anthropic usage endpoint. Raises RateLimitedError on 429."""
    req = urllib.request.Request(
        "https://api.anthropic.com/api/oauth/usage",
        headers={
            "Authorization": f"Bearer {token}",
            "anthropic-beta": "oauth-2025-04-20",
            "Accept": "application/json",
        },
    )
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


def _fetch_anthropic_usage(token: str) -> str | None:
    """Fetch Anthropic usage (cached) and return a short status string."""
    result = _cached_fetch("anthropic-usage", lambda: _fetch_anthropic_raw(token))
    data = result.data
    if not data:
        if result.is_rate_limited:
            return "☁ (rate-limited)"
        return None

    local_tz = datetime.now().astimezone().tzinfo
    now = datetime.now(UTC).astimezone(local_tz)

    parts: list[tuple[float, str]] = []
    for key, val in data.items():
        if key == "extra_usage" or not isinstance(val, dict):
            continue
        util = val.get("utilization")
        if util is None:
            continue

        label = key.replace("_", " ").title()
        label = label.replace("Five Hour", "5h").replace("Seven Day", "7d")

        reset_str = ""
        resets_at = val.get("resets_at")
        if resets_at:
            with contextlib.suppress(Exception):
                dt = datetime.fromisoformat(resets_at).astimezone(local_tz)
                total_min = max(0, int((dt - now).total_seconds()) // 60)
                reset_str = _fmt_countdown(total_min)

        if reset_str:
            parts.append((util, f"{label} {util:.0f}% ({reset_str})"))
        else:
            parts.append((util, f"{label} {util:.0f}%"))

    if not parts:
        return None

    parts.sort(key=lambda x: -x[0])
    display = ", ".join(p[1] for p in parts[:3])
    suffix = ""
    if result.is_rate_limited:
        suffix = " ⚠ rate-limited"
    elif result.is_stale:
        suffix = " ⚠ stale"
    return f"☁ {display}{suffix}"


def _fetch_poe_raw(api_key: str) -> dict:
    """Raw API call to Poe balance endpoint. Raises RateLimitedError on 429."""
    req = urllib.request.Request(
        "https://api.poe.com/usage/current_balance",
        headers={"Authorization": f"Bearer {api_key}"},
    )
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


def _fetch_poe_usage(api_key: str) -> str | None:
    """Fetch Poe point balance (cached) and return a short status string."""
    result = _cached_fetch("poe-usage", lambda: _fetch_poe_raw(api_key))
    data = result.data
    if not data:
        if result.is_rate_limited:
            return "🅿 (rate-limited)"
        return None

    balance = data.get("current_point_balance")
    if balance is None:
        return None

    if balance >= 1_000_000:
        bal_str = f"{balance / 1_000_000:.1f}M"
    elif balance >= 1_000:
        bal_str = f"{balance / 1_000:.1f}k"
    else:
        bal_str = str(balance)

    suffix = ""
    if result.is_rate_limited:
        suffix = " ⚠ rate-limited"
    elif result.is_stale:
        suffix = " ⚠ stale"
    return f"🅿 {bal_str}pts{suffix}"


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

        if anthropic_token:
            s = _fetch_anthropic_usage(anthropic_token)
            if s:
                parts.append(s)

        if poe_key:
            s = _fetch_poe_usage(poe_key)
            if s:
                parts.append(s)

        if parts:
            with contextlib.suppress(Exception):
                ctx.set_status(" │ ".join(parts))

        _stop_event.wait(REFRESH_INTERVAL)


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
