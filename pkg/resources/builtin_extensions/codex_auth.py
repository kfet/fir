#!/usr/bin/env python3
# ---
# name: codex-auth
# description: OpenAI Codex (ChatGPT Plus/Pro) OAuth provider
# builtin: true
# auth_providers: openai-codex
# ---
"""codex-auth — OAuth provider for OpenAI Codex (ChatGPT Plus/Pro).

Declarative provider: fir drives the standard authorization-code+PKCE flow.
Codex-specific bits: extracting ``chatgpt_account_id`` from the access-token
JWT (post-exchange enrichment) and the live model list via the ChatGPT
backend API.
"""

from __future__ import annotations

import base64
import json
import urllib.error
import urllib.request

import fir_ext

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann"
_JWT_CLAIM_PATH = "https://api.openai.com/auth"
_MODELS_URL = "https://chatgpt.com/backend-api/models"


fir_ext.declare_oauth_provider(
    provider_id="openai-codex",
    name="ChatGPT Plus/Pro (Codex Subscription)",
    client_id=_CLIENT_ID,
    authorize_url="https://auth.openai.com/oauth/authorize",
    token_url="https://auth.openai.com/oauth/token",  # noqa: S106
    scope="openid profile email offline_access",
    # Codex's OAuth client only whitelists this exact loopback redirect
    # (fixed port 1455 + /auth/callback). Empirically verified
    # 2026-05-14: wildcard port AND different path are both rejected.
    callback_addr="127.0.0.1:1455",
    callback_path="/auth/callback",
    auth_params_extra={
        "id_token_add_organizations": "true",
        "codex_cli_simplified_flow": "true",
        "originator": "fir",
    },
    open_url_instructions="Complete the sign-in in your browser.",
    short_url_base="https://tinyurl.com/fir-cdx",
)


# ---------------------------------------------------------------------------
# JWT helpers
# ---------------------------------------------------------------------------


def _decode_jwt_payload(token: str) -> dict:
    """Decode the payload of a JWT without verification."""
    parts = token.split(".")
    if len(parts) != 3:
        raise ValueError(f"Invalid JWT: expected 3 parts, got {len(parts)}")
    payload = parts[1]
    payload += "=" * (-len(payload) % 4)  # JWT base64url has no padding
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
# Codex-specific hooks
# ---------------------------------------------------------------------------


@fir_ext.auth_post_exchange(provider="openai-codex")
def post_exchange(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Pull chatgpt_account_id out of the access-token JWT after exchange/refresh."""
    tok = params.get("token", {})
    access_token = tok.get("access_token", "")
    refresh_token = tok.get("refresh_token", "")
    expires_at = tok.get("expires_at")

    if not access_token or not refresh_token or expires_at is None:
        raise RuntimeError("Token response missing required fields")

    account_id = _get_account_id(access_token)
    if not account_id:
        raise RuntimeError("Failed to extract accountId from token")

    return {
        "access": access_token,
        "refresh": refresh_token,
        "expires": int(expires_at),
        "extra": {"accountId": account_id},
    }


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
