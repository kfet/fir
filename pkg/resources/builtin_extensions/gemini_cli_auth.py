#!/usr/bin/env python3
# ---
# name: gemini-cli-auth
# description: Google Cloud Code Assist (Gemini CLI) OAuth provider
# builtin: true
# auth_providers: google-gemini-cli
# ---
"""gemini-cli-auth — OAuth provider for Google Cloud Code Assist (Gemini CLI).

Migrated from the built-in Go implementation to demonstrate the external auth
provider extension capability. Implements the full OAuth flow including PKCE,
token exchange, user email lookup, and Cloud Code Assist project discovery
and provisioning.
"""

from __future__ import annotations

import base64
import json
import os
import time
import urllib.parse
import urllib.request

import fir_ext

# ---------------------------------------------------------------------------
# Constants (decoded at import time)
# ---------------------------------------------------------------------------

_CLIENT_ID = base64.b64decode(
    "NjgxMjU1ODA5Mzk1LW9vOGZ0Mm9wcmRybnA5ZTNhcWY2YXYzaG1kaWIxMzVqLmFwcHMuZ29vZ2xldXNlcmNvbnRlbnQuY29t"
).decode()

_CLIENT_SECRET = base64.b64decode(
    "R09DU1BYLTR1SGdNUG0tMW83U2stZ2VWNkN1NWNsWEZzeGw="
).decode()

_AUTH_URL = "https://accounts.google.com/o/oauth2/v2/auth"
_TOKEN_URL = "https://oauth2.googleapis.com/token"
_CODE_ASSIST_ENDPOINT = "https://cloudcode-pa.googleapis.com"
_CALLBACK_ADDR = "127.0.0.1:8085"
_CALLBACK_PATH = "/oauth2callback"
_REDIRECT_URI = "http://localhost:8085/oauth2callback"

_SCOPES = [
    "https://www.googleapis.com/auth/cloud-platform",
    "https://www.googleapis.com/auth/userinfo.email",
    "https://www.googleapis.com/auth/userinfo.profile",
]

_TIER_FREE = "free-tier"
_TIER_LEGACY = "legacy-tier"
_TIER_STANDARD = "standard-tier"

# NOTE: The User-Agent header intentionally impersonates Google's Node.js client.
# This value is ported directly from the upstream TypeScript source and is required
# by the Cloud Code Assist API to accept requests. Changing it breaks authentication.
_API_HEADERS = {
    "Content-Type": "application/json",
    "User-Agent": "google-api-nodejs-client/9.15.1",
    "X-Goog-Api-Client": "gl-node/22.17.0",
}


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------


