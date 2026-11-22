#!/usr/bin/env python3
# ---
# name: provider-usage
# description: Show LLM provider usage/limits in the status bar (Anthropic, Poe)
# builtin: true
# modes: tui, acp
# ---
"""Show Anthropic and/or Poe usage in the status bar.

Anthropic numbers come only from inference response headers — the
``/api/oauth/usage`` endpoint is never called (it intermittently rejects
tokens that still work for inference, which raised false auth alerts).
``message_end`` / ``provider_error`` carry the provider's HTTP
``response_headers``; per-provider parsers (``HEADER_PARSERS``) turn the
account-wide ``anthropic-ratelimit-unified-*`` headers into usage windows.
Each reading updates a per-host shared cache (flock'd, ``headers_at``,
``headers``) and a short time-series (``anthropic-usage-series.json``). A
session shows the last cached reading with its age (``☁ 5h 4% · 12m ago``),
or nothing until a first reading lands. Poe's balance is fetched from its API
through the same flock'd cache, once per ``CACHE_SECONDS_TTL`` across sessions,
backing off exponentially on failure.

Early warning (all modes): when a usage window crosses a warning level
(80% / 95%) the extension publishes a short ``set_section`` so the agent
sees it on every turn, and announces each crossing exactly once per window
reset — a ``notify`` in the TUI, a turn-triggering message in ACP (where
``notify`` and the status bar have no surface). A flock'd state file in the
shared cache dir makes "exactly once" hold across every concurrent session.
A rise of ``burst_points`` (default 3) in 5-hour utilization within
``burst_window_seconds`` (default 600) inside one window — typically many idle
sessions waking at once after a reset — is detected by ``_burst_alerts``; it is
kept out of the refresh loop since v1.46.4 (in the prompt it stalled subagents).

Also listens for ``provider_error`` and, when a Claude subscription (OAuth)
account hits a usage limit, notifies the user when that limit resets.

Credential health (section + exactly-once notify in the TUI; section only in
ACP, never a triggered turn): the stored Anthropic OAuth access token is past
expiry by more than ``refresh_grace_minutes`` (fir's refresh is failing — log
in again); the login is older than ``login_max_age_days`` (off by default;
the clock starts when a login is first observed — delete
``anthropic-login-seen.json`` after re-login to reset it).

Config: ``provider-usage.json`` in any fir config dir, e.g.
``{"enabled": true, "warn_levels": [80, 95], "refresh_grace_minutes": 15,
"login_max_age_days": 0, "burst_points": 3, "burst_window_seconds": 600}``.
Unknown keys (e.g. the retired ``stale_after_seconds`` /
``endpoint_poll_seconds``) are ignored. ``"enabled": false`` turns the
extension off entirely.
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
BACKOFF_MAX = 3600  # max backoff (60 minutes)

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

# -- early warning ------------------------------------------------------------
# Utilization levels (percent) that each announce once per window reset.
WARN_LEVELS = (80.0, 95.0)
REFRESH_GRACE_MINUTES = 15.0
LOGIN_MAX_AGE_DAYS = 0.0  # 0 disables the login-age warning
ALERT_MEMORY = 7 * 24 * 3600  # how long an announced health alert stays claimed

# -- header feed ----------------------------------------------------------------
SERIES_MAX_AGE = 6 * 3600  # how much time-series history to keep
SERIES_MAX_LEN = 500
BURST_POINTS = 3.0  # 5-hour utilization rise (percentage points) ...
BURST_WINDOW_SECONDS = 600.0  # ... within this long counts as a burst
BURST_BUCKET = 1800  # one burst alert episode per bucket per window

# Cache dict keys
_K_FETCHED_AT = "fetched_at"
_K_BACKOFF_UNTIL = "backoff_until"
_K_BACKOFF_DURATION = "backoff_duration"
_K_DATA = "data"
_K_RATE_LIMITED = "rate_limited"
_K_HEADERS_AT = "headers_at"
_K_HEADERS = "headers"

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

    def __init__(
        self,
        data: dict | None,
        *,
        is_stale: bool = False,
        is_rate_limited: bool = False,
    ):
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

    def _is_backed_off(cached: dict) -> bool:
        return time.time() < cached.get(_K_BACKOFF_UNTIL, 0)

    def _cache_to_result(cached: dict | None) -> CacheResult | None:
        """Convert cached dict to CacheResult if usable, else None to signal refetch needed."""
        if not cached:
            return None
        if _is_backed_off(cached):
            return CacheResult(
                cached.get(_K_DATA),
                is_stale=True,
                is_rate_limited=bool(cached.get(_K_RATE_LIMITED)),
            )
        age = time.time() - cached.get(_K_FETCHED_AT, 0)
        if age < CACHE_SECONDS_TTL and cached.get(_K_DATA) is not None:
            return CacheResult(cached[_K_DATA])
        return None

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
                except Exception as e:
                    # Every failure backs off, not just 429: otherwise a dead
                    # key or endpoint is re-polled by every session each cycle.
                    prev = cached or {}
                    prev_dur = prev.get(_K_BACKOFF_DURATION, 0) if _is_backed_off(prev) else 0
                    base = min(max(prev_dur * 2, BACKOFF_BASE), BACKOFF_MAX)
                    retry_after = getattr(e, "retry_after", None)
                    if retry_after and retry_after > 0:
                        base = max(base, retry_after)
                    jitter = random.uniform(0, base * 0.5)  # noqa: S311
                    obj = dict(prev)
                    obj[_K_BACKOFF_UNTIL] = time.time() + base + jitter
                    obj[_K_BACKOFF_DURATION] = base  # store base, not jittered
                    obj[_K_RATE_LIMITED] = isinstance(e, RateLimitedError)
                    _atomic_write_json(cache_file, obj)
                    return CacheResult(
                        obj.get(_K_DATA),
                        is_stale=obj.get(_K_DATA) is not None,
                        is_rate_limited=bool(obj[_K_RATE_LIMITED]),
                    )

                _atomic_write_json(cache_file, {_K_FETCHED_AT: time.time(), _K_DATA: data})
                return CacheResult(data)
            finally:
                fcntl.flock(lf, fcntl.LOCK_UN)
    except Exception:
        stale = (_read_json(cache_file) or {}).get(_K_DATA)
        return CacheResult(stale, is_stale=stale is not None)


# ---------------------------------------------------------------------------
# Token / key discovery
# ---------------------------------------------------------------------------


DEFAULT_SLOT = "anthropic"


def _is_anthropic_slot(provider: str) -> bool:
    """True for the default Anthropic account and for extra accounts
    ("anthropic#<id>"). Each slot is its own account with its own quota."""
    return provider == DEFAULT_SLOT or provider.startswith(DEFAULT_SLOT + "#")


def _cache_name(slot: str = DEFAULT_SLOT) -> str:
    """Per-account cache stem. The default account keeps the historic
    ``anthropic-usage`` name (other extensions read it)."""
    if slot == DEFAULT_SLOT:
        return "anthropic-usage"
    ident = re.sub(r"[^A-Za-z0-9_.-]", "_", slot.split("#", 1)[1])
    return f"anthropic-usage-{ident}"


def _find_anthropic_token(slot: str = DEFAULT_SLOT) -> str | None:
    """Find the OAuth access token of one account slot. Only its presence is
    used (to tell a subscription account from an API-key one); never sent."""
    if tok := _search_configs("auth.json", slot, "access"):
        return tok
    if slot != DEFAULT_SLOT:
        return None  # Claude Code's credential is the default account only
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


def _anthropic_cached(slot: str = DEFAULT_SLOT) -> tuple[dict | None, float]:
    """Last header reading of one account from the shared cache: (windows,
    headers_at). A local file read — the usage endpoint is never called."""
    cached = _read_json(_cache_dir() / f"{_cache_name(slot)}-cache.json") or {}
    data = cached.get(_K_DATA)
    at = cached.get(_K_HEADERS_AT)
    if not isinstance(data, dict) or not isinstance(at, (int, float)) or at <= 0:
        return None, 0.0
    return data, float(at)


def _fmt_age(seconds: float) -> str:
    """Compact age of a reading: 'just now', '12m ago', '3h05m ago'."""
    minutes = max(0, int(seconds // 60))
    return "just now" if minutes < 1 else f"{_fmt_countdown(minutes)} ago"


def _format_anthropic_status(data: dict | None, age: float) -> str | None:
    if not data:
        return None

    local_tz = datetime.now().astimezone().tzinfo
    now = datetime.now(timezone.utc).astimezone(local_tz)

    parts: list[tuple[float, str]] = []
    for key, val in data.items():
        if key == "extra_usage" or not isinstance(val, dict):
            continue
        util = val.get("utilization")
        if not isinstance(util, (int, float)):
            continue

        label = key.replace("_", " ").title().replace("Five Hour", "5h").replace("Seven Day", "7d")

        reset_str = ""
        if (dt := _parse_iso(val.get("resets_at"))) is not None:
            left = int((dt.astimezone(local_tz) - now).total_seconds())
            if left <= 0:
                continue  # window reset since this reading: its number is void
            reset_str = _fmt_countdown(left // 60)

        parts.append(
            (util, f"{label} {util:.0f}% ({reset_str})" if reset_str else f"{label} {util:.0f}%")
        )

    if not parts:
        return None

    parts.sort(key=lambda x: -x[0])
    return f"☁ {', '.join(p[1] for p in parts[:3])} · {_fmt_age(age)}"


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
# Header feed: quota readings from provider response headers
# ---------------------------------------------------------------------------
#
# Each parser takes a response-header map (lowercase names) and returns
# ``(cache_name, windows, extra)`` or None. ``windows`` has the shape
# ``{window_key: {"utilization": percent, "resets_at": iso}}`` that every
# consumer of the shared cache reads. ``extra`` is the raw provider-specific detail kept beside
# them. Add a provider by appending to HEADER_PARSERS.

_ANTHROPIC_UNIFIED = "anthropic-ratelimit-unified-"
# header window prefix -> cache window key
_ANTHROPIC_WINDOWS = {"5h": "five_hour", "7d": "seven_day"}


def _iso_from_epoch(value: object) -> str | None:
    try:
        epoch = float(str(value).strip())
    except (TypeError, ValueError):
        return None
    if epoch <= 0:
        return None
    if epoch > 1e11:  # milliseconds
        epoch /= 1000
    return datetime.fromtimestamp(epoch, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _parse_anthropic_unified(headers: dict) -> tuple[str, dict, dict] | None:
    """Anthropic subscription (OAuth) quota headers: account-wide 5h / 7d
    utilization as a 0..1 fraction and reset instants in epoch seconds."""
    extra = {
        k[len(_ANTHROPIC_UNIFIED) :]: v
        for k, v in headers.items()
        if k.startswith(_ANTHROPIC_UNIFIED) and isinstance(v, str)
    }
    windows: dict[str, dict] = {}
    for prefix, key in _ANTHROPIC_WINDOWS.items():
        try:
            frac = float(extra.get(f"{prefix}-utilization", ""))
        except ValueError:
            continue
        win: dict = {"utilization": round(frac * 100, 2)}
        if (iso := _iso_from_epoch(extra.get(f"{prefix}-reset"))) is not None:
            win["resets_at"] = iso
        windows[key] = win
    if not windows:
        return None
    return "anthropic-usage", windows, extra


# provider base id -> parser. An extra account's slot ("anthropic#<account>")
# uses the same parser but feeds its own cache (see _cache_name).
HEADER_PARSERS = {"anthropic": _parse_anthropic_unified}


def _parse_headers(provider: str, headers: object) -> tuple[str, dict, dict] | None:
    if not isinstance(headers, dict) or not headers:
        return None
    provider = provider or ""
    parser = HEADER_PARSERS.get(provider.split("#", 1)[0])
    if parser is None:
        return None
    try:
        found = parser(headers)
    except Exception:
        return None
    if found is None:
        return None
    # Each account slot feeds its own cache: quotas are per account.
    _name, windows, extra = found
    return _cache_name(provider), windows, extra


def _atomic_write_json(path: Path, obj: object) -> None:
    tmp_fd, tmp_path = tempfile.mkstemp(dir=str(path.parent), suffix=".tmp")
    try:
        with os.fdopen(tmp_fd, "w") as f:
            json.dump(obj, f)
        os.replace(tmp_path, str(path))
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp_path)
        raise


def _ingest_reading(cache_name: str, windows: dict, extra: dict, now: float) -> None:
    """Merge a header reading into the shared cache and its time-series.

    Windows a reading does not carry keep their previous values.
    """
    cd = _cache_dir()
    cd.mkdir(parents=True, exist_ok=True)
    cache_file = cd / f"{cache_name}-cache.json"
    series_file = cd / f"{cache_name}-series.json"
    with open(cd / f"{cache_name}-cache.lock", "w") as lf:
        fcntl.flock(lf, fcntl.LOCK_EX)
        try:
            cached = _read_json(cache_file) or {}
            if float(cached.get(_K_HEADERS_AT) or 0) > now:
                return  # a newer reading already landed
            data = cached.get(_K_DATA)
            data = dict(data) if isinstance(data, dict) else {}
            for key, win in windows.items():
                merged = dict(data.get(key) or {}) if isinstance(data.get(key), dict) else {}
                merged.update(win)
                data[key] = merged
            cached[_K_DATA] = data
            cached[_K_HEADERS_AT] = now
            cached[_K_HEADERS] = extra
            _atomic_write_json(cache_file, cached)

            series = _read_json(series_file)
            entries = series.get("entries") if isinstance(series, dict) else None
            entries = [e for e in entries or [] if isinstance(e, dict)]
            entry: dict = {"t": now}
            for key, win in windows.items():
                entry[key] = win.get("utilization")
                if win.get("resets_at"):
                    entry[f"{key}_reset"] = win["resets_at"]
            entries.append(entry)
            entries = [e for e in entries if now - float(e.get("t") or 0) <= SERIES_MAX_AGE]
            _atomic_write_json(series_file, {"entries": entries[-SERIES_MAX_LEN:]})
        finally:
            fcntl.flock(lf, fcntl.LOCK_UN)


def _ingest_headers(provider: str, headers: object, now: float) -> bool:
    """Parse and store a response's quota headers; True if a reading landed."""
    found = _parse_headers(provider, headers)
    if found is None:
        return False
    try:
        _ingest_reading(*found, now)
    except OSError:
        return False
    return True


