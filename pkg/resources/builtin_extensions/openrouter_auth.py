#!/usr/bin/env python3
# ---
# name: openrouter-auth
# description: OpenRouter OAuth (PKCE) provider — exchanges a browser consent for a user-owned sk-or-v1 API key
# builtin: true
# auth_providers: openrouter
# ---
"""openrouter-auth — OAuth PKCE provider for OpenRouter (openrouter.ai).

OpenRouter's flow is *PKCE-shaped* but not RFC 6749: there is no client_id,
no ``response_type``, the redirect is passed as ``callback_url`` (not
``redirect_uri``), no ``state`` is round-tripped, and the "token endpoint"
(``POST /api/v1/auth/keys``) takes a JSON body and answers ``{"key": ...}``
rather than an ``access_token`` envelope.

None of that fits fir's declarative ``declare_oauth_provider`` path
(pkg/extension/bridge_auth_generic.go builds a standard authorize URL and
parses a standard token response), so this uses the *imperative*
``fir_ext.auth_provider`` path: fir calls ``auth/login`` and this extension
drives PKCE generation, the loopback callback server, the browser open and
the exchange itself, using the bridge helpers in
pkg/extension/bridge_auth.go (``handleAuthHelperRPC``).

Flow (https://openrouter.ai/docs/use-cases/oauth-pkce):

  1. ``ctx.generate_pkce()``          → verifier + S256 challenge
  2. ``ctx.start_callback_server()``  → http://localhost:<port>/callback
  3. open ``https://openrouter.ai/auth?callback_url=…&code_challenge=…
     &code_challenge_method=S256``
  4. ``ctx.await_callback()``         → ``?code=``
  5. ``POST /api/v1/auth/keys {code, code_verifier,
     code_challenge_method:"S256"}`` → ``{"key": "sk-or-v1-…"}``

The resulting key is returned as the credential's ``access`` field, which
fir stores in ``auth.json`` under the ``openrouter`` slot. Because the
storage slot name is the same string as the ``openrouter`` *model provider*,
``AuthStorage.GetApiKey("openrouter")`` (pkg/auth/authstorage.go) finds it
before ever consulting the env var or the models.json/models.d ``apiKey``
fallback resolver — so the provider fragment needs no key.

OpenRouter keys do not expire and there is no refresh token, so ``expires``
is 0 ("never") and the refresh hook is a no-op.

Testing override (used by the local fake-server harness; unset in normal
operation):

    FIR_OPENROUTER_AUTH_URL   default https://openrouter.ai/auth
    FIR_OPENROUTER_KEYS_URL   default https://openrouter.ai/api/v1/auth/keys
"""

from __future__ import annotations

import contextlib
import json
import os
import urllib.error
import urllib.parse
import urllib.request

import fir_ext

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_PROVIDER_ID = "openrouter"
_PROVIDER_NAME = "OpenRouter (openrouter.ai)"

_DEFAULT_AUTH_URL = "https://openrouter.ai/auth"
_DEFAULT_KEYS_URL = "https://openrouter.ai/api/v1/auth/keys"

_CALLBACK_ADDR = "127.0.0.1:0"  # arbitrary free port — OpenRouter allows any
_CALLBACK_PATH = "/callback"

# Documented OpenRouter exchange failures, mapped to actionable text.
_EXCHANGE_ERRORS = {
    400: "invalid code_challenge_method (must be S256 in both steps)",
    403: "invalid code or code_verifier — make sure you are logged in to OpenRouter",
    405: "method not allowed — the exchange must be POST over HTTPS",
}


def _auth_url() -> str:
    return os.environ.get("FIR_OPENROUTER_AUTH_URL") or _DEFAULT_AUTH_URL


def _keys_url() -> str:
    return os.environ.get("FIR_OPENROUTER_KEYS_URL") or _DEFAULT_KEYS_URL


def _redact(key: str) -> str:
    """Render an API key safe to print. Never log the raw value."""
    if not key:
        return "<empty>"
    prefix = "sk-or-v1-" if key.startswith("sk-or-v1-") else key[:4]
    return f"{prefix}XXXX… ({len(key)} chars)"


# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------


