#!/usr/bin/env python3
# ---
# name: gemini-cli-auth
# description: Google Cloud Code Assist (Gemini CLI) OAuth provider
# builtin: true
# auth_providers: google-gemini-cli
# ---
"""gemini-cli-auth — OAuth provider for Google Cloud Code Assist (Gemini CLI).

Declarative Google OAuth flow; the only Gemini-CLI-specific work is project
discovery / provisioning (and email lookup for diagnostics) after the
standard token exchange — handled in the ``auth/post_exchange`` hook. On
refresh, the same hook receives the previous credentials and forwards the
project ID through unchanged.
"""

from __future__ import annotations

import base64
import json
import os
import time
import urllib.error
import urllib.request

import fir_ext

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_CLIENT_ID = base64.b64decode(
    "NjgxMjU1ODA5Mzk1LW9vOGZ0Mm9wcmRybnA5ZTNhcWY2YXYzaG1kaWIxMzVqLmFwcHMuZ29vZ2xldXNlcmNvbnRlbnQuY29t"
).decode()
_CLIENT_SECRET = base64.b64decode("R09DU1BYLTR1SGdNUG0tMW83U2stZ2VWNkN1NWNsWEZzeGw=").decode()

_CODE_ASSIST_ENDPOINT = "https://cloudcode-pa.googleapis.com"

# NOTE: TinyURL routes redirects to google.com domains through their
# affiliate wrapper (redirect.viglink.com → final). Functional end-to-end
# (verified to merge our click-time params with `&` and forward to
# accounts.google.com correctly), but adds one extra hop. Only affects
# the two Google providers (this one + antigravity).

_TIER_FREE = "free-tier"
_TIER_LEGACY = "legacy-tier"
_TIER_STANDARD = "standard-tier"

# NOTE: User-Agent + X-Goog-Api-Client headers intentionally impersonate
# Google's Node.js client. Required by Cloud Code Assist; ported verbatim.
_API_HEADERS = {
    "Content-Type": "application/json",
    "User-Agent": "google-api-nodejs-client/9.15.1",
    "X-Goog-Api-Client": "gl-node/22.17.0",
}


fir_ext.declare_oauth_provider(
    provider_id="google-gemini-cli",
    name="Google Cloud Code Assist (Gemini CLI)",
    client_id=_CLIENT_ID,
    client_secret=_CLIENT_SECRET,
    authorize_url="https://accounts.google.com/o/oauth2/v2/auth",
    token_url="https://oauth2.googleapis.com/token",  # noqa: S106
    scope=(
        "https://www.googleapis.com/auth/cloud-platform"
        " https://www.googleapis.com/auth/userinfo.email"
        " https://www.googleapis.com/auth/userinfo.profile"
    ),
    callback_addr="127.0.0.1:0",
    callback_path="/cb",
    auth_params_extra={"access_type": "offline", "prompt": "consent"},
    open_url_instructions="Complete the sign-in in your browser.",
    short_url_base="https://tinyurl.com/fir-gem",
)


# ---------------------------------------------------------------------------
# HTTP helpers (only what's left after dropping the auth-flow plumbing)
# ---------------------------------------------------------------------------


def _http_post_json(url: str, body: dict, headers: dict[str, str]) -> tuple[int, dict]:
    """POST JSON and return (status_code, parsed_json)."""
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
            raise RuntimeError(f"HTTP {e.code}: {body_bytes.decode(errors='replace')}") from e


def _http_get_json(url: str, headers: dict[str, str]) -> tuple[int, dict]:
    """GET and return (status_code, parsed_json)."""
    if not url.startswith(("http:", "https:")):
        raise ValueError("url must start with http or https: " + url)
    req = urllib.request.Request(url)  # noqa: S310
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
            raise RuntimeError(f"HTTP {e.code}: {body_bytes.decode(errors='replace')}") from e


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


