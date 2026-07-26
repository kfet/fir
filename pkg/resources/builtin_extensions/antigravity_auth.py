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
import os
import socket
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

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
    """List the Antigravity models that are actually live for *this* account.

    Antigravity has no public list-models endpoint. The Cloud Code Assist
    ``retrieveUserQuota`` returns quota buckets per *base* model ID, not the
    Antigravity-suffixed IDs (``-high`` / ``-low`` / ``-lite`` / ``-thinking``)
    that the wire actually accepts — so it can't be used for filtering.

    Strategy: opportunistically probe ``/v1internal:streamGenerateContent``
    with a 1-token request against each ID in the **static catalogue**
    (the list registered just below this function). Bucket each ID as live
    (200 / 400 / 429 / 500) or missing (404). Return only the live IDs so
    fir's model picker hides stale catalogue entries automatically.

    fir already caches this result for 1 hour per provider (see
    ``pkg/models/live_models.go`` ``liveCacheTTL``) and runs it in a
    background goroutine, so the cost is amortised across many invocations.
    Discovery of *new* IDs (ones not in the catalogue at all) is out of
    scope here — that's what the ``antigravity-models`` skill does, with
    a hand-curated candidate sweep.

    Returns ``None`` on any unexpected failure so fir falls back to the
    permissive built-in catalogue rather than masking everything.
    """
    creds = params.get("credentials") or {}
    access = creds.get("access") or ""
    project = (creds.get("extra") or {}).get("projectId") or creds.get("project_id") or ""
    if not access or not project:
        return None

    # Opt-out for users who'd rather not spend tokens on a diagnostic probe.
    if os.environ.get("FIR_ANTIGRAVITY_DISABLE_PROBE"):
        return None

    # Pull the canonical ID list straight from the provider we registered.
    catalogue_ids = _antigravity_catalogue_ids()
    if not catalogue_ids:
        return None

    # Pre-flight against a known-good ID. If auth/endpoint is broken, every
    # probe will return 401/403/0 — we'd waste 12 more requests just to
    # discover the same thing. Bail early.
    if not _preflight_ok(access, project, catalogue_ids):
        return None

    ctx.progress(f"Probing {len(catalogue_ids)} Antigravity models...")
    live, missing = _probe_models(access, project, catalogue_ids)
    if not live and missing:
        # Every probe came back 404 — overwhelmingly likely an auth/endpoint
        # issue rather than every catalogue entry being stale. Stay permissive.
        return None
    if missing:
        ctx.progress(f"Antigravity: {len(live)} live, {len(missing)} stale (hidden)")
    return sorted(live)


# Pre-flight uses the IDs most likely to be live across all tiers, in order
# of preference. First one that returns a "model exists" status code is
# enough proof that auth and endpoint are working.
_PREFLIGHT_PROBE_IDS = ("gemini-3-flash", "gemini-3-pro-low", "gemini-3.1-pro-high")


def _preflight_ok(access: str, project: str, catalogue_ids: list[str]) -> bool:
    """Return True iff at least one preflight ID returns an existence code.

    On 401/403/network-down we get neither EXISTS nor MISSING — every probe
    is effectively useless, so we shouldn't fire the rest of the catalogue.
    """
    # Try our well-known stable IDs first, but fall back to anything in the
    # catalogue if none of them is registered (paranoid future-proofing).
    candidates = [m for m in _PREFLIGHT_PROBE_IDS if m in catalogue_ids] or catalogue_ids[:1]
    for cid in candidates:
        code = _probe_one(cid, access, project)
        if code in _PROBE_EXISTS_STATUSES or code in _PROBE_MISSING_STATUSES:
            return True
    return False


def _antigravity_catalogue_ids() -> list[str]:
    """Return the IDs of the Antigravity provider we registered below."""
    for prov in fir_ext._providers:
        if prov.get("id") == "google-antigravity":
            return [m["id"] for m in prov.get("models", []) if m.get("id")]
    return []


