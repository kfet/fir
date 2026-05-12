#!/usr/bin/env python3
# ---
# name: poe-auth
# description: Poe (poe.com) OAuth provider — "Sign in with Poe"
# builtin: true
# auth_providers: poe
# ---
"""poe-auth — OAuth provider for Poe (poe.com).

Implements the "Sign in with Poe" OAuth 2.0 Authorization Code + PKCE flow
documented at https://creator.poe.com/docs/external-applications/sign-in-with-poe.

The token endpoint returns a plain ``api_key`` plus an optional
``api_key_expires_in``. There is no refresh token — when the key expires
the user must re-authenticate.

A default client ID for the "fir" OAuth app is baked in; override it via
``FIR_POE_CLIENT_ID`` (and optionally ``FIR_POE_REDIRECT_URI``) if you
register your own client at https://poe.com/api/clients.
``localhost``/``127.0.0.1`` redirect URIs do not need to be registered.
"""

from __future__ import annotations

import contextlib
import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request

import fir_ext

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_AUTHORIZE_URL = "https://poe.com/oauth/authorize"
_TOKEN_URL = "https://api.poe.com/token"  # noqa: S105
_SCOPE = "apikey:create"
# Default Client ID for the "fir" OAuth app registered at
# https://poe.com/api/clients. Override with FIR_POE_CLIENT_ID.
_DEFAULT_CLIENT_ID = "client_9962de5dfb824c669587e4069666c5ee"

# Local callback server — OS-assigned port (RFC 8252 §7.3, public clients
# may use any loopback port). The redirect URI is constructed at runtime
# from the actual bound port and travels as a per-session query param on
# the short URL.
_CALLBACK_ADDR = "127.0.0.1:0"
_CALLBACK_PATH = "/cb"

# Static OAuth parameters (everything except per-session state /
# code_challenge / redirect_uri). The short link at _SHORT_URL is
# pre-created to point at _AUTHORIZE_URL + these params, urlencoded in
# this exact order. Drift between this dict and the short link is caught
# by tests/poe_auth_test.py — if they fail, re-create the short URL.
def _static_auth_params(client_id: str) -> dict:
    return {
        "client_id": client_id,
        "response_type": "code",
        "scope": _SCOPE,
        "code_challenge_method": "S256",
    }


_SHORT_URL = "https://tinyurl.com/fir-poe"


# ---------------------------------------------------------------------------
# Config helpers
# ---------------------------------------------------------------------------


def _client_id() -> str:
    return os.environ.get("FIR_POE_CLIENT_ID", "").strip() or _DEFAULT_CLIENT_ID


def _redirect_uri_override() -> str:
    return os.environ.get("FIR_POE_REDIRECT_URI", "").strip()


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------