def _read_series(cache_name: str = "anthropic-usage") -> list[dict]:
    series = _read_json(_cache_dir() / f"{cache_name}-series.json")
    entries = series.get("entries") if isinstance(series, dict) else None
    return [e for e in entries or [] if isinstance(e, dict)]


def _detect_burst(entries: list[dict], now: float, key: str = "five_hour") -> tuple | None:
    """Return (rise_points, start_t, reset_iso) when ``key`` utilization rose
    by at least BURST_POINTS within the last BURST_WINDOW_SECONDS, comparing
    only readings of the same window (same reset instant)."""
    recent = [
        e
        for e in entries
        if now - float(e.get("t") or 0) <= BURST_WINDOW_SECONDS
        and isinstance(e.get(key), (int, float))
        and e.get(f"{key}_reset")  # unknown window: never compared
    ]
    if len(recent) < 2:
        return None
    latest = max(recent, key=lambda e: e["t"])
    reset = latest.get(f"{key}_reset")
    same = [e for e in recent if e.get(f"{key}_reset") == reset]
    low = min(same, key=lambda e: e[key])
    rise = float(latest[key]) - float(low[key])
    if rise < BURST_POINTS:
        return None
    return rise, float(low["t"]), reset


def _burst_alerts(entries: list[dict], now: float) -> list:
    """Health-alert tuples (tag, expiry, text) for a detected usage burst.

    The text names the threshold, not the live numbers, so the section stays
    byte-stable for the whole episode (one per window per BURST_BUCKET).
    """
    found = _detect_burst(entries, now)
    if found is None:
        return []
    _rise, start, reset = found
    bucket = int(start) // BURST_BUCKET * BURST_BUCKET
    return [
        (
            f"burst:{reset}:{bucket}",
            now + ALERT_MEMORY,
            f"Anthropic usage burst: account-wide 5-hour utilization rose "
            f"≥{BURST_POINTS:g} points within {BURST_WINDOW_SECONDS / 60:g} min "
            f"(since {_fmt_clock(bucket)}) — likely many sessions resuming at once "
            "(cold prompt caches); informational only.",
        )
    ]


