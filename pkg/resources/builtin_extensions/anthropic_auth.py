#!/usr/bin/env python3
# ---
# name: anthropic-auth
# description: Anthropic (Claude Pro/Max) OAuth provider
# builtin: true
# auth_providers: anthropic
# ---
"""anthropic-auth — OAuth provider for Anthropic (Claude Pro/Max).

Declarative provider: fir drives the standard authorization-code+PKCE flow
using the static config below. The extension only carries Anthropic-specific
bits — JSON-encoded token body, Claude-Code User-Agent, OAuth-mode header
injection on outbound model requests, and the fir→Claude-Code tool-name map.
"""

from __future__ import annotations

import base64
import json
import time
import urllib.error
import urllib.request

import fir_ext

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_CLIENT_ID = base64.b64decode("OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl").decode()
_CLAUDE_CODE_VERSION = "2.1.257"
_USER_AGENT = f"claude-cli/{_CLAUDE_CODE_VERSION} (external, cli)"


# ---------------------------------------------------------------------------
# Provider declaration — fir drives the entire flow.
# ---------------------------------------------------------------------------

fir_ext.declare_oauth_provider(
    provider_id="anthropic",
    name="Anthropic (Claude Pro/Max)",
    client_id=_CLIENT_ID,
    authorize_url="https://claude.ai/oauth/authorize",
    token_url="https://platform.claude.com/v1/oauth/token",  # noqa: S106
    scope=(
        "org:create_api_key user:profile user:inference user:sessions:claude_code "
        "user:mcp_servers user:file_upload"
    ),
    # Anthropic's OAuth client accepts any loopback port (RFC 8252 §7.3
    # wildcard-port works) but the path is whitelisted exactly — only
    # "/callback" is accepted; "/cb" yields "Redirect URI ... is not
    # supported by client". Empirically verified 2026-05-14.
    callback_addr="127.0.0.1:0",
    callback_path="/callback",
    manual_redirect_uri="https://platform.claude.com/oauth/code/callback",
    auth_params_extra={"code": "true"},
    token_body_json=True,
    # Anthropic's token endpoint requires the OAuth `state` value to be
    # echoed back in the token-request body — a non-standard quirk of
    # the Claude-Code OAuth client (RFC 6749 §4.1.3 does not list state
    # as a token-request parameter; the endpoint returns
    # `invalid_request` without it). The "{state}" placeholder is
    # substituted by fir with the per-session state value (currently
    # pkce.Verifier) before the token request is sent.
    token_body_extra={"state": "{state}"},
    token_headers={"User-Agent": _USER_AGENT},
    open_url_instructions=(
        "Complete login in your browser. If the browser is on another machine, "
        "paste the final redirect URL here."
    ),
    short_url_base="https://tinyurl.com/fir-ant",
)


# ---------------------------------------------------------------------------
# Provider-specific hooks
# ---------------------------------------------------------------------------


def _slugify(value: str) -> str:
    """Turn a free-form string (email, org name) into a slot-key-safe token.

    Keeps it human-readable: lowercases, keeps alphanumerics plus ``@ . _``,
    turns every other run of characters into a single ``-``, and trims/collapses
    dashes. ``#`` (fir's provider/account slot separator) is therefore always
    stripped.
    """
    out = []
    for ch in value.strip().lower():
        if ch.isalnum() or ch in "@._":
            out.append(ch)
        else:
            out.append("-")
    slug = "".join(out)
    while "--" in slug:
        slug = slug.replace("--", "-")
    return slug.strip("-")