def _http_post_form(url: str, data: dict[str, str]) -> dict:
    """POST form-encoded data and return parsed JSON."""
    if not url.startswith(("http:", "https:")):
        raise ValueError("url must start with http or https: " + url)
    encoded = urllib.parse.urlencode(data).encode()
    req = urllib.request.Request(  # noqa: S310
        url,
        data=encoded,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        body = ""
        with contextlib.suppress(Exception):
            body = e.read().decode("utf-8", errors="replace")
        # Surface Poe's structured error fields when present.
        try:
            parsed = json.loads(body)
            err = parsed.get("error", "") or str(e.code)
            desc = parsed.get("error_description", "")
            msg = f"Poe token endpoint returned {e.code} {err}"
            if desc:
                msg += f": {desc}"
            raise RuntimeError(msg) from e
        except json.JSONDecodeError:
            raise RuntimeError(f"Poe token endpoint returned {e.code}: {body[:200]}") from e
    except urllib.error.URLError as e:
        raise RuntimeError(f"Poe token endpoint unreachable: {e.reason}") from e
    except TimeoutError as e:
        raise RuntimeError("Poe token endpoint timed out after 30s") from e


# ---------------------------------------------------------------------------
# Auth provider handlers
# ---------------------------------------------------------------------------


@fir_ext.auth_provider(
    provider_id="poe",
    name="Poe (poe.com)",
)
def login(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Run the Poe OAuth authorization code + PKCE flow."""
    client_id = _client_id()

    # 1. PKCE
    pkce = ctx.generate_pkce()

    # 2. Start callback server (unless the caller pinned a non-localhost URI).
    override = _redirect_uri_override()
    server = None
    redirect_uri = override

    if not override:
        try:
            server = ctx.start_callback_server(
                addr=_CALLBACK_ADDR, path=_CALLBACK_PATH, state=pkce["verifier"]
            )
            redirect_uri = server["redirect_uri"]
        except Exception:
            # OS-assigned port should almost never fail; fall back to manual
            # paste flow with a generic placeholder if it does.
            server = None
            redirect_uri = "http://localhost/cb"

    # 3. Authorization URL (full) + short URL (pre-shortened static prefix
    # + click-time per-session params merged by the shortener).
    static_params = _static_auth_params(client_id)
    session_params = {
        "redirect_uri": redirect_uri,
        "code_challenge": pkce["challenge"],
        "state": pkce["verifier"],
    }
    auth_params = urllib.parse.urlencode({**static_params, **session_params})
    auth_url = f"{_AUTHORIZE_URL}?{auth_params}"
    short_url = f"{_SHORT_URL}?{urllib.parse.urlencode(session_params)}"

    # 4. Open browser
    ctx.open_url(auth_url, short_url, "Approve the connection in your browser to continue.")
    ctx.progress("Waiting for OAuth callback...")

    # 5. Wait for callback (or manual paste)
    if server is not None:
        try:
            result = ctx.await_callback()
        finally:
            ctx.stop_callback_server()
    else:
        raw = ctx.prompt(
            "Paste the authorization code or full redirect URL:",
            placeholder=redirect_uri,
        )
        if raw.startswith("http"):
            parsed = urllib.parse.urlparse(raw)
            qs = urllib.parse.parse_qs(parsed.query)
            result = {
                "code": qs.get("code", [""])[0],
                "state": qs.get("state", [""])[0],
            }
        else:
            result = {"code": raw, "state": pkce["verifier"]}

    code = result.get("code", "")
    state = result.get("state", "")
    if not code:
        err = result.get("error", "")
        desc = result.get("error_description", "")
        if err:
            raise RuntimeError(f"Poe authorization failed: {err}: {desc}".rstrip(": "))
        raise RuntimeError("No authorization code received")
    if state and state != pkce["verifier"]:
        raise RuntimeError("OAuth state mismatch — possible CSRF attack")

    # 6. Exchange code for api_key
    ctx.progress("Exchanging authorization code for API key...")
    token_data = _http_post_form(
        _TOKEN_URL,
        {
            "grant_type": "authorization_code",
            "client_id": client_id,
            "code": code,
            "redirect_uri": redirect_uri,
            "code_verifier": pkce["verifier"],
        },
    )

    api_key_val = token_data.get("api_key", "")
    if not api_key_val:
        raise RuntimeError("Poe token response missing api_key")

    expires_in = token_data.get("api_key_expires_in")
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


@fir_ext.auth_api_key(provider="poe")
def api_key(params: dict, ctx: fir_ext.AuthContext) -> str:
    """Return the stored Poe API key."""
    creds = params.get("credentials", {})
    return creds.get("access", "")


# NOTE: intentionally no ``@fir_ext.auth_list_models`` handler.
#
# Poe's ``/v1/models`` endpoint is public and user-agnostic: it returns the
# same catalog for every (anonymous or authenticated) caller. The static
# model list compiled into fir via ``cmd/generate-models`` is therefore
# authoritative, and live discovery would only add latency + request volume
# on every login without surfacing anything new. If a user has access to a
# private bot they can address it by its ID directly.


fir_ext.run(name="poe-auth")
