#!/usr/bin/env python3
# ---
# name: codex-auth
# description: OpenAI Codex (ChatGPT Plus/Pro) OAuth provider
# builtin: true
# auth_providers: openai-codex
# ---
"""codex-auth — OAuth provider for OpenAI Codex (ChatGPT Plus/Pro).

Implements the full OAuth flow including PKCE, token exchange, JWT account-ID
extraction, and model listing via the ChatGPT backend API.
"""

from __future__ import annotations

import base64
import json
import time
import urllib.error
import urllib.parse
import urllib.request

import fir_ext

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann"
_AUTHORIZE_URL = "https://auth.openai.com/oauth/authorize"
_TOKEN_URL = "https://auth.openai.com/oauth/token"  # noqa: S105
# Local callback server — OS-assigned port (RFC 8252 §7.3).
_CALLBACK_ADDR = "127.0.0.1:0"
_CALLBACK_PATH = "/cb"
_SCOPE = "openid profile email offline_access"
_JWT_CLAIM_PATH = "https://api.openai.com/auth"
_MODELS_URL = "https://chatgpt.com/backend-api/models"

# Pre-created short link. Points at _AUTHORIZE_URL + _static_auth_params()
# urlencoded in this exact order; tests/codex_auth_test.py catches drift.
_SHORT_URL = "https://tinyurl.com/fir-cdx"


def _static_auth_params() -> dict:
    """Static (non per-session) OAuth params. The short link is pre-created
    to point at _AUTHORIZE_URL + urlencode(this dict). Per-session params
    (state, code_challenge, redirect_uri) are appended at click time."""
    return {
        "response_type": "code",
        "client_id": _CLIENT_ID,
        "scope": _SCOPE,
        "code_challenge_method": "S256",
        "id_token_add_organizations": "true",
        "codex_cli_simplified_flow": "true",
        "originator": "fir",
    }


# ---------------------------------------------------------------------------
# JWT helpers
# ---------------------------------------------------------------------------


def _decode_jwt_payload(token: str) -> dict:
    """Decode the payload of a JWT without verification."""
    parts = token.split(".")
    if len(parts) != 3:
        raise ValueError(f"Invalid JWT: expected 3 parts, got {len(parts)}")
    # JWT uses base64url without padding
    payload = parts[1]
    # Add padding
    payload += "=" * (-len(payload) % 4)
    decoded = base64.urlsafe_b64decode(payload)
    return json.loads(decoded)


def _get_account_id(access_token: str) -> str:
    """Extract chatgpt_account_id from a JWT access token."""
    try:
        payload = _decode_jwt_payload(access_token)
        auth_claim = payload.get(_JWT_CLAIM_PATH, {})
        if isinstance(auth_claim, dict):
            return auth_claim.get("chatgpt_account_id", "")
    except Exception:
        return ""
    return ""


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------


def _http_post_form(url: str, data: dict[str, str]) -> dict:
    """POST form-encoded data and return parsed JSON."""
    if not url.startswith(("http:", "https:")):
        raise ValueError("url must start with http or https: " + url)
    encoded = urllib.parse.urlencode(data).encode()
    req = urllib.request.Request(  # noqa: S310
        url, data=encoded, headers={"Content-Type": "application/x-www-form-urlencoded"}
    )
    with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310
        return json.loads(resp.read())


# ---------------------------------------------------------------------------
# Auth provider handlers
# ---------------------------------------------------------------------------


