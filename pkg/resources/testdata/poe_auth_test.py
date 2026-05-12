#!/usr/bin/env python3
"""Tests for the poe_auth builtin extension.

Drift detection: the static (non-per-session) part of the OAuth URL is
stored behind a pre-created TinyURL short link (``_SHORT_URL``). If any
of the static params drift — client_id, scope, code_challenge_method,
the response_type, or the authorize host — the short link becomes stale
and logins will use outdated/missing params.

When ``test_static_oauth_url_matches_short_link_target`` fails, the
fix is to re-create the short link to point at the new static URL
(printed in the assertion message).
"""

import os
import sys
import unittest
import urllib.parse
from unittest import mock

_ext_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "builtin_extensions")
_sdk_dir = os.path.join(
    os.path.dirname(os.path.abspath(__file__)),
    "..",
    "..",
    "extension",
    "sdk",
    "python",
)
sys.path.insert(0, _ext_dir)
sys.path.insert(0, _sdk_dir)

import fir_ext


def _load():
    if "poe_auth" in sys.modules:
        del sys.modules["poe_auth"]
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import poe_auth
    return poe_auth


_mod = _load()


# The exact static URL that the pre-created short link
# https://tinyurl.com/fir-poe currently points to. If you intentionally
# change anything below, recreate the short link to point at the new URL
# and update this constant.
_FROZEN_STATIC_URL = (
    "https://poe.com/oauth/authorize"
    "?client_id=client_9962de5dfb824c669587e4069666c5ee"
    "&response_type=code"
    "&scope=apikey%3Acreate"
    "&code_challenge_method=S256"
)


class TestStaticURLDrift(unittest.TestCase):
    """Detects drift between the static OAuth URL and the short link target."""

    def test_short_url_constant(self):
        self.assertEqual(_mod._SHORT_URL, "https://tinyurl.com/fir-poe")

    def test_static_oauth_url_matches_short_link_target(self):
        current = (
            _mod._AUTHORIZE_URL
            + "?"
            + urllib.parse.urlencode(_mod._static_auth_params(_mod._DEFAULT_CLIENT_ID))
        )
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
