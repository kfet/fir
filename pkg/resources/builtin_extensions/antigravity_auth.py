#!/usr/bin/env python3
# ---
# name: antigravity-auth
# description: Google Antigravity (Gemini 3, Claude, GPT-OSS) OAuth provider
# builtin: true
# auth_providers: google-antigravity
# ---
"""antigravity-auth — OAuth provider for Google Antigravity.

Implements the full OAuth flow including PKCE, token exchange, user email
lookup, and Cloud Code Assist project discovery.
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

_CLIENT_ID = base64.b64decode(
    "MTA3MTAwNjA2MDU5MS10bWhzc2luMmgyMWxjcmUyMzV2dG9sb2poNGc0MDNlcC5hcHBzLmdvb2dsZXVzZXJjb250ZW50LmNvbQ=="
).decode()

_CLIENT_SECRET = base64.b64decode("R09DU1BYLUs1OEZXUjQ4NkxkTEoxbUxCOHNYQzR6NnFEQWY=").decode()

_AUTH_URL = "https://accounts.google.com/o/oauth2/v2/auth"
_TOKEN_URL = "https://oauth2.googleapis.com/token"  # noqa: S105
_CALLBACK_ADDR = "127.0.0.1:51121"
_CALLBACK_PATH = "/oauth-callback"
_REDIRECT_URI = "http://localhost:51121/oauth-callback"
_DEFAULT_PROJECT_ID = "rising-fact-p41fc"

_SCOPES = [
    "https://www.googleapis.com/auth/cloud-platform",
    "https://www.googleapis.com/auth/userinfo.email",
    "https://www.googleapis.com/auth/userinfo.profile",
    "https://www.googleapis.com/auth/cclog",
    "https://www.googleapis.com/auth/experimentsandconfigs",
]

# NOTE: The User-Agent and X-Goog-Api-Client headers intentionally impersonate
# Google's Node.js client and VS Code Cloud Shell Editor. These values are ported
# directly from the upstream TypeScript source and are required by the Cloud Code
# Assist API to accept requests. Changing them breaks authentication.
_API_HEADERS = {
    "Content-Type": "application/json",
    "User-Agent": "google-api-nodejs-client/9.15.1",
    "X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
}

_CODE_ASSIST_ENDPOINTS = [
    "https://cloudcode-pa.googleapis.com",
    "https://daily-cloudcode-pa.sandbox.googleapis.com",
]


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


def _http_post_json(url: str, body: dict, headers: dict[str, str]) -> tuple[int, dict | None]:
    """POST JSON data and return (status_code, parsed_json_or_None)."""
    if not url.startswith(("http:", "https:")):
        raise ValueError("url must start with http or https: " + url)
    encoded = json.dumps(body).encode()
    req = urllib.request.Request(url, data=encoded)  # noqa: S310
    for k, v in headers.items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310
            return resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as e:
        body_bytes = e.read()
        try:
            return e.code, json.loads(body_bytes)
        except (json.JSONDecodeError, ValueError):
            return e.code, None
    except Exception:
        return 0, None


# ---------------------------------------------------------------------------
# User info
# ---------------------------------------------------------------------------


def _get_user_email(access_token: str) -> str:
    """Fetch the user's email from Google's userinfo endpoint."""
    try:
        req = urllib.request.Request(
            "https://www.googleapis.com/oauth2/v1/userinfo?alt=json",
        )
        req.add_header("Authorization", f"Bearer {access_token}")
        with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310
            data = json.loads(resp.read())
            return data.get("email", "")
    except Exception:
        return ""


# ---------------------------------------------------------------------------
# Project discovery
# ---------------------------------------------------------------------------


def _discover_project(access_token: str, ctx: fir_ext.AuthContext) -> str:
    """Discover an existing Google Cloud project for Antigravity."""
    ctx.progress("Checking for existing project...")

    headers = {**_API_HEADERS, "Authorization": f"Bearer {access_token}"}

    client_meta = json.dumps(
        {
            "ideType": "IDE_UNSPECIFIED",
            "platform": "PLATFORM_UNSPECIFIED",
            "pluginType": "GEMINI",
        }
    )
    headers["Client-Metadata"] = client_meta

    body = {
        "metadata": {
            "ideType": "IDE_UNSPECIFIED",
            "platform": "PLATFORM_UNSPECIFIED",
            "pluginType": "GEMINI",
        },
    }

    for endpoint in _CODE_ASSIST_ENDPOINTS:
        status, data = _http_post_json(f"{endpoint}/v1internal:loadCodeAssist", body, headers)
        if status != 200 or data is None:
            continue

        # Handle both string and object formats for cloudaicompanionProject
        project = data.get("cloudaicompanionProject")
        if isinstance(project, str) and project:
            return project
        if isinstance(project, dict):
            proj_id = project.get("id", "")
            if proj_id:
                return proj_id

    ctx.progress("Using default project...")
    return _DEFAULT_PROJECT_ID


# ---------------------------------------------------------------------------
# Auth provider handlers
# ---------------------------------------------------------------------------


@fir_ext.auth_provider(
    provider_id="google-antigravity",
    name="Antigravity (Gemini 3, Claude, GPT-OSS)",
)
def login(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Run the full Antigravity OAuth login flow."""
    # 1. Generate PKCE
    pkce = ctx.generate_pkce()

    # 2. Start callback server
    try:
        server = ctx.start_callback_server(addr=_CALLBACK_ADDR, path=_CALLBACK_PATH, state=pkce["verifier"])
        redirect_uri = server["redirect_uri"]
    except Exception:
        redirect_uri = _REDIRECT_URI
        server = None

    # 3. Build authorization URL
    auth_params = urllib.parse.urlencode(
        {
            "client_id": _CLIENT_ID,
            "response_type": "code",
            "redirect_uri": redirect_uri,
            "scope": " ".join(_SCOPES),
            "code_challenge": pkce["challenge"],
            "code_challenge_method": "S256",
            "state": pkce["verifier"],
            "access_type": "offline",
            "prompt": "consent",
        }
    )
    auth_url = f"{_AUTH_URL}?{auth_params}"

    # 4. Open browser
    ctx.open_url(auth_url, "Complete the sign-in in your browser.")
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
            placeholder=_REDIRECT_URI,
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
            "client_id": _CLIENT_ID,
            "client_secret": _CLIENT_SECRET,
            "code": code,
            "grant_type": "authorization_code",
            "redirect_uri": redirect_uri,
            "code_verifier": pkce["verifier"],
        },
    )

    refresh_token = token_data.get("refresh_token", "")
    access_token = token_data.get("access_token", "")
    expires_in = token_data.get("expires_in", 0)

    if not refresh_token:
        raise RuntimeError("No refresh token received. Please try again.")

    # 7. Get user email (optional)
    ctx.progress("Getting user info...")
    email = _get_user_email(access_token)

    # 8. Discover project
    project_id = _discover_project(access_token, ctx)

    # 9. Return credentials
    expires_at = int(time.time() * 1000) + expires_in * 1000 - 5 * 60 * 1000

    extra: dict = {"projectId": project_id}
    if email:
        extra["email"] = email

    return {
        "access": access_token,
        "refresh": refresh_token,
        "expires": expires_at,
        "extra": extra,
    }


