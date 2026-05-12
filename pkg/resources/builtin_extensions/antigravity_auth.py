#!/usr/bin/env python3
# ---
# name: antigravity-auth
# description: Google Antigravity (Gemini 3, Claude, GPT-OSS) OAuth provider
# builtin: true
# auth_providers: google-antigravity
# ---
"""antigravity-auth — OAuth provider for Google Antigravity.

Declarative Google OAuth flow; the only Antigravity-specific work is
project discovery (and email lookup for diagnostics) after the standard
token exchange — handled in the ``auth/post_exchange`` hook. On refresh,
the same hook receives the previous credentials and forwards the project
ID through unchanged.
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

_CLIENT_ID = base64.b64decode(
    "MTA3MTAwNjA2MDU5MS10bWhzc2luMmgyMWxjcmUyMzV2dG9sb2poNGc0MDNlcC5hcHBzLmdvb2dsZXVzZXJjb250ZW50LmNvbQ=="
).decode()
_CLIENT_SECRET = base64.b64decode("R09DU1BYLUs1OEZXUjQ4NkxkTEoxbUxCOHNYQzR6NnFEQWY=").decode()

_DEFAULT_PROJECT_ID = "rising-fact-p41fc"

# NOTE: User-Agent + X-Goog-Api-Client headers intentionally impersonate
# Google's Node.js client / VS Code Cloud Shell Editor. Required by the
# Cloud Code Assist API; ported verbatim from upstream.
_API_HEADERS = {
    "Content-Type": "application/json",
    "User-Agent": "google-api-nodejs-client/9.15.1",
    "X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
}
_CODE_ASSIST_ENDPOINTS = [
    "https://cloudcode-pa.googleapis.com",
    "https://daily-cloudcode-pa.sandbox.googleapis.com",
]


fir_ext.declare_oauth_provider(
    provider_id="google-antigravity",
    name="Antigravity (Gemini 3, Claude, GPT-OSS)",
    client_id=_CLIENT_ID,
    client_secret=_CLIENT_SECRET,
    authorize_url="https://accounts.google.com/o/oauth2/v2/auth",
    token_url="https://oauth2.googleapis.com/token",  # noqa: S106
    scope=(
        "https://www.googleapis.com/auth/cloud-platform"
        " https://www.googleapis.com/auth/userinfo.email"
        " https://www.googleapis.com/auth/userinfo.profile"
        " https://www.googleapis.com/auth/cclog"
        " https://www.googleapis.com/auth/experimentsandconfigs"
    ),
    callback_addr="127.0.0.1:0",
    callback_path="/cb",
    auth_params_extra={"access_type": "offline", "prompt": "consent"},
    open_url_instructions="Complete the sign-in in your browser.",
    short_url_base="https://tinyurl.com/fir-agr",
)


# ---------------------------------------------------------------------------
# Helpers (only what's left after dropping the auth-flow plumbing)
# ---------------------------------------------------------------------------


def _http_post_json(url: str, body: dict, headers: dict[str, str]) -> tuple[int, dict | None]:
    """POST JSON and return (status_code, parsed_json_or_None)."""
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
# Antigravity-specific hooks
# ---------------------------------------------------------------------------


@fir_ext.auth_post_exchange(provider="google-antigravity")
def post_exchange(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Discover the user's GCP project (initial login) or carry it through (refresh).

    fir calls this hook after both the initial code exchange and each
    refresh. ``previous_credentials`` is populated only on refresh — when
    present we skip discovery and reuse the cached project ID.
    """
    tok = params.get("token", {})
    access_token = tok.get("access_token", "")
    refresh_token = tok.get("refresh_token", "")
    expires_at = tok.get("expires_at")

    if not access_token:
        raise RuntimeError("Token response missing access_token")
    if not refresh_token:
        # Google sometimes omits refresh_token on a refresh response.
        # Fall back to the previous one when refreshing.
        refresh_token = (params.get("previous_credentials") or {}).get("refresh", "")
    if not refresh_token:
        raise RuntimeError("No refresh token received. Please try again.")

    # 5-minute safety buffer
    if expires_at is not None:
        expires_at = int(expires_at) - 5 * 60 * 1000
    else:
        expires_at = int(time.time() * 1000) + 60 * 60 * 1000

    previous = params.get("previous_credentials") or {}
    if previous:
        # Refresh path: preserve projectId (and email) from existing creds.
        extra = dict(previous.get("extra") or {})
    else:
        # Initial login: do user-info + project discovery.
        ctx.progress("Getting user info...")
        email = _get_user_email(access_token)
        project_id = _discover_project(access_token, ctx)
        extra: dict = {"projectId": project_id}
        if email:
            extra["email"] = email

    return {
        "access": access_token,
        "refresh": refresh_token,
        "expires": expires_at,
        "extra": extra,
    }


