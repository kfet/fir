#!/usr/bin/env python3
# ---
# name: copilot-auth
# description: GitHub Copilot OAuth provider (device code flow)
# builtin: true
# auth_providers: github-copilot
# ---
"""copilot-auth — OAuth provider for GitHub Copilot.

Implements the GitHub device code OAuth flow: requests a device code, has the
user authorize in-browser, polls for the GitHub access token, then exchanges it
for a short-lived Copilot API token.
"""

from __future__ import annotations

import base64
import contextlib
import json
import re
import time
import urllib.error
import urllib.parse
import urllib.request

import fir_ext

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_CLIENT_ID = base64.b64decode("SXYxLmI1MDdhMDhjODdlY2ZlOTg=").decode()

# NOTE: The following headers intentionally impersonate the GitHub Copilot Chat
# VS Code extension. These values are ported directly from the upstream TypeScript
# source and are required by GitHub's OAuth and API endpoints to accept requests.
# Changing them breaks authentication.
_COPILOT_HEADERS = {
    "User-Agent": "GitHubCopilotChat/0.35.0",
    "Editor-Version": "vscode/1.107.0",
    "Editor-Plugin-Version": "copilot-chat/0.35.0",
    "Copilot-Integration-Id": "vscode-chat",
}

_PROXY_EP_RE = re.compile(r"proxy-ep=([^;]+)")


# ---------------------------------------------------------------------------
# URL helpers
# ---------------------------------------------------------------------------


def _github_urls(domain: str) -> tuple[str, str, str]:
    """Return (device_code_url, access_token_url, copilot_token_url)."""
    return (
        f"https://{domain}/login/device/code",
        f"https://{domain}/login/oauth/access_token",
        f"https://api.{domain}/copilot_internal/v2/token",
    )


def _base_url_from_token(token: str) -> str:
    """Extract the API base URL from a Copilot token's proxy-ep field."""
    m = _PROXY_EP_RE.search(token)
    if not m:
        return ""
    proxy_host = m.group(1)
    api_host = proxy_host.replace("proxy.", "api.", 1)
    return "https://" + api_host


def _copilot_base_url(token: str, enterprise_domain: str) -> str:
    if token:
        u = _base_url_from_token(token)
        if u:
            return u
    if enterprise_domain:
        return "https://copilot-api." + enterprise_domain
    return "https://api.individual.githubcopilot.com"


def _normalize_domain(raw: str) -> str:
    """Normalize a user-supplied domain string to a hostname."""
    trimmed = raw.strip()
    if not trimmed:
        return ""
    if "://" not in trimmed:
        trimmed = "https://" + trimmed
    parsed = urllib.parse.urlparse(trimmed)
    return parsed.hostname or ""


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------


def _http_post_form(url: str, data: dict[str, str], headers: dict[str, str] | None = None) -> dict:
    """POST form-encoded data and return parsed JSON."""
    encoded = urllib.parse.urlencode(data).encode()
    req = urllib.request.Request(url, data=encoded)  # noqa: S310
    req.add_header("Accept", "application/json")
    req.add_header("Content-Type", "application/x-www-form-urlencoded")
    if headers:
        for k, v in headers.items():
            req.add_header(k, v)
    with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310
        return json.loads(resp.read())


def _http_get_json(url: str, headers: dict[str, str]) -> tuple[int, dict]:
    """GET and return (status_code, parsed_json)."""
    req = urllib.request.Request(url)  # noqa: S310
    req.add_header("Accept", "application/json")
    for k, v in headers.items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310
            return resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as e:
        body = e.read()
        try:
            return e.code, json.loads(body)
        except (json.JSONDecodeError, ValueError):
            return e.code, {}


def _http_post_json(url: str, body: dict, headers: dict[str, str]) -> tuple[int, dict | None]:
    """POST JSON and return (status_code, parsed_json_or_None)."""
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
# Device code flow
# ---------------------------------------------------------------------------