@fir_ext.auth_post_exchange(provider="anthropic")
def post_exchange(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Apply Anthropic's 5-minute refresh-window safety buffer.

    The token endpoint returns a normal access/refresh/expires_at triple;
    we just shorten the effective expiry by 5 minutes so fir refreshes
    early instead of mid-request.
    """
    tok = params.get("token", {})
    expires_at = tok.get("expires_at")
    if expires_at is not None:
        # 5-minute safety buffer
        expires_at = int(expires_at) - 5 * 60 * 1000
    else:
        # No expiry returned — fall back to a short default.
        expires_at = int(time.time() * 1000) + 60 * 60 * 1000
    result = {
        "access": tok.get("access_token", ""),
        "refresh": tok.get("refresh_token", ""),
        "expires": expires_at,
    }

    # Capture the account identity so fir can label this account and keep
    # multiple Anthropic logins side by side. The token endpoint echoes an
    # `account` object (uuid + email, sometimes a display name) and an
    # `organization` object (uuid + name) in the raw response; scope already
    # includes `user:profile`.
    #
    # Identity must distinguish the SAME user across DIFFERENT organizations:
    # one Anthropic user can belong to several orgs and each OAuth login is
    # org-scoped, so personal/work orgs must coexist rather than overwrite each
    # other.
    #
    # We deliberately build a *human-readable* account id from email + org name
    # (not the opaque uuids) because the account id becomes the storage slot key
    # (`anthropic#<accountId>`) which is surfaced verbatim in the model selector
    # badge, `fir login list`, and `fir logout`. A uuid there means nothing to a
    # human. uuids are only used as a last-resort fallback when no email/org
    # name is available.
    raw = tok.get("raw") or {}
    account = raw.get("account") or {}
    # The organization object may live at the top level or nested under the
    # account, depending on the token-endpoint response shape; check both.
    organization = raw.get("organization") or account.get("organization") or {}
    email = account.get("email_address") or account.get("email") or ""
    # A human display name, if the profile provides one.
    name = (
        account.get("display_name")
        or account.get("full_name")
        or account.get("name")
        or raw.get("name")
        or ""
    )
    user_uuid = account.get("uuid") or ""
    org_uuid = (
        organization.get("uuid")
        or organization.get("organization_uuid")
        or raw.get("organization_uuid")
        or ""
    )
    org_name = (
        organization.get("name")
        or organization.get("organization_name")
        or raw.get("organization_name")
        or ""
    )

    # Account id (storage slot): readable email + org slug, uuid only as a
    # last resort. '#' is reserved (provider/account separator) so it is
    # stripped by _slugify.
    base_id = _slugify(email) or user_uuid
    org_id = _slugify(org_name) or org_uuid
    account_id = f"{base_id}-{org_id}" if base_id and org_id else base_id or org_id

    # Display label: prefer a real name, then email; always append the org.
    who = name or email
    if who and org_name:
        label = f"{who} ({org_name})"
    elif org_name:
        label = org_name
    else:
        label = who

    extra = {}
    if account_id:
        extra["accountId"] = account_id
    if label:
        extra["label"] = label
    if name:
        extra["name"] = name
    if email:
        extra["email"] = email
    if org_uuid:
        extra["orgId"] = org_uuid
    if org_name:
        extra["orgName"] = org_name
    if extra:
        result["extra"] = extra
    return result


@fir_ext.auth_modify_models(provider="anthropic")
def modify_models(params: dict, ctx: fir_ext.AuthContext) -> list[dict] | None:
    """Set OAuth-specific headers on all anthropic models."""
    creds = params.get("credentials", {})
    models = params.get("models", [])
    if not creds.get("access"):
        return None

    oauth_headers = {
        "user-agent": _USER_AGENT,
        "x-app": "cli",
        "x-anthropic-oauth-beta-prefix": "claude-code-20250219,oauth-2025-04-20",
        "x-anthropic-oauth-system-prefix": (
            "You are Claude Code, Anthropic's official CLI for Claude."
        ),
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
    """Live-list Anthropic models for OAuth (Claude Pro/Max) credentials.

    fir built-in Go lister authenticates with an ``x-api-key`` header,
    which Anthropic rejects for OAuth access tokens (HTTP 401
    ``x-api-key header is required``). OAuth tokens must use
    ``Authorization: Bearer`` plus the ``oauth-2025-04-20`` beta header.

    Pages through ``GET /v1/models`` and returns the live model IDs so fir
    can hide catalogue entries the account can no longer reach. Returns
    ``None`` on any failure so fir falls back permissively to the static
    built-in catalogue rather than masking everything.
    """
    creds = params.get("credentials") or {}
    access = creds.get("access")
    if not access:
        return None

    headers = {
        "Authorization": f"Bearer {access}",
        "anthropic-version": "2023-06-01",
        "anthropic-beta": "oauth-2025-04-20",
        "User-Agent": _USER_AGENT,
    }

    ids: list[str] = []
    after_id = ""
    try:
        for _ in range(20):  # pagination safety cap
            url = "https://api.anthropic.com/v1/models?limit=100"
            if after_id:
                url += f"&after_id={after_id}"
            req = urllib.request.Request(url, headers=headers)  # noqa: S310
            with urllib.request.urlopen(req, timeout=20) as resp:  # noqa: S310
                data = json.loads(resp.read().decode())
            page = data.get("data") or []
            for m in page:
                mid = m.get("id")
                if mid:
                    ids.append(mid)
            if not data.get("has_more") or not page:
                break
            after_id = page[-1].get("id") or ""
            if not after_id:
                break
    except (urllib.error.URLError, OSError, ValueError):
        return None
    return ids or None


# ---------------------------------------------------------------------------
# Tool-name mapping (fir → Claude Code canonical names)
# ---------------------------------------------------------------------------
#
# When a session is authenticated with OAuth (Claude Pro/Max), Anthropic's
# backend expects the Claude Code canonical tool names. We translate fir's
# tool names to the CC names on the way out to the LLM and back again on
# incoming tool calls. The map is shipped with this extension so all the
# Claude-Code-specific knowledge lives next to the OAuth flow.
#
# Current CC tool surface (as advertised by Claude Code itself): Agent,
# Bash, BashOutput, Edit, Glob, Grep, KillShell, Read, ScheduleWakeup,
# Skill, ToolSearch, Write.
# Every fir tool currently has an entry below — the map is complete.
fir_ext.register_tool_name_map(
    {
        "read": "Read",
        "write": "Write",
        "edit": "Edit",
        "bash": "Bash",
        "grep": "Grep",
        "find": "Glob",
        "bash_output": "BashOutput",
        "bash_kill": "KillShell",
    }
)


fir_ext.run(name="anthropic-auth")