@fir_ext.on("message_end")
def on_message_end(params: fir_ext.MessageEndParams, ctx: fir_ext.Context) -> None:
    """Record quota readings carried by the provider's response headers."""
    if not params or params.get("role") != "assistant":
        return
    _ingest_headers(params.get("provider") or "", params.get("response_headers"), time.time())


# ---------------------------------------------------------------------------
# Rate-limit reset reporting
# ---------------------------------------------------------------------------
#
# When a Claude subscription account hits its 5-hour or 7-day usage limit, the
# error says only that a limit was hit. Everything needed to say *when it
# resets* is already here: the header cache this extension maintains, and
# the reset instant Anthropic often puts in the error body itself. Sources are
# consulted cheapest-and-most-authoritative first, and nothing is said at all
# unless one of them yields a plausible instant.


def _cached_usage_windows(slot: str = DEFAULT_SLOT) -> dict[str, dict]:
    """Read the cached usage windows — a local file read, never a fetch.
    A window's ``resets_at`` does not move while the window is open."""
    cached = _read_json(_cache_dir() / f"{_cache_name(slot)}-cache.json") or {}
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
    if not params:
        return
    # A failed response (429) carries the freshest quota reading of all.
    _ingest_headers(params.get("provider") or "", params.get("response_headers"), time.time())
    if params.get("kind") != "rate_limit":
        return
    slot = params.get("provider") or ""
    if not _is_anthropic_slot(slot):
        return
    # OAuth/subscription path only: an API-key account has no unified
    # windows and no token here. Only the presence of a token is consulted —
    # its value is never read into a message or a log.
    if not _find_anthropic_token(slot):
        return
    if notice := _rate_limit_notice(params, _cached_usage_windows(slot), time.time()):
        with contextlib.suppress(Exception):
            ctx.notify(notice, level="warning")