@fir_ext.auth_provider(
    provider_id="openai-codex",
    name="ChatGPT Plus/Pro (Codex Subscription)",
)
def login(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Run the full OpenAI Codex OAuth login flow."""
    # 1. Generate PKCE
    pkce = ctx.generate_pkce()

    # 2. Start callback server
    try:
        server = ctx.start_callback_server(addr=_CALLBACK_ADDR, path=_CALLBACK_PATH, state=pkce["verifier"])
        redirect_uri = server["redirect_uri"]
    except Exception as e:
        raise RuntimeError(
            f"Could not start local OAuth callback server: {e}"
        ) from e

    # 3. Build authorization URL (full) + short URL.
    session_params = {
        "redirect_uri": redirect_uri,
        "code_challenge": pkce["challenge"],
        "state": pkce["verifier"],
    }
    auth_params = urllib.parse.urlencode({**_static_auth_params(), **session_params})
    auth_url = f"{_AUTHORIZE_URL}?{auth_params}"
    short_url = f"{_SHORT_URL}?{urllib.parse.urlencode(session_params)}"

    # 4. Open browser
    ctx.open_url(auth_url, short_url, "Complete the sign-in in your browser.")
    ctx.progress("Waiting for OAuth callback...")

    # 5. Wait for callback
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
            result = {"code": qs.get("code", [""])[0], "state": qs.get("state", [""])[0]}
        else:
            result = {"code": raw, "state": pkce["verifier"]}

    code = result.get("code", "")
    state = result.get("state", "")
    if not code:
        raise RuntimeError("No authorization code received")
    if state != pkce["verifier"]:
        raise RuntimeError("OAuth state mismatch — possible CSRF attack")

    # 6. Exchange code for tokens
    ctx.progress("Exchanging authorization code for tokens...")
    token_data = _http_post_form(
        _TOKEN_URL,
        {
            "grant_type": "authorization_code",
            "client_id": _CLIENT_ID,
            "code": code,
            "code_verifier": pkce["verifier"],
            "redirect_uri": redirect_uri,
        },
    )

    access_token = token_data.get("access_token", "")
    refresh_token = token_data.get("refresh_token", "")
    expires_in = token_data.get("expires_in")

    if not access_token or not refresh_token or expires_in is None:
        raise RuntimeError("Token response missing required fields")

    # 7. Extract account ID from JWT
    account_id = _get_account_id(access_token)
    if not account_id:
        raise RuntimeError("Failed to extract accountId from token")

    # 8. Return credentials
    expires_at = int(time.time() * 1000) + int(expires_in) * 1000

    return {
        "access": access_token,
        "refresh": refresh_token,
        "expires": expires_at,
        "extra": {"accountId": account_id},
    }


@fir_ext.auth_refresh(provider="openai-codex")
def refresh(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Refresh an expired OpenAI Codex token."""
    creds = params.get("credentials", {})
    refresh_token = creds.get("refresh", "")

    if not refresh_token:
        raise RuntimeError("No refresh token available")

    token_data = _http_post_form(
        _TOKEN_URL,
        {
            "grant_type": "refresh_token",
            "refresh_token": refresh_token,
            "client_id": _CLIENT_ID,
        },
    )

    access_token = token_data.get("access_token", "")
    new_refresh = token_data.get("refresh_token", "")
    expires_in = token_data.get("expires_in")

    if not access_token or not new_refresh or expires_in is None:
        raise RuntimeError("Token refresh response missing required fields")

    account_id = _get_account_id(access_token)
    if not account_id:
        raise RuntimeError("Failed to extract accountId from refreshed token")

    expires_at = int(time.time() * 1000) + int(expires_in) * 1000

    return {
        "access": access_token,
        "refresh": new_refresh,
        "expires": expires_at,
        "extra": {"accountId": account_id},
    }


@fir_ext.auth_api_key(provider="openai-codex")
def api_key(params: dict, ctx: fir_ext.AuthContext) -> str:
    """Return the access token as the API key."""
    creds = params.get("credentials", {})
    return creds.get("access", "")


@fir_ext.auth_list_models(provider="openai-codex")
def list_models(params: dict, ctx: fir_ext.AuthContext) -> list[str] | None:
    """List available models from the ChatGPT backend API."""
    creds = params.get("credentials", {})
    access_token = creds.get("access", "")
    account_id = creds.get("extra", {}).get("accountId", "")

    if not access_token:
        return None

    try:
        req = urllib.request.Request(_MODELS_URL)  # noqa: S310
        req.add_header("Authorization", f"Bearer {access_token}")
        if account_id:
            req.add_header("Chatgpt-Account-Id", account_id)

        with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310
            data = json.loads(resp.read())

        models = data.get("models", [])
        return [m["id"] for m in models if isinstance(m, dict) and m.get("id")]
    except Exception:
        return None


fir_ext.run(name="codex-auth")