# Status codes that prove the model exists on the endpoint, even when the
# specific request was rejected for unrelated reasons. 404 alone means the
# model is genuinely not in the catalogue. Anything else (network failure,
# 401/403) is "don't know" — we treat as "exists" to stay permissive.
_PROBE_EXISTS_STATUSES = frozenset({200, 400, 429, 500})
_PROBE_MISSING_STATUSES = frozenset({404})

# Endpoint used for the probe. The provider's wire-protocol Api lists three
# endpoints with failover, but the probe only needs one — production
# Cloud Code Assist accepts personal-tier OAuth tokens and returns 200/404
# cleanly. We pick the same one the live extension prefers first.
_PROBE_ENDPOINT = "https://cloudcode-pa.googleapis.com"
_PROBE_TIMEOUT_S = 8.0
_PROBE_WORKERS = 8


def _probe_one(model_id: str, access: str, project: str) -> int:
    """Send a 1-token request; return the HTTP status (0 on network error)."""
    inner = {
        "contents": [{"role": "user", "parts": [{"text": "."}]}],
        "generationConfig": {
            "maxOutputTokens": 1,
            "thinkingConfig": {"thinkingBudget": 0},
        },
    }
    body = {
        "project": project,
        "model": model_id,
        "request": inner,
        "requestType": "agent",
        "userAgent": "antigravity",
        "requestId": f"firprobe-{int(time.time() * 1000)}",
    }
    headers = {
        "Authorization": f"Bearer {access}",
        "Content-Type": "application/json",
        "Accept": "text/event-stream",
        "User-Agent": "antigravity/1.107.0 darwin/arm64",
        "X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
    }
    url = f"{_PROBE_ENDPOINT}/v1internal:streamGenerateContent?alt=sse"
    req = urllib.request.Request(url, data=json.dumps(body).encode(), headers=headers)  # noqa: S310
    try:
        with urllib.request.urlopen(req, timeout=_PROBE_TIMEOUT_S) as resp:  # noqa: S310
            # Drain a tiny bit so the server doesn't keep streaming
            # the full response on our behalf.
            resp.read(64)
            return resp.status
    except urllib.error.HTTPError as e:
        return e.code
    except (urllib.error.URLError, socket.timeout, OSError):
        # Narrowed catch: network/DNS/timeout returns 0 ("unknown"), but
        # programming bugs (TypeError, KeyError, …) propagate so they
        # surface in extension logs instead of being silently classified
        # as a network blip.
        return 0