# ---------------------------------------------------------------------------
# Early warning
# ---------------------------------------------------------------------------


def _usage_warnings(data: dict | None, now: float) -> list[tuple[str, float, float, str]]:
    """Return (window_key, utilization, reset_epoch, label) for every window at
    or above the lowest warning level whose reset is still in the future."""
    out = []
    if not isinstance(data, dict):
        return out
    for key, val in data.items():
        if key not in WINDOW_LABELS or not isinstance(val, dict):
            continue
        util = val.get("utilization")
        dt = _parse_iso(val.get("resets_at"))
        if not isinstance(util, (int, float)) or dt is None:
            continue
        reset = dt.timestamp()
        if util >= WARN_LEVELS[0] and reset > now:
            out.append((key, float(util), reset, WINDOW_LABELS[key]))
    out.sort(key=lambda w: -w[1])
    return out


def _warning_text(
    warnings: list[tuple[str, float, float, str]], now: float, *, countdown: bool = True
) -> str:
    parts = []
    for _key, util, reset, label in warnings:
        when = datetime.fromtimestamp(reset).astimezone().strftime("%a %H:%M %Z")
        if countdown:
            when += f", in {_fmt_countdown(max(0, int(reset - now) // 60))}"
            pct = f"{util:.0f}%"
        else:
            # Section text: crossed level only, so it stays byte-stable as usage creeps.
            pct = f"≥{max(lv for lv in WARN_LEVELS if util >= lv):.0f}%"
        parts.append(f"{label} window at {pct} (resets {when})")
    return "Anthropic usage warning: " + "; ".join(parts) + "."