def _poll_operation(
    op_name: str, headers: dict[str, str], ctx: fir_ext.AuthContext, max_attempts: int = 60
) -> dict:
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
    env_project = os.environ.get("GOOGLE_CLOUD_PROJECT") or os.environ.get(
        "GOOGLE_CLOUD_PROJECT_ID", ""
    )

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

    status, data = _http_post_json(
        f"{_CODE_ASSIST_ENDPOINT}/v1internal:loadCodeAssist", body, headers
    )

    if status != 200:
        if _is_vpc_sc_affected(data):
            data = {"currentTier": {"id": _TIER_STANDARD}}
        else:
            raise RuntimeError(f"loadCodeAssist failed ({status}): {json.dumps(data)}")

    if "currentTier" in data:
        project = data.get("cloudaicompanionProject", "")
        if project and isinstance(project, str):
            return project
        if env_project:
            return env_project
        raise RuntimeError(
            "This account requires setting the GOOGLE_CLOUD_PROJECT or "
            "GOOGLE_CLOUD_PROJECT_ID environment variable. "
            "See https://goo.gle/gemini-cli-auth-docs#workspace-gca"
        )

    # User needs onboarding
    allowed_tiers = [t for t in data.get("allowedTiers", []) if isinstance(t, dict)]
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

    status, lro_data = _http_post_json(
        f"{_CODE_ASSIST_ENDPOINT}/v1internal:onboardUser", onboard_body, headers
    )
    if status != 200:
        raise RuntimeError(f"onboardUser failed ({status}): {json.dumps(lro_data)}")

    if not lro_data.get("done"):
        op_name = lro_data.get("name", "")
        if op_name:
            lro_data = _poll_operation(op_name, headers, ctx)

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
# Provider-specific hooks
# ---------------------------------------------------------------------------


@fir_ext.auth_post_exchange(provider="google-gemini-cli")
def post_exchange(params: dict, ctx: fir_ext.AuthContext) -> dict:
    """Discover the user's GCP project (initial login) or carry it through (refresh).

    fir calls this hook after both the initial code exchange and each
    refresh. ``previous_credentials`` is populated only on refresh — when
    present we skip discovery and reuse the cached project ID + email.
    """
    tok = params.get("token", {})
    access_token = tok.get("access_token", "")
    refresh_token = tok.get("refresh_token", "")
    expires_at = tok.get("expires_at")

    if not access_token:
        raise RuntimeError("Token response missing access_token")
    if not refresh_token:
        # Google often omits refresh_token on a refresh response.
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
        extra = dict(previous.get("extra") or {})
    else:
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


@fir_ext.auth_api_key(provider="google-gemini-cli")
def api_key(params: dict, ctx: fir_ext.AuthContext) -> str:
    """Return a JSON-encoded ``{token, projectId}`` blob (envelope expects both)."""
    creds = params.get("credentials", {})
    project_id = creds.get("extra", {}).get("projectId", "")
    return json.dumps({"token": creds.get("access", ""), "projectId": project_id})


@fir_ext.auth_list_models(provider="google-gemini-cli")
def list_models(params: dict, ctx: fir_ext.AuthContext) -> list[str] | None:
    """List available models.

    The Cloud Code Assist API exposes retrieveUserQuota which returns quota
    buckets per model, but only for the user's tier — models outside the
    tier (e.g. gemini-2.0-flash, gemini-3.1-*) are omitted even though
    they may still work. Returning None keeps permissive mode so no valid
    models are accidentally filtered out.
    """
    return None


# ---------------------------------------------------------------------------
# Wire-protocol Api + hosted-provider registration
# ---------------------------------------------------------------------------
#
# Ships the "google-gemini-cli" wire-protocol Api (Cloud Code Assist's
# envelope around Google's GenerativeAI request format) and the
# matching hosted-provider record + model catalogue. Replaces what used
# to live in:
#
#   - pkg/ai/providers/register_gemini_cli.go (Api + DeclGoogleConfig)
#   - pkg/ai/provider_registry_builtins.go (RegisteredProvider record)
#   - cmd/generate-models/main.go + pkg/ai/models_generated.go (7 models)
#
# fir's core retains only the generic StreamDeclGoogle adapter; nothing
# in non-extension code mentions "gemini-cli" any more.

