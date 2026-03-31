#!/usr/bin/env python3
# ---
# name: anthropic-auth
# description: Anthropic (Claude Pro/Max) OAuth provider
# builtin: true
# auth_providers: anthropic
# ---
"""anthropic-auth — OAuth provider for Anthropic (Claude Pro/Max).

Implements the full OAuth authorization code flow with PKCE, token exchange,
and refresh via the Anthropic platform API.
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

_CLIENT_ID = base64.b64decode("OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl").decode()
_AUTHORIZE_URL = "https://claude.ai/oauth/authorize"
_TOKEN_URL = "https://platform.claude.com/v1/oauth/token"  # noqa: S105
_CALLBACK_ADDR = "127.0.0.1:53692"
_CALLBACK_PATH = "/callback"
_MANUAL_REDIRECT_URI = "https://platform.claude.com/oauth/code/callback"
_SCOPES = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"


_CLAUDE_CODE_VERSION = "2.1.75"


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------


def _http_post_json(url: str, data: dict) -> dict:
    """POST JSON data and return parsed JSON response."""
    if not url.startswith(("http:", "https:")):
        raise ValueError("url must start with http or https: " + url)
    encoded = json.dumps(data).encode()
    req = urllib.request.Request(  # noqa: S310
        url,
        data=encoded,
        headers={
            "Content-Type": "application/json",
            "User-Agent": f"claude-cli/{_CLAUDE_CODE_VERSION} (external, cli)",
        },
    )
    with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310
        return json.loads(resp.read())


# ---------------------------------------------------------------------------
# Token helpers
# ---------------------------------------------------------------------------


def _exchange_token(body: dict) -> dict:
    """Send a token request to the Anthropic OAuth endpoint and return credentials."""
    token_data = _http_post_json(_TOKEN_URL, body)

    access_token = token_data.get("access_token", "")
    refresh_token = token_data.get("refresh_token", "")
    expires_in = token_data.get("expires_in")

    if not access_token or not refresh_token or expires_in is None:
        raise RuntimeError("Token response missing required fields")

    # Calculate expiry: current time + expires_in - 5 min buffer
    expires_at = int(time.time() * 1000) + int(expires_in) * 1000 - 5 * 60 * 1000

    return {
        "access": access_token,
        "refresh": refresh_token,
        "expires": expires_at,
    }


# ---------------------------------------------------------------------------
# Auth provider handlers
# ---------------------------------------------------------------------------


@fir_ext.auth_provider(
    provider_id="anthropic",
    name="Anthropic (Claude Pro/Max)",
)
def login(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Run the Anthropic OAuth authorization code + PKCE flow."""
    # 1. Generate PKCE
    pkce = ctx.generate_pkce()

    # 2. Start callback server
    try:
        server = ctx.start_callback_server(addr=_CALLBACK_ADDR, path=_CALLBACK_PATH, state=pkce["verifier"])
        redirect_uri = server["redirect_uri"]
    except Exception:
        redirect_uri = _MANUAL_REDIRECT_URI
        server = None

    # 3. Build authorization URL
    auth_params = urllib.parse.urlencode(
        {
            "code": "true",
            "client_id": _CLIENT_ID,
            "response_type": "code",
            "redirect_uri": redirect_uri,
            "scope": _SCOPES,
            "code_challenge": pkce["challenge"],
            "code_challenge_method": "S256",
            "state": pkce["verifier"],
        }
    )
    auth_url = f"{_AUTHORIZE_URL}?{auth_params}"

    # 4. Open browser
    ctx.open_url(
        auth_url,
        "Complete login in your browser. If the browser is on another machine, paste the final redirect URL here.",
    )
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
            placeholder=_MANUAL_REDIRECT_URI,
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
    if not state:
        state = pkce["verifier"]
    if state != pkce["verifier"]:
        raise RuntimeError("OAuth state mismatch")

    # 6. Exchange code for tokens
    ctx.progress("Exchanging authorization code for tokens...")
    return _exchange_token(
        {
            "grant_type": "authorization_code",
            "client_id": _CLIENT_ID,
            "code": code,
            "state": state,
            "redirect_uri": redirect_uri,
            "code_verifier": pkce["verifier"],
        }
    )


@fir_ext.auth_refresh(provider="anthropic")
def refresh(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Refresh an expired Anthropic OAuth token."""
    creds = params.get("credentials", {})
    refresh_token = creds.get("refresh", "")

    if not refresh_token:
        raise RuntimeError("No refresh token available")

    return _exchange_token(
        {
            "grant_type": "refresh_token",
            "client_id": _CLIENT_ID,
            "refresh_token": refresh_token,
        }
    )


@fir_ext.auth_api_key(provider="anthropic")
def api_key(params: dict, ctx: fir_ext.AuthContext) -> str:
    """Return the access token as the API key."""
    creds = params.get("credentials", {})
    return creds.get("access", "")


@fir_ext.auth_modify_models(provider="anthropic")
def modify_models(params: dict, ctx: fir_ext.AuthContext) -> list[dict] | None:
    """Set OAuth-specific headers on all anthropic models."""
    creds = params.get("credentials", {})
    models = params.get("models", [])
    access_token = creds.get("access", "")

    if not access_token:
        return None

    oauth_headers = {
        "authorization": f"Bearer {access_token}",
        "user-agent": f"claude-cli/{_CLAUDE_CODE_VERSION} (external, cli)",
        "x-app": "cli",
        "x-anthropic-oauth-beta-prefix": "claude-code-20250219,oauth-2025-04-20",
        "x-anthropic-oauth-system-prefix": "You are Claude Code, Anthropic's official CLI for Claude.",
    }

    result = []
    for m in models:
        if isinstance(m, dict) and m.get("provider") == "anthropic":
            m = dict(m)
            existing = m.get("headers") or {}
            m["headers"] = {**existing, **oauth_headers}
        result.append(m)
    return result


@fir_ext.auth_list_models(provider="anthropic")
def list_models(params: dict, ctx: fir_ext.AuthContext) -> list[str] | None:
    """List available models — not supported (statically defined)."""
    return None


fir_ext.run(name="anthropic-auth")
