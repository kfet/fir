#!/usr/bin/env python3
"""Drift-detection for the codex_auth builtin extension's pre-created
TinyURL short link."""

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

_PROVIDER_ID = "openai-codex"
_FROZEN_SHORT_URL = "https://tinyurl.com/fir-cdx"
_FROZEN_AUTHORIZE_URL = "https://auth.openai.com/oauth/authorize"
_FROZEN_STATIC_PARAMS = {
    "client_id": "app_EMoamEEZ73f0CkXaXp7hrann",
    "response_type": "code",
    "scope": "openid profile email offline_access",
    "code_challenge_method": "S256",
    "id_token_add_organizations": "true",
    "codex_cli_simplified_flow": "true",
    "originator": "fir",
}


def _load_spec(provider_id: str) -> dict:
    sys.modules.pop("codex_auth", None)
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import codex_auth  # noqa: F401
    for p in fir_ext._auth_providers:
        if p["id"] == provider_id:
            return p
    raise AssertionError(f"provider {provider_id} not registered")


def _static_params(flow: dict) -> dict:
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

    def test_callback_redirect_uri_frozen(self):
        # Codex's OAuth client only whitelists this exact loopback
        # redirect — wildcard port and different paths are both rejected
        # with "Redirect URI ... is not supported by client".
        # Empirically verified 2026-05-14. Regression guard for the
        # short-link refactor that briefly switched this to
        # 127.0.0.1:0 + /cb.
        self.assertEqual(self.flow.get("callback_addr"), "127.0.0.1:1455")
        self.assertEqual(self.flow.get("callback_path"), "/auth/callback")


if __name__ == "__main__":
    unittest.main()
