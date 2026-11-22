#!/usr/bin/env python3
"""Drift-detection for the gemini_cli_auth builtin extension's pre-created
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

_PROVIDER_ID = "google-gemini-cli"
_FROZEN_SHORT_URL = "https://tinyurl.com/fir-gem"
_FROZEN_AUTHORIZE_URL = "https://accounts.google.com/o/oauth2/v2/auth"
_FROZEN_STATIC_PARAMS = {
    "client_id": "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com",
    "response_type": "code",
    "scope": (
        "https://www.googleapis.com/auth/cloud-platform"
        " https://www.googleapis.com/auth/userinfo.email"
        " https://www.googleapis.com/auth/userinfo.profile"
    ),
    "code_challenge_method": "S256",
    "access_type": "offline",
    "prompt": "consent",
}


def _load_spec(provider_id: str) -> dict:
    sys.modules.pop("gemini_cli_auth", None)
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import gemini_cli_auth  # noqa: F401
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


class TestPersonalAccountGuidance(unittest.TestCase):
    """Free/personal accounts moved to Antigravity; login must say so."""

    def setUp(self):
        _load_spec(_PROVIDER_ID)
        import gemini_cli_auth

        self.mod = gemini_cli_auth
        self.ctx = mock.Mock()

    def _discover(self, env, tier="free-tier"):
        resp = (
            200,
            {
                "allowedTiers": [{"id": tier, "isDefault": True}],
                "done": True,
                "response": {"cloudaicompanionProject": {"id": "p1"}},
            },
        )
        with (
            mock.patch.dict(os.environ, env, clear=True),
            mock.patch.object(self.mod, "_http_post_json", return_value=resp) as post,
        ):
            try:
                return self.mod._discover_project("tok", self.ctx), post
            except RuntimeError as e:
                return e, post

    def test_free_tier_without_project_points_to_antigravity(self):
        err, post = self._discover({})
        self.assertIsInstance(err, RuntimeError)
        self.assertIn("google-antigravity", str(err))
        self.assertIn("GOOGLE_CLOUD_PROJECT", str(err))
        self.assertEqual(post.call_count, 1)  # no onboarding attempted

    def test_free_tier_with_project_still_points_to_antigravity(self):
        err, post = self._discover({"GOOGLE_CLOUD_PROJECT": "p1"})
        self.assertIsInstance(err, RuntimeError)
        self.assertIn("google-antigravity", str(err))
        self.assertEqual(post.call_count, 1)

    def test_standard_tier_onboards_with_project(self):
        got, post = self._discover({"GOOGLE_CLOUD_PROJECT": "p1"}, tier="standard-tier")
        self.assertEqual(got, "p1")
        body = post.call_args_list[1].args[1]
        self.assertEqual(body["cloudaicompanionProject"], "p1")
        self.assertEqual(body["metadata"]["duetProject"], "p1")

    def test_spec_name_mentions_standard_enterprise_and_antigravity(self):
        name = _load_spec(_PROVIDER_ID)["name"]
        self.assertIn("Standard/Enterprise", name)
        self.assertIn("google-antigravity", name)


if __name__ == "__main__":
    unittest.main()
