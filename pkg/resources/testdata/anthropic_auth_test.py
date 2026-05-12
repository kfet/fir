#!/usr/bin/env python3
"""Tests for the anthropic_auth builtin extension — drift detection for the
pre-created TinyURL short link."""

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
    if "anthropic_auth" in sys.modules:
        del sys.modules["anthropic_auth"]
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import anthropic_auth
    return anthropic_auth


_mod = _load()


_FROZEN_STATIC_URL = (
    "https://claude.ai/oauth/authorize"
    "?code=true"
    "&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e"
    "&response_type=code"
    "&scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference+user%3Asessions%3Aclaude_code+user%3Amcp_servers+user%3Afile_upload"
    "&code_challenge_method=S256"
)


class TestStaticURLDrift(unittest.TestCase):
    def test_short_url_constant(self):
        self.assertEqual(_mod._SHORT_URL, "https://tinyurl.com/fir-ant")

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