def _http_post_form(url: str, data: dict[str, str]) -> dict:
    """POST form-encoded data and return parsed JSON."""
    encoded = urllib.parse.urlencode(data).encode()
    req = urllib.request.Request(url, data=encoded, headers={"Content-Type": "application/x-www-form-urlencoded"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read())


def _http_post_json(url: str, body: dict, headers: dict[str, str]) -> tuple[int, dict]:
    """POST JSON data and return (status_code, parsed_json)."""
    encoded = json.dumps(body).encode()
    req = urllib.request.Request(url, data=encoded)
    for k, v in headers.items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as e:
        body_bytes = e.read()
        try:
            return e.code, json.loads(body_bytes)
        except (json.JSONDecodeError, ValueError):
            raise RuntimeError(f"HTTP {e.code}: {body_bytes.decode(errors='replace')}") from e


def _http_get_json(url: str, headers: dict[str, str]) -> tuple[int, dict]:
    """GET and return (status_code, parsed_json)."""
    req = urllib.request.Request(url)
    for k, v in headers.items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as e:
        body_bytes = e.read()
        try:
            return e.code, json.loads(body_bytes)
        except (json.JSONDecodeError, ValueError):
            raise RuntimeError(f"HTTP {e.code}: {body_bytes.decode(errors='replace')}") from e


# ---------------------------------------------------------------------------
# User info
# ---------------------------------------------------------------------------


def _get_user_email(access_token: str) -> str:
    """Fetch the user's email from Google's userinfo endpoint."""
    try:
        _, data = _http_get_json(
            "https://www.googleapis.com/oauth2/v1/userinfo?alt=json",
            {"Authorization": f"Bearer {access_token}"},
        )
        return data.get("email", "")
    except Exception:
        return ""


# ---------------------------------------------------------------------------
# Project discovery and provisioning
# ---------------------------------------------------------------------------


def _is_vpc_sc_affected(data: dict) -> bool:
    """Check if the error indicates a VPC Service Controls violation."""
    err = data.get("error", {})
    for detail in err.get("details", []):
        if isinstance(detail, dict) and detail.get("reason") == "SECURITY_POLICY_VIOLATED":
            return True
    return False


def _get_default_tier(allowed_tiers: list[dict]) -> str:
    """Return the default tier from the allowed tiers list."""
    if not allowed_tiers:
        return _TIER_LEGACY
    for t in allowed_tiers:
        if t.get("isDefault"):
            return t.get("id", _TIER_LEGACY)
    return _TIER_LEGACY


def _poll_operation(op_name: str, headers: dict[str, str], ctx: fir_ext.AuthContext, max_attempts: int = 60) -> dict:
    """Poll a long-running operation until done."""
    for attempt in range(max_attempts):
        if attempt > 0:
            ctx.progress(f"Waiting for project provisioning (attempt {attempt + 1})...")
            time.sleep(5)
        status, data = _http_get_json(f"{_CODE_ASSIST_ENDPOINT}/v1internal/{op_name}", headers)
        if status != 200:
            raise RuntimeError(f"poll operation failed ({status}): {json.dumps(data)}")
        if data.get("done"):
            return data
    raise RuntimeError(f"operation {op_name} timed out after {max_attempts} attempts")


def _discover_project(access_token: str, ctx: fir_ext.AuthContext) -> str:
    """Discover or provision a Google Cloud project for the user."""
    env_project = os.environ.get("GOOGLE_CLOUD_PROJECT") or os.environ.get("GOOGLE_CLOUD_PROJECT_ID", "")

    headers = {**_API_HEADERS, "Authorization": f"Bearer {access_token}"}

    ctx.progress("Checking for existing Cloud Code Assist project...")

    body = {
        "cloudaicompanionProject": env_project,
        "metadata": {
            "ideType": "IDE_UNSPECIFIED",
            "platform": "PLATFORM_UNSPECIFIED",
            "pluginType": "GEMINI",
            "duetProject": env_project,
        },
    }

    status, data = _http_post_json(f"{_CODE_ASSIST_ENDPOINT}/v1internal:loadCodeAssist", body, headers)

    if status != 200:
        if _is_vpc_sc_affected(data):
            data = {"currentTier": {"id": _TIER_STANDARD}}
        else:
            raise RuntimeError(f"loadCodeAssist failed ({status}): {json.dumps(data)}")

    # User already has a current tier and project
    if "currentTier" in data:
        project = data.get("cloudaicompanionProject", "")
        if project:
            return project
        if env_project:
            return env_project
        raise RuntimeError(
            "This account requires setting the GOOGLE_CLOUD_PROJECT or "
            "GOOGLE_CLOUD_PROJECT_ID environment variable. "
            "See https://goo.gle/gemini-cli-auth-docs#workspace-gca"
        )

    # User needs onboarding
    allowed_tiers = []
    for t in data.get("allowedTiers", []):
        if isinstance(t, dict):
            allowed_tiers.append(t)
    tier_id = _get_default_tier(allowed_tiers)

    if tier_id != _TIER_FREE and not env_project:
        raise RuntimeError(
            "This account requires setting the GOOGLE_CLOUD_PROJECT or "
            "GOOGLE_CLOUD_PROJECT_ID environment variable. "
            "See https://goo.gle/gemini-cli-auth-docs#workspace-gca"
        )

    ctx.progress("Provisioning Cloud Code Assist project (this may take a moment)...")

    onboard_body: dict = {
        "tierId": tier_id,
        "metadata": {
            "ideType": "IDE_UNSPECIFIED",
            "platform": "PLATFORM_UNSPECIFIED",
            "pluginType": "GEMINI",
        },
    }
    if tier_id != _TIER_FREE and env_project:
        onboard_body["cloudaicompanionProject"] = env_project
        onboard_body["metadata"]["duetProject"] = env_project

    status, lro_data = _http_post_json(f"{_CODE_ASSIST_ENDPOINT}/v1internal:onboardUser", onboard_body, headers)
    if status != 200:
        raise RuntimeError(f"onboardUser failed ({status}): {json.dumps(lro_data)}")

    if not lro_data.get("done"):
        op_name = lro_data.get("name", "")
        if op_name:
            lro_data = _poll_operation(op_name, headers, ctx)

    # Extract project ID from response
    resp_obj = lro_data.get("response", {})
    proj = resp_obj.get("cloudaicompanionProject", {})
    if isinstance(proj, dict):
        proj_id = proj.get("id", "")
        if proj_id:
            return proj_id

    if env_project:
        return env_project

    raise RuntimeError(
        "Could not discover or provision a Google Cloud project. "
        "Try setting the GOOGLE_CLOUD_PROJECT or GOOGLE_CLOUD_PROJECT_ID environment variable."
    )


# ---------------------------------------------------------------------------
# Auth provider handlers
# ---------------------------------------------------------------------------


@fir_ext.auth_provider(id="google-gemini-cli", name="Google Cloud Code Assist (Gemini CLI)")
def login(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Run the full Gemini CLI OAuth login flow."""
    # 1. Generate PKCE
    pkce = ctx.generate_pkce()

    # 2. Start callback server
    try:
        server = ctx.start_callback_server(addr=_CALLBACK_ADDR, path=_CALLBACK_PATH)
        redirect_uri = server["redirect_uri"]
    except Exception:
        # Port unavailable — use the fixed redirect URI
        redirect_uri = _REDIRECT_URI
        server = None

    # 3. Build authorization URL
    auth_params = urllib.parse.urlencode({
        "client_id": _CLIENT_ID,
        "response_type": "code",
        "redirect_uri": redirect_uri,
        "scope": " ".join(_SCOPES),
        "code_challenge": pkce["challenge"],
        "code_challenge_method": "S256",
        "state": pkce["verifier"],
        "access_type": "offline",
        "prompt": "consent",
    })
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
        # Fallback: ask user to paste the code
        raw = ctx.prompt(
            "Paste the authorization code or full redirect URL:",
            placeholder=_REDIRECT_URI,
        )
        # Parse code and state from URL or raw code
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
    token_data = _http_post_form(_TOKEN_URL, {
        "client_id": _CLIENT_ID,
        "client_secret": _CLIENT_SECRET,
        "code": code,
        "grant_type": "authorization_code",
        "redirect_uri": redirect_uri,
        "code_verifier": pkce["verifier"],
    })

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


@fir_ext.auth_refresh(provider="google-gemini-cli")
def refresh(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Refresh an expired Gemini CLI token."""
    creds = params.get("credentials", {})
    refresh_token = creds.get("refresh", "")
    extra = creds.get("extra", {})
    project_id = extra.get("projectId", "")

    if not project_id:
        raise RuntimeError("Google Cloud credentials missing projectId")

    token_data = _http_post_form(_TOKEN_URL, {
        "client_id": _CLIENT_ID,
        "client_secret": _CLIENT_SECRET,
        "refresh_token": refresh_token,
        "grant_type": "refresh_token",
    })

    new_refresh = token_data.get("refresh_token", "") or refresh_token
    new_access = token_data.get("access_token", "")
    expires_in = token_data.get("expires_in", 0)

    return {
        "access": new_access,
        "refresh": new_refresh,
        "expires": int(time.time() * 1000) + expires_in * 1000 - 5 * 60 * 1000,
        "extra": {"projectId": project_id},
    }


@fir_ext.auth_api_key(provider="google-gemini-cli")
def api_key(params: dict, ctx: fir_ext.AuthContext) -> str:
    """Extract the API key — JSON-encoded token + projectId."""
    creds = params.get("credentials", {})
    project_id = creds.get("extra", {}).get("projectId", "")
    return json.dumps({"token": creds.get("access", ""), "projectId": project_id})


fir_ext.run(name="gemini-cli-auth")