def _start_device_flow(domain: str) -> dict:
    """Start the GitHub device code flow. Returns device_code, user_code, etc."""
    device_code_url, _, _ = _github_urls(domain)
    data = _http_post_form(
        device_code_url,
        {"client_id": _CLIENT_ID, "scope": "read:user"},
        {"User-Agent": "GitHubCopilotChat/0.35.0"},
    )
    if "device_code" not in data:
        raise RuntimeError(f"Device code request failed: {json.dumps(data)}")
    return data


def _poll_for_access_token(
    domain: str, device_code: str, interval: int, expires_in: int, ctx: fir_ext.AuthContext
) -> str:
    """Poll GitHub until user authorizes, returning the access token."""
    _, access_token_url, _ = _github_urls(domain)
    deadline = time.time() + expires_in
    wait = max(1, interval) * 1.2

    while time.time() < deadline:
        time.sleep(wait)

        try:
            data = _http_post_form(
                access_token_url,
                {
                    "client_id": _CLIENT_ID,
                    "device_code": device_code,
                    "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
                },
                {"User-Agent": "GitHubCopilotChat/0.35.0"},
            )
        except Exception as e:
            raise RuntimeError(f"Access token request failed: {e}") from e

        # Success
        token = data.get("access_token", "")
        if token:
            return token

        error = data.get("error", "")
        if error == "authorization_pending":
            continue
        if error == "slow_down":
            server_interval = data.get("interval")
            if (
                server_interval
                and isinstance(server_interval, (int, float))
                and server_interval > 0
            ):
                wait = float(server_interval)
            else:
                wait += 5
            wait *= 1.4
        else:
            desc = data.get("error_description", error)
            raise RuntimeError(f"Device flow failed: {desc}")

    raise RuntimeError("Device flow timed out")


# ---------------------------------------------------------------------------
# Copilot token exchange
# ---------------------------------------------------------------------------


def _get_copilot_token(github_token: str, enterprise_domain: str) -> dict:
    """Exchange a GitHub access token for a Copilot API token."""
    domain = enterprise_domain if enterprise_domain else "github.com"
    _, _, copilot_token_url = _github_urls(domain)

    headers = {
        "Authorization": f"Bearer {github_token}",
        **_COPILOT_HEADERS,
    }

    status, data = _http_get_json(copilot_token_url, headers)
    if status != 200 or not data:
        raise RuntimeError(f"Copilot token request failed ({status}): {json.dumps(data)}")

    token = data.get("token", "")
    expires_at = data.get("expires_at", 0)
    if not token:
        raise RuntimeError("Invalid Copilot token response: missing token")

    return {"token": token, "expires_at": expires_at}


# ---------------------------------------------------------------------------
# Enable models
# ---------------------------------------------------------------------------


def _enable_copilot_models(copilot_token: str, enterprise_domain: str) -> None:
    """Best-effort enable all known GitHub Copilot models."""
    base_url = _copilot_base_url(copilot_token, enterprise_domain)
    # Known model IDs to enable — matches the Go implementation
    model_ids = [
        "claude-3.5-sonnet",
        "claude-3.7-sonnet",
        "claude-3.7-sonnet-thought",
        "claude-sonnet-4",
        "gemini-2.0-flash-001",
        "gpt-4o",
        "gpt-4.1",
        "o1",
        "o3-mini",
        "o4-mini",
    ]
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {copilot_token}",
        "openai-intent": "chat-policy",
        "x-interaction-type": "chat-policy",
        **_COPILOT_HEADERS,
    }
    for mid in model_ids:
        with contextlib.suppress(Exception):
            _http_post_json(f"{base_url}/models/{mid}/policy", {"state": "enabled"}, headers)


# ---------------------------------------------------------------------------
# Auth provider handlers
# ---------------------------------------------------------------------------