@fir_ext.auth_api_key(provider="google-antigravity")
def api_key(params: dict, ctx: fir_ext.AuthContext) -> str:
    """Return a JSON-encoded ``{token, projectId}`` blob.

    The Cloud Code Assist Api wire-protocol envelope wants the project ID
    alongside the bearer token, so we ship both as a single key.
    """
    creds = params.get("credentials", {})
    project_id = creds.get("extra", {}).get("projectId", "")
    return json.dumps({"token": creds.get("access", ""), "projectId": project_id})


@fir_ext.auth_list_models(provider="google-antigravity")
def list_models(params: dict, ctx: fir_ext.AuthContext) -> list[str] | None:
    """List available models.

    The Cloud Code Assist API's retrieveUserQuota returns quota buckets
    per base model ID, but antigravity uses different model IDs (e.g.
    gemini-3-pro-high, claude-sonnet-4-5) that don't appear in quota.
    Returning None keeps permissive mode so no valid models are filtered out.
    """
    return None


# ---------------------------------------------------------------------------
# Wire-protocol Api + hosted-provider registration
# ---------------------------------------------------------------------------
#
# Ships the "google-antigravity" wire-protocol Api (Cloud Code Assist's
# sandbox endpoints with antigravity-specific User-Agent, system
# instructions, and Claude+thinking interleaved-thinking-2025-05-14
# header), the matching hosted-provider record, and the 12-model catalogue.
# Replaces what used to live in:
#
#   - pkg/ai/providers/register_antigravity.go (DeclGoogleConfig)
#   - pkg/ai/providers/register_gemini_cli.go (Api wire registration)
#   - pkg/ai/provider_registry_builtins.go (RegisteredProvider record)
#   - cmd/generate-models/main.go + pkg/ai/models_generated.go (12 models)
#
# Migration also corrects a latent inconsistency: the antigravity Api wire
# was previously registered but never actively used at runtime — antigravity
# model entries set api="google-gemini-cli", routing through geminiCLIConfig
# so antigravityConfig (system instructions + anthropic-beta header for
# Claude+thinking) never applied. After this migration antigravity models
# correctly use api="google-antigravity" and pick up that config.

_ANTIGRAVITY_SYSTEM_INSTRUCTION = (
    "You are Antigravity, a powerful agentic AI coding assistant designed by "
    "the Google Deepmind team working on Advanced Agentic Coding."
    "You are pair programming with a USER to solve their coding task. The task "
    "may require creating a new codebase, modifying or debugging an existing "
    "codebase, or simply answering a question."
    "**Absolute paths only**"
    "**Proactiveness**"
)

_ANTIGRAVITY_ENVELOPE = (
    "{"
    '"project":"${creds.project_id}",'
    '"model":"${model.id}",'
    '"request":"$inner",'
    '"requestType":"agent",'
    '"userAgent":"antigravity",'
    '"requestId":"${fn.rand_id(antigravity)}"'
    "}"
)

fir_ext.register_api(
    fir_ext.DeclGoogleApi(
        id="google-antigravity",
        endpoints=[
            "https://daily-cloudcode-pa.sandbox.googleapis.com",
            "https://autopush-cloudcode-pa.sandbox.googleapis.com",
            "https://cloudcode-pa.googleapis.com",
        ],
        headers={"User-Agent": "antigravity/1.21.9 ${os}/${arch}"},
        conditional_headers=[
            fir_ext.DeclGoogleConditional(
                when_model_id_prefix="claude-",
                when_requires_reasoning=True,
                set={"anthropic-beta": "interleaved-thinking-2025-05-14"},
            ),
        ],
        envelope=_ANTIGRAVITY_ENVELOPE,
        system_instruction_prefix=[
            _ANTIGRAVITY_SYSTEM_INSTRUCTION,
            "Please ignore following [ignore]" + _ANTIGRAVITY_SYSTEM_INSTRUCTION + "[/ignore]",
        ],
        system_instruction_role="user",
        reasoning_header_prefix="x-gemini-thinking-",
    )
)

_ANTIGRAVITY_BASE_URL = "https://daily-cloudcode-pa.sandbox.googleapis.com"


