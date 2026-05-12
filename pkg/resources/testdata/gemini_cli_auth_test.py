#!/usr/bin/env python3
"""Tests for the gemini_cli_auth builtin extension.

Locks in the wire-protocol Api spec, the provider record, and the model
catalogue that the extension hands off to fir at handshake. These used
to live as Go-level vars in pkg/ai/providers/register_gemini_cli.go;
they now travel as register_api(...) / register_provider(...) calls
inside the extension itself, so the tests live next to it.
"""

import json
import os
import sys
import unittest
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


def _load_gemini_cli_auth():
    """Import gemini_cli_auth.py with a clean fir_ext registry, returning
    a snapshot dict of the registrations it produced. We snapshot rather
    than read globals because other extension test modules in the same
    pytest run may also load extensions and replace these registries."""
    if "gemini_cli_auth" in sys.modules:
        del sys.modules["gemini_cli_auth"]
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import gemini_cli_auth
    return {
        "module": gemini_cli_auth,
        "apis": list(fir_ext._apis),
        "providers": list(fir_ext._providers),
        "auth_providers": list(fir_ext._auth_providers),
    }


_state = _load_gemini_cli_auth()
_mod = _state["module"]


class TestApiSpec(unittest.TestCase):
    """The extension must register exactly one decl-google Api."""

    def test_one_api_registered(self):
        ids = [a["id"] for a in _state["apis"]]
        self.assertEqual(ids, ["google-gemini-cli"])

    def test_kind_is_decl_google(self):
        spec = _state["apis"][0]
        self.assertEqual(spec["kind"], "decl-google")

    def test_payload_has_endpoints_and_envelope(self):
        spec = _state["apis"][0]
        payload = spec["payload"]
        self.assertIn("https://cloudcode-pa.googleapis.com", payload["endpoints"])
        self.assertEqual(payload["reasoning_header_prefix"], "x-gemini-thinking-")
        self.assertIn("${model.id}", payload["envelope"])
        self.assertIn("$inner", payload["envelope"])

    def test_headers_present(self):
        payload = _state["apis"][0]["payload"]
        self.assertIn("User-Agent", payload["headers"])
        self.assertIn("Client-Metadata", payload["headers"])


class TestProviderSpec(unittest.TestCase):
    def test_one_provider_registered(self):
        ids = [p["id"] for p in _state["providers"]]
        self.assertEqual(ids, ["google-gemini-cli"])

    def test_passthrough_to_decl_google_api(self):
        p = _state["providers"][0]
        self.assertEqual(p["api"], "google-gemini-cli")

    def test_oauth_wiring(self):
        p = _state["providers"][0]
        self.assertEqual(p["oauth_provider_id"], "google-gemini-cli")
        self.assertTrue(p["env_keys"]["authenticated"])

    def test_default_model_in_catalogue(self):
        p = _state["providers"][0]
        default = p["default_model_id"]
        ids = [m["id"] for m in p["models"]]
        self.assertIn(default, ids, f"default_model_id {default!r} not in models[]")

    def test_models_are_well_formed(self):
        p = _state["providers"][0]
        self.assertGreater(len(p["models"]), 0)
        for m in p["models"]:
            self.assertIn("id", m)
            self.assertIn("name", m)
            self.assertGreater(m["context_window"], 0)
            self.assertGreater(m["max_tokens"], 0)
            self.assertEqual(m["base_url"], "https://cloudcode-pa.googleapis.com")


class TestAuthProvider(unittest.TestCase):
    def test_one_auth_provider_registered(self):
        ids = [ap["id"] for ap in _state["auth_providers"]]
        self.assertEqual(ids, ["google-gemini-cli"])


class TestApiKey(unittest.TestCase):
    def test_returns_json_with_token_and_project(self):
        # gemini-cli wraps creds as a JSON blob the wire layer parses
        # via parseGoogleCreds (token + projectId).
        params = {"credentials": {"access": "tok_123", "extra": {"projectId": "proj_x"}}}
        out = json.loads(_mod.api_key(params, None))
        self.assertEqual(out["token"], "tok_123")
        self.assertEqual(out["projectId"], "proj_x")


# The exact static URL that the pre-created short link
# https://tinyurl.com/fir-gem currently points to. If you intentionally
# change anything below, recreate the short link to point at the new URL
# and update this constant.
_FROZEN_STATIC_URL = (
    "https://accounts.google.com/o/oauth2/v2/auth"
    "?client_id=681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
    "&response_type=code"
    "&scope=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcloud-platform"
    "+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fuserinfo.email"
    "+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fuserinfo.profile"
    "&code_challenge_method=S256"
    "&access_type=offline"
    "&prompt=consent"
)


class TestStaticURLDrift(unittest.TestCase):
    """Drift detection between the static OAuth URL and the pre-created
    short link target. See module docstring for repair steps."""

    def test_short_url_constant(self):
        import urllib.parse  # noqa: F401  (ensure available at module import)
        self.assertEqual(_mod._SHORT_URL, "https://tinyurl.com/fir-gem")

    def test_static_oauth_url_matches_short_link_target(self):
        import urllib.parse
        current = _mod._AUTH_URL + "?" + urllib.parse.urlencode(_mod._static_auth_params())
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