def _http_post_json(url: str, payload: dict) -> dict:
    """POST a JSON body and return the parsed JSON response."""
    if not url.startswith(("http:", "https:")):
        raise ValueError("url must start with http or https: " + url)
    body = json.dumps(payload).encode()
    req = urllib.request.Request(  # noqa: S310
        url,
        data=body,
        headers={"Content-Type": "application/json", "Accept": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310
            return json.loads(resp.read())
    except ValueError:
        raise RuntimeError("OpenRouter key exchange failed: response was not valid JSON") from None
    except urllib.error.HTTPError as e:
        detail = _EXCHANGE_ERRORS.get(e.code, "")
        raw = ""
        with contextlib.suppress(Exception):  # body is best-effort context only
            raw = e.read().decode("utf-8", "replace")[:200]
        msg = f"OpenRouter key exchange failed: HTTP {e.code}"
        if detail:
            msg += f" — {detail}"
        if raw:
            msg += f" ({raw})"
        raise RuntimeError(msg) from None
    except urllib.error.URLError as e:
        raise RuntimeError(f"OpenRouter key exchange failed: {e.reason}") from None


# ---------------------------------------------------------------------------
# Auth provider
# ---------------------------------------------------------------------------


@fir_ext.auth_provider(provider_id=_PROVIDER_ID, name=_PROVIDER_NAME)
def login(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Run the OpenRouter PKCE flow and return the minted API key."""
    pkce = ctx.generate_pkce()
    verifier = pkce["verifier"]
    challenge = pkce["challenge"]

    # OpenRouter does not echo an OAuth `state` back on the redirect, so the
    # callback server is started with state="" (no enforcement — see
    # pinoauth callback_server.go: expectedState=="" skips the check).
    # CSRF protection comes from PKCE instead: a code obtained by anyone
    # else is useless without this session's code_verifier, which never
    # leaves this process. Residual gap inherent to OpenRouter's no-state
    # protocol: nothing binds the *account* to this session, so an attacker
    # who learns the auth URL could authorize their own OpenRouter account
    # against our challenge and feed us their key. Not fixable client-side.
    server = ctx.start_callback_server(addr=_CALLBACK_ADDR, path=_CALLBACK_PATH, state="")
    callback_url = server["redirect_uri"]

    query = urllib.parse.urlencode(
        {
            "callback_url": callback_url,
            "code_challenge": challenge,
            "code_challenge_method": "S256",
        }
    )
    auth_url = f"{_auth_url()}?{query}"

    try:
        ctx.open_url(
            auth_url,
            instructions="Sign in to OpenRouter and click Authorize. "
            "A user-owned API key will be created for fir.",
        )
        ctx.progress(f"Waiting for the OpenRouter callback on {callback_url} …")
        result = ctx.await_callback()
    finally:
        ctx.stop_callback_server()

    code = (result or {}).get("code", "")
    if not code:
        raise RuntimeError("No authorization code received from OpenRouter")

    ctx.progress("Exchanging authorization code for an OpenRouter API key…")
    data = _http_post_json(
        _keys_url(),
        {
            "code": code,
            "code_verifier": verifier,
            "code_challenge_method": "S256",
        },
    )

    key = ""
    if isinstance(data, dict):
        key = data.get("key", "")
    if not key:
        raise RuntimeError("OpenRouter key exchange returned no 'key' field")

    ctx.progress(f"Received OpenRouter API key {_redact(key)}")

    # OpenRouter mints a long-lived, user-owned key: no refresh token, no
    # expiry. expires=0 is fir's "never expires" convention, so GetApiKey
    # never tries to refresh it.
    return {"access": key, "refresh": "", "expires": 0}


@fir_ext.auth_refresh(_PROVIDER_ID)
def refresh(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """No-op refresh: OpenRouter keys do not expire.

    Only reachable if something calls RefreshToken explicitly, since stored
    credentials carry expires=0. Returning the credential unchanged is
    safer than erroring — it keeps a working key working.
    """
    creds = params.get("credentials") or {}
    access = creds.get("access", "")
    if not access:
        raise RuntimeError(
            "No OpenRouter API key stored — run: fir login list / fir login openrouter"
        )
    return {
        "access": access,
        "refresh": creds.get("refresh", ""),
        "expires": creds.get("expires", 0) or 0,
        "extra": creds.get("extra") or {},
    }


fir_ext.run(name="openrouter-auth")