@fir_ext.auth_refresh(provider="google-antigravity")
def refresh(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Refresh an expired Antigravity token."""
    creds = params.get("credentials", {})
    refresh_token = creds.get("refresh", "")
    extra = creds.get("extra", {})
    project_id = extra.get("projectId", "")

    if not project_id:
        raise RuntimeError("Antigravity credentials missing projectId")

    token_data = _http_post_form(
        _TOKEN_URL,
        {
            "client_id": _CLIENT_ID,
            "client_secret": _CLIENT_SECRET,
            "refresh_token": refresh_token,
            "grant_type": "refresh_token",
        },
    )

    new_refresh = token_data.get("refresh_token", "") or refresh_token
    new_access = token_data.get("access_token", "")
    expires_in = token_data.get("expires_in", 0)

    return {
        "access": new_access,
        "refresh": new_refresh,
        "expires": int(time.time() * 1000) + expires_in * 1000 - 5 * 60 * 1000,
        "extra": {"projectId": project_id},
    }


@fir_ext.auth_api_key(provider="google-antigravity")
def api_key(params: dict, ctx: fir_ext.AuthContext) -> str:
    """Extract the API key — JSON-encoded token + projectId."""
    creds = params.get("credentials", {})
    project_id = creds.get("extra", {}).get("projectId", "")
    return json.dumps({"token": creds.get("access", ""), "projectId": project_id})


@fir_ext.auth_list_models(provider="google-antigravity")
def list_models(params: dict, ctx: fir_ext.AuthContext) -> list[str] | None:
    """List available models — not supported for Antigravity (statically defined)."""
    return None


fir_ext.run(name="antigravity-auth")
