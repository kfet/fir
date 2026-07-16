#!/usr/bin/env python3
# ---
# name: poe-auth
# description: Poe (poe.com) OAuth provider — "Sign in with Poe"
# builtin: true
# auth_providers: poe
# ---
"""poe-auth — OAuth provider for Poe (poe.com).

Declarative provider implementing the "Sign in with Poe" OAuth 2.0
Authorization Code + PKCE flow documented at
https://creator.poe.com/docs/external-applications/sign-in-with-poe.

The token endpoint returns a plain ``api_key`` plus an optional
``api_key_expires_in``. There is no refresh token — when the key expires
the user must re-authenticate (the registered refresh handler raises a
descriptive error).

A default client ID for the "fir" OAuth app is baked in; override it via
``FIR_POE_CLIENT_ID`` (and optionally ``FIR_POE_REDIRECT_URI`` for a
custom-registered client whose redirect is not loopback).
"""

from __future__ import annotations

import json
import os
import threading
import time
import urllib.error
import urllib.request

import fir_ext

# ---------------------------------------------------------------------------
# Constants & overridable config (read once at extension load).
# ---------------------------------------------------------------------------

# Default Client ID for the "fir" OAuth app at https://poe.com/api/clients.
_DEFAULT_CLIENT_ID = "client_9962de5dfb824c669587e4069666c5ee"
_CLIENT_ID = os.environ.get("FIR_POE_CLIENT_ID", "").strip() or _DEFAULT_CLIENT_ID

# When the user registers their own Poe OAuth client with a non-loopback
# redirect URI, FIR_POE_REDIRECT_URI lets them route the redirect there;
# fir then drives a manual-paste fallback flow instead of binding a local
# callback server.
_REDIRECT_URI_OVERRIDE = os.environ.get("FIR_POE_REDIRECT_URI", "").strip()


# ---------------------------------------------------------------------------
# Provider declaration
# ---------------------------------------------------------------------------

if _REDIRECT_URI_OVERRIDE:
    fir_ext.declare_oauth_provider(
        provider_id="poe",
        name="Poe (poe.com)",
        client_id=_CLIENT_ID,
        authorize_url="https://poe.com/oauth/authorize",
        token_url="https://api.poe.com/token",  # noqa: S106
        scope="apikey:create",
        disable_callback_server=True,
        manual_redirect_uri=_REDIRECT_URI_OVERRIDE,
        open_url_instructions="Approve the connection in your browser to continue.",
    )
else:
    fir_ext.declare_oauth_provider(
        provider_id="poe",
        name="Poe (poe.com)",
        client_id=_CLIENT_ID,
        authorize_url="https://poe.com/oauth/authorize",
        token_url="https://api.poe.com/token",  # noqa: S106
        scope="apikey:create",
        callback_addr="127.0.0.1:0",
        callback_path="/cb",
        manual_redirect_uri="",
        open_url_instructions="Approve the connection in your browser to continue.",
        short_url_base="https://tinyurl.com/fir-poe",
    )


# ---------------------------------------------------------------------------
# Poe-specific hooks
# ---------------------------------------------------------------------------


