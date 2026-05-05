#!/usr/bin/env python3
"""Tests for the antigravity_auth builtin extension.

Locks in the wire-protocol Api spec, the provider record, and the model
catalogue that the extension hands off to fir at handshake.
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


def _load_antigravity_auth():
    """Import antigravity_auth.py with a clean fir_ext registry, returning
    a snapshot of the registrations it produced."""
    if "antigravity_auth" in sys.modules:
        del sys.modules["antigravity_auth"]
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import antigravity_auth
    return {
        "module": antigravity_auth,
        "apis": list(fir_ext._apis),
        "providers": list(fir_ext._providers),
        "auth_providers": list(fir_ext._auth_providers),
    }


_state = _load_antigravity_auth()
_mod = _state["module"]


class TestApiSpec(unittest.TestCase):
    """The extension must register exactly one decl-google Api with
    conditional Claude-thinking headers and a multi-part system
    instruction prefix."""

    def test_one_api_registered(self):
        ids = [a["id"] for a in _state["apis"]]
        self.assertEqual(ids, ["google-antigravity"])

    def test_kind_is_decl_google(self):
        spec = _state["apis"][0]
        self.assertEqual(spec["kind"], "decl-google")

    def test_endpoints(self):
        payload = _state["apis"][0]["payload"]
        # Three sandbox + production hosts, in fallback order.
        self.assertEqual(len(payload["endpoints"]), 3)
        self.assertTrue(payload["endpoints"][0].startswith("https://"))

    def test_conditional_anthropic_beta_header(self):
        payload = _state["apis"][0]["payload"]
        cond = payload["conditional_headers"]
        self.assertEqual(len(cond), 1)
        self.assertEqual(cond[0]["when"]["model_id_prefix"], "claude-")
        self.assertTrue(cond[0]["when"]["requires_reasoning"])
        self.assertEqual(
            cond[0]["set"]["anthropic-beta"], "interleaved-thinking-2025-05-14"
        )

    def test_system_instruction_prefix_two_parts(self):
        payload = _state["apis"][0]["payload"]
        prefix = payload["system_instruction_prefix"]
        self.assertEqual(len(prefix), 2)
        self.assertIn("ignore", prefix[1]["text"])
        self.assertEqual(payload.get("system_instruction_role", "user"), "user")

    def test_envelope_request_type_agent(self):
        payload = _state["apis"][0]["payload"]
        self.assertIn('"requestType":"agent"', payload["envelope"])
        self.assertIn("$inner", payload["envelope"])


class TestProviderSpec(unittest.TestCase):
    def test_one_provider_registered(self):
        ids = [p["id"] for p in _state["providers"]]
        self.assertEqual(ids, ["google-antigravity"])

    def test_passthrough_to_antigravity_api(self):
        p = _state["providers"][0]
        # Antigravity uses its own decl-google Api (NOT gemini-cli's),
        # so its conditional Claude headers + system prompt fire.
        self.assertEqual(p["api"], "google-antigravity")

    def test_oauth_wiring(self):
        p = _state["providers"][0]
        self.assertEqual(p["oauth_provider_id"], "google-antigravity")
        self.assertTrue(p["env_keys"]["authenticated"])

    def test_default_model_in_catalogue(self):
        p = _state["providers"][0]
        ids = [m["id"] for m in p["models"]]
        self.assertIn(p["default_model_id"], ids)

    def test_includes_claude_models_for_conditional_headers(self):
        # Antigravity hosts Claude variants — these are exactly the
        # models that exercise the conditional anthropic-beta header
        # path, so missing them would defeat the conditional config.
        p = _state["providers"][0]
        ids = [m["id"] for m in p["models"]]
        self.assertTrue(
            any(i.startswith("claude-") for i in ids),
            f"expected at least one claude-* model, got {ids}",
        )


class TestAuthProvider(unittest.TestCase):
    def test_one_auth_provider_registered(self):
        ids = [ap["id"] for ap in _state["auth_providers"]]
        self.assertEqual(ids, ["google-antigravity"])


class TestApiKey(unittest.TestCase):
    def test_returns_json_with_token_and_project(self):
        # antigravity wraps creds the same way as gemini-cli — JSON blob
        # with token + projectId — for the decl-google wire layer.
        params = {"credentials": {"access": "tok_xyz", "extra": {"projectId": "proj_a"}}}
        out = json.loads(_mod.api_key(params, None))
        self.assertEqual(out["token"], "tok_xyz")
        self.assertEqual(out["projectId"], "proj_a")


if __name__ == "__main__":
    unittest.main()