_GEMINI_CLI_ENVELOPE = (
    "{"
    '"project":"${creds.project_id}",'
    '"model":"${model.id}",'
    '"request":"$inner",'
    '"userAgent":"fir-coding-agent",'
    '"requestId":"${fn.rand_id(fir-coding-agent)}"'
    "}"
)

fir_ext.register_api(
    fir_ext.DeclGoogleApi(
        id="google-gemini-cli",
        endpoints=["https://cloudcode-pa.googleapis.com"],
        headers={
            "User-Agent": "google-cloud-sdk vscode_cloudshelleditor/0.1",
            "X-Goog-Api-Client": "gl-node/22.17.0",
            "Client-Metadata": (
                '{"ideType":"IDE_UNSPECIFIED",'
                '"platform":"PLATFORM_UNSPECIFIED",'
                '"pluginType":"GEMINI"}'
            ),
        },
        envelope=_GEMINI_CLI_ENVELOPE,
        reasoning_header_prefix="x-gemini-thinking-",
    )
)

_GEMINI_CLI_BASE_URL = "https://cloudcode-pa.googleapis.com"
_GEMINI_CLI_INPUT = ["text", "image"]


def _gemini_cli_model(
    model_id: str,
    name: str,
    *,
    reasoning: bool,
    context_window: int = 1_048_576,
    max_tokens: int,
    swe_score: float = 0.0,
) -> fir_ext.Model:
    return fir_ext.Model(
        id=model_id,
        name=name,
        base_url=_GEMINI_CLI_BASE_URL,
        reasoning=reasoning,
        input=list(_GEMINI_CLI_INPUT),
        context_window=context_window,
        max_tokens=max_tokens,
        swe_score=swe_score,
    )


fir_ext.register_provider(
    fir_ext.Provider(
        id="google-gemini-cli",
        api="google-gemini-cli",
        display_name="Google Gemini CLI",
        priority=6,
        default_model_id="gemini-3.1-pro-preview",
        oauth_provider_id="google-gemini-cli",
        env_keys=fir_ext.EnvKeys(authenticated=True),
        models=[
            _gemini_cli_model(
                "gemini-2.0-flash",
                "Gemini 2.0 Flash (Cloud Code Assist)",
                reasoning=False,
                max_tokens=8192,
                swe_score=42.1,
            ),
            _gemini_cli_model(
                "gemini-2.5-flash",
                "Gemini 2.5 Flash (Cloud Code Assist)",
                reasoning=True,
                max_tokens=65535,
                swe_score=47.3,
            ),
            _gemini_cli_model(
                "gemini-2.5-pro",
                "Gemini 2.5 Pro (Cloud Code Assist)",
                reasoning=True,
                max_tokens=65535,
                swe_score=57.6,
            ),
            _gemini_cli_model(
                "gemini-3-flash-preview",
                "Gemini 3 Flash Preview (Cloud Code Assist)",
                reasoning=True,
                max_tokens=65535,
                swe_score=76.2,
            ),
            _gemini_cli_model(
                "gemini-3-pro-preview",
                "Gemini 3 Pro Preview (Cloud Code Assist)",
                reasoning=True,
                max_tokens=65535,
                swe_score=76.2,
            ),
            _gemini_cli_model(
                "gemini-3.1-flash-light-preview",
                "Gemini 3.1 Flash Light Preview (Cloud Code Assist)",
                reasoning=True,
                max_tokens=65535,
            ),
            _gemini_cli_model(
                "gemini-3.1-pro-preview",
                "Gemini 3.1 Pro Preview (Cloud Code Assist)",
                reasoning=True,
                max_tokens=65535,
                swe_score=80.6,
            ),
        ],
    )
)


fir_ext.run(name="gemini-cli-auth")
