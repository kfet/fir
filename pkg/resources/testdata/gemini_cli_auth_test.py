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
    """fir tries every account; the Antigravity hint appears only after Google fails."""

    def setUp(self):
        _load_spec(_PROVIDER_ID)
        import gemini_cli_auth

        self.mod = gemini_cli_auth
        self.ctx = mock.Mock()

    def _discover(self, env, tier="free-tier", responses=None):
        if responses is None:
            ok = (
                200,
                {
                    "allowedTiers": [{"id": tier, "isDefault": True}],
                    "done": True,
                    "response": {"cloudaicompanionProject": {"id": "p1"}},
                },
            )
            responses = [ok, ok]
        with (
            mock.patch.dict(os.environ, env, clear=True),
            mock.patch.object(self.mod, "_http_post_json", side_effect=responses) as post,
        ):
            try:
                return self.mod._discover_project("tok", self.ctx), post
            except RuntimeError as e:
                return e, post

    def test_free_tier_onboards_without_project(self):
        got, post = self._discover({})
        self.assertEqual(got, "p1")
        self.assertEqual(post.call_count, 2)
        body = post.call_args_list[1].args[1]
        self.assertEqual(body["tierId"], "free-tier")
        self.assertNotIn("cloudaicompanionProject", body)
        self.assertNotIn("duetProject", body["metadata"])

    def test_free_tier_login_succeeds_with_onboarded_project(self):
        load = (200, {"allowedTiers": [{"id": "free-tier", "isDefault": True}]})
        onboard = (
            200,
            {"done": True, "response": {"cloudaicompanionProject": {"id": "goog-proj"}}},
        )
        params = {"token": {"access_token": "a", "refresh_token": "r", "expires_at": 10**13}}
        with (
            mock.patch.dict(os.environ, {}, clear=True),
            mock.patch.object(self.mod, "_http_post_json", side_effect=[load, onboard]),
            mock.patch.object(self.mod, "_get_user_email", return_value="u@example.com"),
        ):
            creds = self.mod.post_exchange(params, self.ctx)
        self.assertEqual(creds["extra"]["projectId"], "goog-proj")
        self.assertEqual(creds["access"], "a")
        self.assertIsNone(self.mod.list_models({}, self.ctx))

    def test_free_current_tier_without_project_is_accepted(self):
        load = (200, {"currentTier": {"id": "free-tier"}})
        got, post = self._discover({}, responses=[load])
        self.assertEqual(got, "")
        self.assertEqual(post.call_count, 1)

    def test_free_tier_ignores_env_project_in_onboard(self):
        got, post = self._discover({"GOOGLE_CLOUD_PROJECT": "envp"})
        self.assertEqual(got, "p1")
        body = post.call_args_list[1].args[1]
        self.assertNotIn("cloudaicompanionProject", body)

    def test_standard_tier_onboards_with_project(self):
        got, post = self._discover({"GOOGLE_CLOUD_PROJECT": "p1"}, tier="standard-tier")
        self.assertEqual(got, "p1")
        body = post.call_args_list[1].args[1]
        self.assertEqual(body["cloudaicompanionProject"], "p1")
        self.assertEqual(body["metadata"]["duetProject"], "p1")

    def test_onboard_failure_keeps_google_error_and_adds_hint(self):
        load = (200, {"allowedTiers": [{"id": "free-tier", "isDefault": True}]})
        fail = (400, {"error": {"message": "google says no"}})
        err, _ = self._discover({}, responses=[load, fail])
        self.assertIsInstance(err, RuntimeError)
        self.assertIn("onboardUser failed (400)", str(err))
        self.assertIn("google says no", str(err))
        self.assertIn("fir login google-antigravity", str(err))

    def test_current_tier_without_project_calls_onboard(self):
        load = (200, {"currentTier": {"id": "standard-tier"}})
        ok = (200, {"done": True, "response": {"cloudaicompanionProject": {"id": "p9"}}})
        got, post = self._discover({}, responses=[load, ok])
        self.assertEqual(got, "p9")
        self.assertEqual(post.call_count, 2)
        self.assertIn("onboardUser", post.call_args_list[1].args[0])
        self.assertEqual(post.call_args_list[1].args[1]["tierId"], "standard-tier")

    def test_vpc_sc_without_env_project_calls_onboard(self):
        load = (403, {"error": {"details": [{"reason": "SECURITY_POLICY_VIOLATED"}]}})
        ok = (200, {"done": True, "response": {"cloudaicompanionProject": {"id": "p7"}}})
        got, post = self._discover({}, responses=[load, ok])
        self.assertEqual(got, "p7")
        self.assertEqual(post.call_args_list[1].args[1]["tierId"], "standard-tier")

    def test_current_tier_without_id_onboards_legacy(self):
        load = (200, {"currentTier": {}})
        ok = (200, {"done": True, "response": {"cloudaicompanionProject": {"id": "p8"}}})
        got, post = self._discover({}, responses=[load, ok])
        self.assertEqual(got, "p8")
        self.assertEqual(post.call_args_list[1].args[1]["tierId"], "legacy-tier")

    def test_standard_tier_without_env_project_calls_onboard(self):
        got, post = self._discover({}, tier="standard-tier")
        self.assertEqual(got, "p1")
        self.assertEqual(post.call_count, 2)
        body = post.call_args_list[1].args[1]
        self.assertEqual(body["tierId"], "standard-tier")
        self.assertNotIn("cloudaicompanionProject", body)

    def test_empty_project_reports_google_reason(self):
        load = (
            200,
            {
                "allowedTiers": [{"id": "free-tier", "isDefault": True}],
                "ineligibleTiers": [
                    {
                        "tierId": "free-tier",
                        "reasonCode": "UNSUPPORTED_CLIENT",
                        "reasonMessage": "not eligible here",
                    }
                ],
            },
        )
        empty = (200, {"done": True, "response": {}})
        err, post = self._discover({}, responses=[load, empty])
        self.assertIsInstance(err, RuntimeError)
        self.assertEqual(post.call_count, 2)
        self.assertIn("Google says: free-tier: not eligible here", str(err))
        self.assertIn("GOOGLE_CLOUD_PROJECT", str(err))
        self.assertIn("google-antigravity", str(err))

    def test_load_failure_adds_hint(self):
        err, _ = self._discover({}, responses=[(403, {"error": {"message": "denied"}})])
        self.assertIn("loadCodeAssist failed (403)", str(err))
        self.assertIn("denied", str(err))
        self.assertIn("google-antigravity", str(err))

    def test_poll_failure_adds_hint(self):
        with mock.patch.object(self.mod, "_http_get_json", return_value=(500, {"e": 1})):
            with self.assertRaises(RuntimeError) as cm:
                self.mod._poll_operation("op", {}, self.ctx)
        self.assertIn("poll operation failed (500)", str(cm.exception))
        self.assertIn("google-antigravity", str(cm.exception))

    def test_non_json_http_error_adds_hint(self):
        import email.message
        import io
        import urllib.error

        body = io.BytesIO(b"<html>denied</html>")
        err = urllib.error.HTTPError("https://x", 403, "no", email.message.Message(), body)
        with mock.patch("urllib.request.urlopen", side_effect=err):
            with self.assertRaises(RuntimeError) as cm:
                self.mod._http_post_json("https://x", {}, {})
        self.assertIn("HTTP 403: <html>denied</html>", str(cm.exception))
        self.assertIn("google-antigravity", str(cm.exception))


if __name__ == "__main__":
    unittest.main()