def _claim_crossings(
    warnings: list[tuple[str, float, float, str]],
    now: float,
    alerts: list | None = None,
    slot: str = DEFAULT_SLOT,
) -> list:
    """Atomically record (window, level, reset) crossings and health-alert
    tags not yet announced by any session; return the warnings and alerts
    this caller must announce."""
    cd = _cache_dir()
    cd.mkdir(parents=True, exist_ok=True)
    state_file = cd / "anthropic-usage-warned.json"
    fresh = []
    with open(cd / "anthropic-usage-warned.lock", "w") as lf:
        fcntl.flock(lf, fcntl.LOCK_EX)
        try:
            state = _read_json(state_file) or {}
            # Forget crossings whose window has reset.
            state = {k: v for k, v in state.items() if isinstance(v, (int, float)) and v > now}
            for w in warnings:
                key, util, reset, _label = w
                level = max(lv for lv in WARN_LEVELS if util >= lv)
                tag = f"{key}:{level:.0f}:{int(reset) // 60}"
                if slot != DEFAULT_SLOT:
                    tag = f"{slot}|{tag}"
                if tag not in state:
                    state[tag] = reset
                    fresh.append(w)
            for a in alerts or []:
                atag = a[0] if slot == DEFAULT_SLOT else f"{slot}|{a[0]}"
                if atag not in state:
                    state[atag] = a[1]
                    fresh.append(a)
            tmp_fd, tmp_path = tempfile.mkstemp(dir=str(cd), suffix=".tmp")
            try:
                with os.fdopen(tmp_fd, "w") as f:
                    json.dump(state, f)
                os.replace(tmp_path, state_file)
            except BaseException:
                with contextlib.suppress(OSError):
                    os.unlink(tmp_path)
                raise
        finally:
            fcntl.flock(lf, fcntl.LOCK_UN)
    return fresh


