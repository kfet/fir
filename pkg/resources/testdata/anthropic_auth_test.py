#!/usr/bin/env python3
"""Drift-detection for the anthropic_auth builtin extension's pre-created
TinyURL short link.

The short link at ``_FROZEN_SHORT_URL`` is configured to redirect to
``_FROZEN_AUTHORIZE_URL`` with the static (non-per-session) params in
``_FROZEN_STATIC_PARAMS`` baked in. fir appends the per-session params
(state, code_challenge, redirect_uri) at click time; the shortener
merges them with ``&``. If the params drift, the short link becomes
stale — re-create it and update the frozen constants below."""

import os
import sys
import unittest
from unittest import mock

_ext_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "builtin_extensions")
_sdk_dir = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "..", "..", "extension", "sdk", "python"
)
sys.path.insert(0, _ext_dir)
sys.path.insert(0, _sdk_dir)

import fir_ext

_PROVIDER_ID = "anthropic"
_FROZEN_SHORT_URL = "https://tinyurl.com/fir-ant"
_FROZEN_AUTHORIZE_URL = "https://claude.ai/oauth/authorize"
_FROZEN_STATIC_PARAMS = {
    "code": "true",
    "client_id": "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
    "response_type": "code",
    "scope": (
        "org:create_api_key user:profile user:inference user:sessions:claude_code "
        "user:mcp_servers user:file_upload"
    ),
    "code_challenge_method": "S256",
}


def _load_spec(provider_id: str) -> dict:
    sys.modules.pop("anthropic_auth", None)
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import anthropic_auth  # noqa: F401
    for p in fir_ext._auth_providers:
        if p["id"] == provider_id:
            return p
    raise AssertionError(f"provider {provider_id} not registered")


def _static_params(flow: dict) -> dict:
    """Reconstruct the set of static params the short link must encode."""
    params = {
        "client_id": flow["client_id"],
        "response_type": "code",
        "scope": flow["scope"],
        "code_challenge_method": "S256",
    }
    params.update(flow.get("auth_params_extra") or {})
    return params


class TestStaticURLDrift(unittest.TestCase):
    def setUp(self):
        self.flow = _load_spec(_PROVIDER_ID)["flow"]

    def test_short_url_base(self):
        self.assertEqual(self.flow.get("short_url_base"), _FROZEN_SHORT_URL)

    def test_authorize_url(self):
        self.assertEqual(self.flow["authorize_url"], _FROZEN_AUTHORIZE_URL)

    def test_static_params(self):
        current = _static_params(self.flow)
        self.assertEqual(
            current,
            _FROZEN_STATIC_PARAMS,
            "\n\nStatic OAuth params have drifted from the pre-created short link target.\n"
            f"Re-create {_FROZEN_SHORT_URL} to encode the params below, then\n"
            "update _FROZEN_STATIC_PARAMS in this test file:\n\n"
            f"  {current}\n",
        )


if __name__ == "__main__":
    unittest.main()