@fir_ext.auth_post_exchange(provider="poe")
def post_exchange(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Map Poe's non-standard token response to fir credentials.

    Poe returns ``{"api_key": "...", "api_key_expires_in": ...}`` instead
    of ``{"access_token", "refresh_token", "expires_in"}``. Pluck the key
    out of ``token.raw`` and apply a 60-second safety margin.
    """
    tok = params.get("token", {})
    raw = tok.get("raw", {}) or {}
    api_key_val = raw.get("api_key", "") or tok.get("access_token", "")
    if not api_key_val:
        raise RuntimeError("Poe token response missing api_key")

    expires_in = raw.get("api_key_expires_in")
    if expires_in is None:
        expires_at = 0  # 0 means "never expires" in fir's auth storage.
    else:
        # 60-second safety margin so we don't race with server-side expiry.
        expires_at = int(time.time() * 1000) + (int(expires_in) - 60) * 1000

    return {
        "access": api_key_val,
        "refresh": "",
        "expires": expires_at,
    }


@fir_ext.auth_refresh(provider="poe")
def refresh(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Poe does not issue refresh tokens — direct the user to re-login."""
    raise RuntimeError(
        "Poe API key has expired. Run `fir --login poe` to obtain a new key."
    )


# NOTE: intentionally no ``@fir_ext.auth_list_models`` handler.
#
# Poe's ``/v1/models`` endpoint is public and user-agnostic: it returns the
# same catalog for every (anonymous or authenticated) caller. The static
# model list compiled into fir via ``cmd/generate-models`` is therefore
# authoritative, and live discovery would only add latency + request volume
# on every login without surfacing anything new. If a user has access to a
# private bot they can address it by its ID directly.


# ---------------------------------------------------------------------------
# On-demand endpoint/callability resolution
# ---------------------------------------------------------------------------
#
# generate-models keeps Poe models whose catalog ``supported_endpoints`` list
# was EMPTY permissively (as ``openai-completions`` against api.poe.com/v1),
# because it must never make a billable call to tell a genuinely-callable bot
# from a not-yet-enabled pre-release listing (which 404s ``not_found_error``).
#
# We resolve that ambiguity here, lazily, only when such a model is actually
# selected for inference — via the host's provider-neutral
# ``auth/resolve_endpoint`` hook. The first selection probes the model once
# with a tiny POST to /v1/chat/completions and memoises the verdict to a small
# JSON file under fir's config dir; every later selection is a memo hit with
# zero network calls. A definitive ``not_found_error`` 404 records
# ``callable=false`` so fir refuses the selection with a clean message instead
# of failing mid-stream. Models with explicit endpoints are never probed.

_MODELS_URL = "https://api.poe.com/v1/models"
_CHAT_URL = "https://api.poe.com/v1/chat/completions"
_MEMO_FILENAME = "poe-endpoints.json"

_memo_lock = threading.Lock()
_memo_cache: dict | None = None  # model_id -> {"callable": bool, "base_url"?, "api"?}

_empty_lock = threading.Lock()
_empty_ids_cache: set[str] | None = None  # model IDs whose supported_endpoints was empty


def _memo_path() -> str | None:
    return fir_ext.config_path(_MEMO_FILENAME)


def _load_memo() -> dict:
    """Load (and cache) the on-disk memo. Missing/corrupt file -> empty dict."""
    global _memo_cache
    with _memo_lock:
        if _memo_cache is not None:
            return _memo_cache
        _memo_cache = {}
        path = _memo_path()
        if path and os.path.exists(path):
            try:
                with open(path, encoding="utf-8") as fh:
                    data = json.load(fh)
                if isinstance(data, dict):
                    _memo_cache = data
            except (OSError, ValueError):
                _memo_cache = {}
        return _memo_cache


def _record_memo(model_id: str, entry: dict) -> None:
    """Persist a single model's verdict, updating the in-memory cache too."""
    memo = _load_memo()
    with _memo_lock:
        memo[model_id] = entry
        path = _memo_path()
        if not path:
            return
        try:
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w", encoding="utf-8") as fh:
                json.dump(memo, fh)
        except OSError:
            pass


def _fetch_empty_endpoint_ids() -> set[str]:
    """Fetch Poe's free, unauthenticated catalog and return the set of model
    IDs whose ``supported_endpoints`` list was empty (the ambiguous set)."""
    ids: set[str] = set()
    try:
        req = urllib.request.Request(_MODELS_URL, headers={"User-Agent": "fir"})  # noqa: S310
        with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310
            data = json.load(resp)
    except (urllib.error.URLError, OSError, ValueError):
        return ids
    for m in data.get("data", []) or []:
        if not m.get("supported_endpoints"):
            mid = m.get("id")
            if mid:
                ids.add(mid)
    return ids


def _empty_endpoint_ids() -> set[str]:
    """Cached accessor for the ambiguous (empty-supported_endpoints) ID set."""
    global _empty_ids_cache
    with _empty_lock:
        if _empty_ids_cache is None:
            _empty_ids_cache = _fetch_empty_endpoint_ids()
        return _empty_ids_cache


def _probe_model(model_id: str, api_key: str) -> dict:
    """Probe a Poe model once with a tiny /v1/chat/completions ping.

    Returns a memo entry dict. A definitive ``not_found_error`` 404 ->
    ``{"callable": False}``. Any success or non-404 outcome (or a transient
    failure) -> ``{"callable": True}`` so a flaky probe never wrongly prunes a
    working model.
    """
    body = json.dumps(
        {
            "model": model_id,
            "messages": [{"role": "user", "content": "ping"}],
            "max_tokens": 1,
        }
    ).encode("utf-8")
    req = urllib.request.Request(  # noqa: S310
        _CHAT_URL,
        data=body,
        method="POST",
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:  # noqa: S310
            resp.read()
        return {"callable": True}
    except urllib.error.HTTPError as exc:
        if exc.code == 404:
            try:
                raw = exc.read().decode("utf-8", "ignore")
            except OSError:
                raw = ""
            if "not_found_error" in raw:
                return {"callable": False}
        return {"callable": True}
    except (urllib.error.URLError, OSError):
        return {"callable": True}


@fir_ext.auth_resolve_endpoint(provider="poe")
def resolve_endpoint(params: dict, ctx: fir_ext.AuthContext) -> dict | None:
    """Resolve a Poe model's endpoint/callability on selection for inference.

    Memo hit -> return the memoised verdict with zero network calls. Miss on a
    model whose catalog ``supported_endpoints`` was empty -> probe once, persist,
    and return the verdict. Models with explicit endpoints are left untouched.
    """
    model_id = params.get("model_id", "")
    if not model_id:
        return None

    memo = _load_memo()
    if model_id in memo:
        return dict(memo[model_id])

    # Only empty-supported_endpoints models are ambiguous; never probe others.
    if model_id not in _empty_endpoint_ids():
        return None

    entry = _probe_model(model_id, params.get("api_key", ""))
    _record_memo(model_id, entry)
    return dict(entry)


fir_ext.run(name="poe-auth")