def _active_mode(ctx: fir_ext.Context) -> str:
    try:
        return str(ctx.agent_info().get("mode") or "")
    except Exception:
        return ""


def _publish_warnings(
    ctx: fir_ext.Context,
    data: dict | None,
    mode: str,
    alerts: list | None = None,
    slot: str = DEFAULT_SLOT,
) -> None:
    now = time.time()
    warnings = _usage_warnings(data, now)
    alerts = alerts or []
    if not warnings and not alerts:
        with contextlib.suppress(Exception):
            ctx.clear_section()
        return
    # No countdown in the section: a ticking string would re-inject it on
    # every turn and bust the prompt cache. Alert texts are keyed by episode
    # tag, so their wording only changes when the episode does.
    section = [a[2] for a in alerts]
    if warnings:
        section.insert(0, _warning_text(warnings, now, countdown=False))
    with contextlib.suppress(Exception):
        ctx.set_section(" ".join(section))
    try:
        fresh = _claim_crossings(warnings, now, alerts, slot)
    except Exception:
        return
    if not fresh:
        return
    texts = []
    fresh_w = [w for w in fresh if len(w) == 4]
    if fresh_w:
        texts.append(_warning_text(fresh_w, now))
    fresh_a = [a for a in fresh if len(a) == 3]
    if mode == "acp":
        # Alerts (credential health, bursts) never trigger a turn: in ACP that
        # turn lands in whatever session runs next. The section carries them.
        fresh_a = []
    texts += [a[2] for a in fresh_a]
    if not texts:
        return
    text = " ".join(texts)
    with contextlib.suppress(Exception):
        if mode == "acp":
            ctx.send_message(
                "provider-usage-warning",
                text + " Post this warning to the user now, in one or two lines.",
                display=True,
                deliver_as="followUp",
                trigger_turn=True,
            )
        else:
            ctx.notify(text, level="warning")


# ---------------------------------------------------------------------------
# Credential health: unrefreshable token, login age
# ---------------------------------------------------------------------------


def _fmt_clock(epoch: float) -> str:
    """Local wall-clock instant; stable over time, unlike a countdown."""
    return datetime.fromtimestamp(epoch).astimezone().strftime("%a %b %d %H:%M %Z")


def _login_state_file() -> Path:
    return _cache_dir() / "anthropic-login-seen.json"


def _credential_health(cred: dict | None, now: float) -> list:
    """Alerts about the stored Anthropic OAuth credential itself."""
    if not isinstance(cred, dict) or not cred.get("refresh"):
        return []
    out = []
    expires = cred.get("expires")
    if isinstance(expires, (int, float)) and expires > 0:
        exp = expires / 1000 if expires > 1e11 else float(expires)
        if now - exp > REFRESH_GRACE_MINUTES * 60:
            out.append(
                (
                    f"refresh:{int(exp)}",
                    now + ALERT_MEMORY,
                    f"Anthropic OAuth token expired at {_fmt_clock(exp)} and was not refreshed — "
                    "the refresh token is likely failing; log in again (/login anthropic).",
                )
            )
    if LOGIN_MAX_AGE_DAYS > 0:
        extra = cred.get("extra")
        if not isinstance(extra, dict):
            extra = {}
        account = str(extra.get("accountId") or extra.get("email") or "default")
        first = _login_first_seen(account, now)
        if now - first > LOGIN_MAX_AGE_DAYS * 86400:
            out.append(
                (
                    f"login-age:{account}:{int(first)}",
                    now + ALERT_MEMORY,
                    f"Anthropic OAuth login is over {LOGIN_MAX_AGE_DAYS:g} days old — "
                    "consider logging in again (/login anthropic) before it lapses.",
                )
            )
    return out


def _login_first_seen(account: str, now: float) -> float:
    """First time this account's login was observed (the login-age clock).

    The refresh token rotates on every refresh, so it cannot mark a login;
    delete ``anthropic-login-seen.json`` after logging in again to reset it.
    """
    seen_file = _login_state_file()
    try:
        seen_file.parent.mkdir(parents=True, exist_ok=True)
        with open(seen_file.with_suffix(".lock"), "w") as lf:
            fcntl.flock(lf, fcntl.LOCK_EX)
            seen = _read_json(seen_file) or {}
            first = seen.get(account)
            if not isinstance(first, (int, float)):
                seen[account] = first = now
                seen_file.write_text(json.dumps(seen))
            return float(first)
    except OSError:
        return now


