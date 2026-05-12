#!/usr/bin/env python3
"""Tests for the codex_auth builtin extension — drift detection for the
pre-created TinyURL short link.

If ``test_static_oauth_url_matches_short_link_target`` fails, the fix is
to re-create the short link to point at the new static URL (printed in
the assertion message) and update ``_FROZEN_STATIC_URL`` here.
"""

import os
import sys
import unittest
import urllib.parse
from unittest import mock

_ext_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "builtin_extensions")
_sdk_dir = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "..", "..", "extension", "sdk", "python"
)
sys.path.insert(0, _ext_dir)
sys.path.insert(0, _sdk_dir)

import fir_ext


def _load():
    if "codex_auth" in sys.modules:
        del sys.modules["codex_auth"]
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import codex_auth
    return codex_auth


_mod = _load()


_FROZEN_STATIC_URL = (
    "https://auth.openai.com/oauth/authorize"
    "?response_type=code"
    "&client_id=app_EMoamEEZ73f0CkXaXp7hrann"
    "&scope=openid+profile+email+offline_access"
    "&code_challenge_method=S256"
    "&id_token_add_organizations=true"
    "&codex_cli_simplified_flow=true"
    "&originator=fir"
)


class TestStaticURLDrift(unittest.TestCase):
    def test_short_url_constant(self):
        self.assertEqual(_mod._SHORT_URL, "https://tinyurl.com/fir-cdx")

    def test_static_oauth_url_matches_short_link_target(self):
        current = _mod._AUTHORIZE_URL + "?" + urllib.parse.urlencode(_mod._static_auth_params())
        self.assertEqual(
            current,
            _FROZEN_STATIC_URL,
            "\n\nStatic OAuth URL has drifted from the pre-created short link target.\n"
            f"Re-create {_mod._SHORT_URL} to point at the new URL below, then\n"
            "update _FROZEN_STATIC_URL in this test file:\n\n"
            f"  {current}\n",
        )


if __name__ == "__main__":
    unittest.main()