def _antigravity_model(
    model_id: str,
    name: str,
    *,
    reasoning: bool,
    context_window: int,
    max_tokens: int,
    cost_input: float = 0.0,
    cost_output: float = 0.0,
    cost_cache_read: float = 0.0,
    cost_cache_write: float = 0.0,
    inputs: tuple = ("text", "image"),
    swe_score: float = 0.0,
) -> fir_ext.Model:
    return fir_ext.Model(
        id=model_id,
        name=name,
        base_url=_ANTIGRAVITY_BASE_URL,
        reasoning=reasoning,
        input=list(inputs),
        context_window=context_window,
        max_tokens=max_tokens,
        cost_input=cost_input,
        cost_output=cost_output,
        cost_cache_read=cost_cache_read,
        cost_cache_write=cost_cache_write,
        swe_score=swe_score,
    )


fir_ext.register_provider(
    fir_ext.Provider(
        id="google-antigravity",
        api="google-antigravity",
        display_name="Google Antigravity",
        priority=7,
        default_model_id="gemini-3.1-pro-high",
        oauth_provider_id="google-antigravity",
        env_keys=fir_ext.EnvKeys(authenticated=True),
        models=[
            _antigravity_model(
                "claude-opus-4-5-thinking", "Claude Opus 4.5 Thinking (Antigravity)",
                reasoning=True, context_window=200_000, max_tokens=64_000,
                cost_input=5, cost_output=25, cost_cache_read=0.5, cost_cache_write=6.25,
                swe_score=80.9,
            ),
            _antigravity_model(
                "claude-opus-4-6-thinking", "Claude Opus 4.6 Thinking (Antigravity)",
                reasoning=True, context_window=200_000, max_tokens=128_000,
                cost_input=5, cost_output=25, cost_cache_read=0.5, cost_cache_write=6.25,
                swe_score=80.8,
            ),
            _antigravity_model(
                "claude-sonnet-4-5", "Claude Sonnet 4.5 (Antigravity)",
                reasoning=False, context_window=200_000, max_tokens=64_000,
                cost_input=3, cost_output=15, cost_cache_read=0.3, cost_cache_write=3.75,
                swe_score=77.2,
            ),
            _antigravity_model(
                "claude-sonnet-4-5-thinking", "Claude Sonnet 4.5 Thinking (Antigravity)",
                reasoning=True, context_window=200_000, max_tokens=64_000,
                cost_input=3, cost_output=15, cost_cache_read=0.3, cost_cache_write=3.75,
                swe_score=77.2,
            ),
            _antigravity_model(
                "claude-sonnet-4-6", "Claude Sonnet 4.6 (Antigravity)",
                reasoning=True, context_window=200_000, max_tokens=64_000,
                cost_input=3, cost_output=15, cost_cache_read=0.3, cost_cache_write=3.75,
                swe_score=79.6,
            ),
            _antigravity_model(
                "gemini-3-flash", "Gemini 3 Flash (Antigravity)",
                reasoning=True, context_window=1_048_576, max_tokens=65535,
                cost_input=0.5, cost_output=3, cost_cache_read=0.5,
                swe_score=76.2,
            ),
            _antigravity_model(
                "gemini-3-pro-high", "Gemini 3 Pro High (Antigravity)",
                reasoning=True, context_window=1_048_576, max_tokens=65535,
                cost_input=2, cost_output=12, cost_cache_read=0.2, cost_cache_write=2.375,
                swe_score=76.2,
            ),
            _antigravity_model(
                "gemini-3-pro-low", "Gemini 3 Pro Low (Antigravity)",
                reasoning=True, context_window=1_048_576, max_tokens=65535,
                cost_input=2, cost_output=12, cost_cache_read=0.2, cost_cache_write=2.375,
                swe_score=76.2,
            ),
            _antigravity_model(
                "gemini-3.1-flash-light", "Gemini 3.1 Flash Light (Antigravity)",
                reasoning=True, context_window=1_048_576, max_tokens=65535,
                cost_input=0.1, cost_output=0.4, cost_cache_read=0.01,
            ),
            _antigravity_model(
                "gemini-3.1-pro-high", "Gemini 3.1 Pro High (Antigravity)",
                reasoning=True, context_window=1_048_576, max_tokens=65535,
                cost_input=2, cost_output=12, cost_cache_read=0.2, cost_cache_write=2.375,
                swe_score=80.6,
            ),
            _antigravity_model(
                "gemini-3.1-pro-low", "Gemini 3.1 Pro Low (Antigravity)",
                reasoning=True, context_window=1_048_576, max_tokens=65535,
                cost_input=2, cost_output=12, cost_cache_read=0.2, cost_cache_write=2.375,
                swe_score=80.6,
            ),
            _antigravity_model(
                "gpt-oss-120b-medium", "GPT-OSS 120B Medium (Antigravity)",
                reasoning=False, context_window=131072, max_tokens=32768,
                cost_input=0.09, cost_output=0.36,
                inputs=("text",),
            ),
        ],
    )
)


fir_ext.run(name="antigravity-auth")
