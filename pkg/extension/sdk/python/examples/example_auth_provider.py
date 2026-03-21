#!/usr/bin/env python3
# ---
# name: example-auth
# auth_providers: example-corp
# ---
"""Example auth provider extension for fir.

Demonstrates how to implement a custom OAuth provider using the fir_ext SDK.
Replace the URLs and client ID with your own OAuth application's values.
"""

import json
import time
import urllib.parse
import urllib.request

import fir_ext

CLIENT_ID = "your-client-id"
AUTHORIZE_URL = "https://sso.example-corp.com/oauth/authorize"
TOKEN_URL = "https://sso.example-corp.com/oauth/token"  # noqa: S105


@fir_ext.auth_provider(provider_id="example-corp", name="Example Corp SSO")
def login(params, ctx):
    # 1. Generate PKCE challenge
    pkce = ctx.generate_pkce()

    # 2. Start local callback server (port 0 = auto-assign)
    server = ctx.start_callback_server(addr="127.0.0.1:0", path="/callback")

    # 3. Build authorization URL
    auth_url = (
        AUTHORIZE_URL
        + "?"
        + urllib.parse.urlencode(
            {
                "client_id": CLIENT_ID,
                "response_type": "code",
                "redirect_uri": server["redirect_uri"],
                "scope": "openid profile api",
                "code_challenge": pkce["challenge"],
                "code_challenge_method": "S256",
                "state": pkce["verifier"],
            }
        )
    )

    # 4. Ask fir to open the URL in the user's browser
    ctx.open_url(auth_url, "Complete login in your browser.")

    # 5. Wait for the OAuth callback
    result = ctx.await_callback()
    ctx.stop_callback_server()

    # 6. Verify state
    if result["state"] != pkce["verifier"]:
        raise ValueError("OAuth state mismatch")

    # 7. Exchange code for tokens
    ctx.progress("Exchanging authorization code for tokens...")
    token_data = _exchange_code(
        code=result["code"],
        redirect_uri=server["redirect_uri"],
        verifier=pkce["verifier"],
    )

    return {
        "access": token_data["access_token"],
        "refresh": token_data["refresh_token"],
        "expires": int(time.time() * 1000) + token_data["expires_in"] * 1000 - 300_000,
    }


@fir_ext.auth_refresh(provider="example-corp")
def refresh(params, ctx):
    creds = params["credentials"]
    token_data = _token_request(
        {
            "grant_type": "refresh_token",
            "client_id": CLIENT_ID,
            "refresh_token": creds["refresh"],
        }
    )
    return {
        "access": token_data["access_token"],
        "refresh": token_data["refresh_token"],
        "expires": int(time.time() * 1000) + token_data["expires_in"] * 1000 - 300_000,
    }


@fir_ext.auth_list_models(provider="example-corp")
def list_models(params, ctx):
    """List models available via Example Corp SSO.

    This could call an API endpoint to discover available models dynamically.
    Return a list of model ID strings, or None if not supported.
    """
    # Example: query a models endpoint using the OAuth credentials
    # creds = params["credentials"]
    # req = urllib.request.Request(
    #     "https://api.example-corp.com/v1/models",
    #     headers={"Authorization": f"Bearer {creds['access']}"},
    # )
    # with urllib.request.urlopen(req) as resp:
    #     data = json.loads(resp.read())
    #     return [m["id"] for m in data["models"]]
    return None  # noqa: RET501


def _exchange_code(code: str, redirect_uri: str, verifier: str) -> dict:
    return _token_request(
        {
            "grant_type": "authorization_code",
            "client_id": CLIENT_ID,
            "code": code,
            "redirect_uri": redirect_uri,
            "code_verifier": verifier,
        }
    )


def _token_request(body: dict) -> dict:
    data = json.dumps(body).encode()
    req = urllib.request.Request(  # noqa: S310
        TOKEN_URL,
        data=data,
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req) as resp:  # noqa: S310
        return json.loads(resp.read())


fir_ext.run(name="example-auth")
