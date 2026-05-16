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
import time

import fir_ext

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_CLIENT_ID = base64.b64decode("OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl").decode()
_CLAUDE_CODE_VERSION = "2.1.112"
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
    return {
        "access": tok.get("access_token", ""),
        "refresh": tok.get("refresh_token", ""),
        "expires": expires_at,
    }


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
