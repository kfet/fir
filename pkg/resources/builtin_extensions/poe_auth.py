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

import os
import time

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


fir_ext.run(name="poe-auth")