def _probe_models(access: str, project: str, ids: list[str]) -> tuple[list[str], list[str]]:
    """Probe ``ids`` in parallel; return (live, missing).

    Anything that isn't an unambiguous 404 (genuinely missing) counts as
    live — including network errors and auth failures — so a transient
    glitch can't accidentally empty the user's model menu. The pre-flight
    in :func:`list_models` already eliminates the all-broken case.
    """
    live: list[str] = []
    missing: list[str] = []
    with ThreadPoolExecutor(max_workers=_PROBE_WORKERS) as ex:
        for model_id, code in zip(ids, ex.map(lambda m: _probe_one(m, access, project), ids)):
            if code in _PROBE_MISSING_STATUSES:
                missing.append(model_id)
            else:
                live.append(model_id)
    return live, missing


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
        headers={"User-Agent": "antigravity/1.107.0 ${os}/${arch}"},
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
                "claude-opus-4-5-thinking",
                "Claude Opus 4.5 Thinking (Antigravity)",
                reasoning=True,
                context_window=200_000,
                max_tokens=64_000,
                cost_input=5,
                cost_output=25,
                cost_cache_read=0.5,
                cost_cache_write=6.25,
                swe_score=80.9,
            ),
            _antigravity_model(
                "claude-opus-4-6-thinking",
                "Claude Opus 4.6 Thinking (Antigravity)",
                reasoning=True,
                context_window=200_000,
                max_tokens=128_000,
                cost_input=5,
                cost_output=25,
                cost_cache_read=0.5,
                cost_cache_write=6.25,
                swe_score=80.8,
            ),
            _antigravity_model(
                "claude-sonnet-4-5",
                "Claude Sonnet 4.5 (Antigravity)",
                reasoning=False,
                context_window=200_000,
                max_tokens=64_000,
                cost_input=3,
                cost_output=15,
                cost_cache_read=0.3,
                cost_cache_write=3.75,
                swe_score=77.2,
            ),
            _antigravity_model(
                "claude-sonnet-4-5-thinking",
                "Claude Sonnet 4.5 Thinking (Antigravity)",
                reasoning=True,
                context_window=200_000,
                max_tokens=64_000,
                cost_input=3,
                cost_output=15,
                cost_cache_read=0.3,
                cost_cache_write=3.75,
                swe_score=77.2,
            ),
            _antigravity_model(
                "claude-sonnet-4-6",
                "Claude Sonnet 4.6 (Antigravity)",
                reasoning=True,
                context_window=200_000,
                max_tokens=64_000,
                cost_input=3,
                cost_output=15,
                cost_cache_read=0.3,
                cost_cache_write=3.75,
                swe_score=79.6,
            ),
            _antigravity_model(
                "gemini-3-flash",
                "Gemini 3 Flash (Antigravity)",
                reasoning=True,
                context_window=1_048_576,
                max_tokens=65535,
                cost_input=0.5,
                cost_output=3,
                cost_cache_read=0.5,
                swe_score=76.2,
            ),
            _antigravity_model(
                "gemini-3-pro-high",
                "Gemini 3 Pro High (Antigravity)",
                reasoning=True,
                context_window=1_048_576,
                max_tokens=65535,
                cost_input=2,
                cost_output=12,
                cost_cache_read=0.2,
                cost_cache_write=2.375,
                swe_score=76.2,
            ),
            _antigravity_model(
                "gemini-3-pro-low",
                "Gemini 3 Pro Low (Antigravity)",
                reasoning=True,
                context_window=1_048_576,
                max_tokens=65535,
                cost_input=2,
                cost_output=12,
                cost_cache_read=0.2,
                cost_cache_write=2.375,
                swe_score=76.2,
            ),
            _antigravity_model(
                "gemini-3.1-flash-lite",
                "Gemini 3.1 Flash Lite (Antigravity)",
                reasoning=True,
                context_window=1_048_576,
                max_tokens=65535,
                cost_input=0.1,
                cost_output=0.4,
                cost_cache_read=0.01,
            ),
            _antigravity_model(
                "gemini-3.1-pro-high",
                "Gemini 3.1 Pro High (Antigravity)",
                reasoning=True,
                context_window=1_048_576,
                max_tokens=65535,
                cost_input=2,
                cost_output=12,
                cost_cache_read=0.2,
                cost_cache_write=2.375,
                swe_score=80.6,
            ),
            _antigravity_model(
                "gemini-3.1-pro-low",
                "Gemini 3.1 Pro Low (Antigravity)",
                reasoning=True,
                context_window=1_048_576,
                max_tokens=65535,
                cost_input=2,
                cost_output=12,
                cost_cache_read=0.2,
                cost_cache_write=2.375,
                swe_score=80.6,
            ),
            _antigravity_model(
                "gemini-3.5-flash-low",
                "Gemini 3.5 Flash Low (Antigravity)",
                reasoning=True,
                context_window=1_048_576,
                max_tokens=65535,
                cost_input=1.5,
                cost_output=9,
                cost_cache_read=0.15,
                cost_cache_write=0.083,
            ),
            _antigravity_model(
                "gpt-oss-120b-medium",
                "GPT-OSS 120B Medium (Antigravity)",
                reasoning=False,
                context_window=131072,
                max_tokens=32768,
                cost_input=0.09,
                cost_output=0.36,
                inputs=("text",),
            ),
        ],
    )
)


fir_ext.run(name="antigravity-auth")
