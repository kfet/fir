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

REFRESH_INTERVAL_SECONDS = 30  # how frequently to refresh stats from cache
CACHE_SECONDS_TTL = 300  # seconds — shared across all fir sessions
BACKOFF_BASE = 120  # initial backoff after 429 (seconds)
BACKOFF_MAX = 3600  # max backoff (60 minutes — oauth/usage can 429 for 30min+)

# Cache dict keys
_K_FETCHED_AT = "fetched_at"
_K_BACKOFF_UNTIL = "backoff_until"
_K_BACKOFF_DURATION = "backoff_duration"
_K_DATA = "data"

_stop_event = threading.Event()
_thread: threading.Thread | None = None

_CACHE_DIR = Path.home() / ".fir" / "agent"

_CONFIG_PATHS = ["~/.config/fir", "~/.fir/agent"]


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
    for base in _CONFIG_PATHS:
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
    cache_file = _CACHE_DIR / f"{cache_name}-cache.json"
    lock_file = _CACHE_DIR / f"{cache_name}-cache.lock"
    _CACHE_DIR.mkdir(parents=True, exist_ok=True)

    def _is_fresh(cached: dict) -> bool:
        return (time.time() - cached.get(_K_FETCHED_AT, 0)) < CACHE_SECONDS_TTL

    def _is_backed_off(cached: dict) -> bool:
        return time.time() < cached.get(_K_BACKOFF_UNTIL, 0)

    def _effective_backoff_duration(cached: dict) -> float:
        return cached.get(_K_BACKOFF_DURATION, 0) if _is_backed_off(cached) else 0

    def _write_cache(obj: dict) -> None:
        tmp_path = None
        try:
            tmp_fd, tmp_path = tempfile.mkstemp(dir=str(_CACHE_DIR), suffix=".tmp")
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
    now = datetime.now(UTC).astimezone(local_tz)

    parts: list[tuple[float, str]] = []
    for key, val in result.data.items():
        if key == "extra_usage" or not isinstance(val, dict):
            continue
        util = val.get("utilization")
        if util is None:
            continue

        label = key.replace("_", " ").title().replace("Five Hour", "5h").replace("Seven Day", "7d")

        reset_str = ""
        if resets_at := val.get("resets_at"):
            with contextlib.suppress(Exception):
                dt = datetime.fromisoformat(resets_at).astimezone(local_tz)
                reset_str = _fmt_countdown(max(0, int((dt - now).total_seconds()) // 60))

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