@fir_ext.auth_provider(
    provider_id="github-copilot",
    name="GitHub Copilot",
)
def login(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Run the GitHub Copilot device code OAuth flow."""
    # 1. Ask for enterprise domain (blank = github.com)
    raw_domain = ctx.prompt(
        "GitHub Enterprise URL/domain",
        placeholder="company.ghe.com, leave blank for github.com",
        allow_empty=True,
    )
    enterprise_domain = _normalize_domain(raw_domain)
    domain = enterprise_domain if enterprise_domain else "github.com"

    # 2. Start device code flow
    ctx.progress("Starting device code flow...")
    device = _start_device_flow(domain)

    user_code = device.get("user_code", "")
    verification_uri = device.get("verification_uri", "")
    device_code = device.get("device_code", "")
    interval = device.get("interval", 5)
    expires_in = device.get("expires_in", 900)

    # 3. Open browser for user to authorize
    ctx.open_url(verification_uri, f"Enter code: {user_code}")
    ctx.progress("Waiting for authorization...")

    # 4. Poll for GitHub access token
    github_token = _poll_for_access_token(domain, device_code, interval, expires_in, ctx)

    # 5. Exchange for Copilot token
    ctx.progress("Exchanging for Copilot token...")
    copilot = _get_copilot_token(github_token, enterprise_domain)

    # 6. Enable models (best-effort)
    ctx.progress("Enabling models...")
    _enable_copilot_models(copilot["token"], enterprise_domain)

    # 7. Return credentials
    expires_at = int(copilot["expires_at"]) * 1000 - 5 * 60 * 1000

    extra: dict = {}
    if enterprise_domain:
        extra["enterpriseUrl"] = enterprise_domain

    return {
        "access": copilot["token"],
        "refresh": github_token,
        "expires": expires_at,
        "extra": extra,
    }


@fir_ext.auth_refresh(provider="github-copilot")
def refresh(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Refresh an expired Copilot token using the stored GitHub access token."""
    creds = params.get("credentials", {})
    github_token = creds.get("refresh", "")
    extra = creds.get("extra", {})
    enterprise_domain = extra.get("enterpriseUrl", "")

    if not github_token:
        raise RuntimeError("Missing GitHub access token (refresh)")

    copilot = _get_copilot_token(github_token, enterprise_domain)
    expires_at = int(copilot["expires_at"]) * 1000 - 5 * 60 * 1000

    new_extra: dict = {}
    if enterprise_domain:
        new_extra["enterpriseUrl"] = enterprise_domain

    return {
        "access": copilot["token"],
        "refresh": github_token,
        "expires": expires_at,
        "extra": new_extra,
    }


@fir_ext.auth_api_key(provider="github-copilot")
def api_key(params: dict, ctx: fir_ext.AuthContext) -> str:
    """Return the Copilot API token as the API key."""
    creds = params.get("credentials", {})
    return creds.get("access", "")


@fir_ext.auth_modify_models(provider="github-copilot")
def modify_models(params: dict, ctx: fir_ext.AuthContext) -> list[dict] | None:
    """Set BaseURL and required headers on all github-copilot models."""
    creds = params.get("credentials", {})
    models = params.get("models", [])
    token = creds.get("access", "")
    enterprise_domain = creds.get("extra", {}).get("enterpriseUrl", "")
    domain = _normalize_domain(enterprise_domain) if enterprise_domain else ""
    base_url = _copilot_base_url(token, domain)

    result = []
    for m in models:
        if isinstance(m, dict) and m.get("provider") == "github-copilot":
            m = dict(m)
            m["baseUrl"] = base_url
            m["headers"] = dict(_COPILOT_HEADERS)
        result.append(m)
    return result


@fir_ext.auth_list_models(provider="github-copilot")
def list_models(params: dict, ctx: fir_ext.AuthContext) -> list[str] | None:
    """List available models — not supported (statically defined)."""
    return None


fir_ext.run(name="copilot-auth")
