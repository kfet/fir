#!/usr/bin/env python3
# ---
# name: provider-usage
# description: Show LLM provider usage/limits in the status bar (Anthropic, Poe)
# builtin: true
# modes: tui
# events: session_start, session_shutdown
# ---
"""Periodically fetch Anthropic and/or Poe usage stats and display in the status bar.

Refreshes every 5 minutes to avoid hitting rate limits on the usage APIs.
"""

from __future__ import annotations

import contextlib
import json
import os
import threading
import urllib.request
from datetime import UTC, datetime

import fir_ext

REFRESH_INTERVAL = 300  # 5 minutes

_stop_event = threading.Event()
_thread: threading.Thread | None = None


def _read_json(path: str) -> dict | None:
    """Read a JSON file, returning None on any error."""
    try:
        with open(path) as f:
            return json.load(f)
    except Exception:
        return None


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


def _fetch_anthropic_usage(token: str) -> str | None:
    """Fetch Anthropic usage and return a short status string."""
    try:
        req = urllib.request.Request(
            "https://api.anthropic.com/api/oauth/usage",
            headers={
                "Authorization": f"Bearer {token}",
                "anthropic-beta": "oauth-2025-04-20",
                "Accept": "application/json",
            },
        )
        with urllib.request.urlopen(req, timeout=15) as resp:  # noqa: S310
            data = json.loads(resp.read())
    except Exception:
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
    return f"☁ {display}"


def _fetch_poe_usage(api_key: str) -> str | None:
    """Fetch Poe point balance and return a short status string."""
    try:
        req = urllib.request.Request(
            "https://api.poe.com/usage/current_balance",
            headers={"Authorization": f"Bearer {api_key}"},
        )
        with urllib.request.urlopen(req, timeout=15) as resp:  # noqa: S310
            data = json.loads(resp.read())
    except Exception:
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

    return f"🅿 {bal_str}pts"


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