def _anthropic_credential(slot: str = DEFAULT_SLOT) -> dict | None:
    for base in _config_search_paths():
        auth = _read_json(Path(f"{base}/auth.json").expanduser())
        cred = auth.get(slot) if isinstance(auth, dict) else None
        if isinstance(cred, dict) and cred.get("access"):
            return cred
    return None


def _apply_config(cfg: dict | None) -> bool:
    """Apply ``provider-usage.json`` overrides; return False when disabled."""
    global WARN_LEVELS, REFRESH_GRACE_MINUTES, LOGIN_MAX_AGE_DAYS
    global BURST_POINTS, BURST_WINDOW_SECONDS
    if not isinstance(cfg, dict):
        return True
    levels = cfg.get("warn_levels")
    if isinstance(levels, list):
        vals = sorted(float(v) for v in levels if isinstance(v, (int, float)) and 0 < v <= 100)
        if vals:
            WARN_LEVELS = tuple(vals)
    for key, name in (
        ("refresh_grace_minutes", "REFRESH_GRACE_MINUTES"),
        ("login_max_age_days", "LOGIN_MAX_AGE_DAYS"),
        ("burst_points", "BURST_POINTS"),
        ("burst_window_seconds", "BURST_WINDOW_SECONDS"),
    ):
        v = cfg.get(key)
        if isinstance(v, (int, float)) and not isinstance(v, bool) and v >= 0:
            globals()[name] = float(v)
    return cfg.get("enabled", True) is not False


# ---------------------------------------------------------------------------
# Extension lifecycle
# ---------------------------------------------------------------------------


def _active_provider(ctx: fir_ext.Context) -> str:
    """Provider id of the model the session is on now ("" if unknown)."""
    try:
        model = ctx.agent_info().get("model") or {}
        return str(model.get("provider") or "") if isinstance(model, dict) else ""
    except Exception:
        return ""


def _refresh_once(ctx: fir_ext.Context, poe_key, mode: str) -> None:
    """One cycle: report only the budget of the account the session uses.

    Quotas are per account. A session on another provider or on another
    Anthropic account slot ("anthropic#<id>") spends nothing from this
    account, so it gets no status, no prompt section and no warnings for it.
    Checked every cycle because the model can be switched mid-session.
    """
    slot = _active_provider(ctx)
    parts = []
    token = _find_anthropic_token(slot) if _is_anthropic_slot(slot) else None
    if token:
        # Header readings only; the token gates the account, it is never sent.
        now = time.time()
        data, at = _anthropic_cached(slot)
        if s := _format_anthropic_status(data, now - at):
            parts.append(s)
        alerts = _credential_health(_anthropic_credential(slot), now)
        _publish_warnings(ctx, data, mode, alerts, slot)
    else:
        with contextlib.suppress(Exception):
            ctx.clear_section()
    if poe_key and slot == "poe" and (s := _fetch_poe_usage(poe_key)):
        parts.append(s)
    with contextlib.suppress(Exception):
        ctx.set_status(" │ ".join(parts))


def _refresh_loop(ctx: fir_ext.Context) -> None:
    """Periodically fetch usage and update the status bar."""
    mode = _active_mode(ctx)
    while not _stop_event.is_set():
        # Tokens are re-read every cycle (inside _refresh_once): fir refreshes
        # them every few hours, and the session may switch account.
        _refresh_once(ctx, _find_poe_key(), mode)
        _stop_event.wait(REFRESH_INTERVAL_SECONDS)


@fir_ext.on("session_start")
def on_session_start(params: dict, ctx: fir_ext.Context) -> None:
    global _thread
    if _thread is not None and _thread.is_alive():
        return
    if not _apply_config(fir_ext.load_config("provider-usage.json")):
        return
    _stop_event.clear()
    _thread = threading.Thread(target=_refresh_loop, args=(ctx,), daemon=True)
    _thread.start()


@fir_ext.on("session_shutdown")
def on_session_shutdown(params: dict, ctx: fir_ext.Context) -> None:
    _stop_event.set()


fir_ext.run(name="provider-usage")
