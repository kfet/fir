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

    def test_callback_redirect_uri_frozen(self):
        # Anthropic accepts any loopback port (RFC 8252 §7.3) but the
        # path is whitelisted exactly — only "/callback" is accepted;
        # "/cb" yields "Redirect URI ... is not supported by client".
        # Empirically verified 2026-05-14. Regression guard for the
        # short-link refactor that briefly switched the path to /cb.
        self.assertEqual(self.flow.get("callback_addr"), "127.0.0.1:0")
        self.assertEqual(self.flow.get("callback_path"), "/callback")

    def test_state_echoed_in_token_body(self):
        # Anthropic's https://platform.claude.com/v1/oauth/token requires
        # the OAuth `state` value to be echoed back in the token-request
        # body. This is a non-standard quirk of the Claude-Code OAuth
        # client — without it the endpoint returns `invalid_request`.
        # Regression guard for the v0.43.x → main extraction that lost
        # the state passthrough. The "{state}" placeholder is
        # substituted by fir with the per-session state value at
        # request time.
        self.assertEqual(self.flow.get("token_body_extra", {}).get("state"), "{state}")


class TestPostExchangeAccountCapture(unittest.TestCase):
    """post_exchange must surface the account identity (uuid/email) so fir can
    label the account and keep multiple Anthropic logins side by side."""

    def setUp(self):
        _load_spec(_PROVIDER_ID)
        self.handler = fir_ext._auth_post_exchange_handlers[_PROVIDER_ID]

    def _call(self, token):
        return self.handler({"token": token}, None)

    def test_captures_uuid_and_email(self):
        out = self._call(
            {
                "access_token": "a",
                "refresh_token": "r",
                "expires_at": 10_000_000_000_000,
                "raw": {"account": {"uuid": "u-123", "email_address": "me@x.com"}},
            }
        )
        # Account id is the readable email, not the uuid.
        self.assertEqual(out["extra"]["accountId"], "me@x.com")
        self.assertEqual(out["extra"]["label"], "me@x.com")
        self.assertEqual(out["access"], "a")

    def test_org_distinguishes_same_user_readable(self):
        # Same user in two orgs -> different, READABLE account ids (no uuid) and
        # org-labelled display names.
        def call(org_name):
            return self._call(
                {
                    "access_token": "a",
                    "refresh_token": "r",
                    "expires_at": 10_000_000_000_000,
                    "raw": {
                        "account": {"uuid": "u-123", "email_address": "me@x.com"},
                        "organization": {"uuid": "o-1", "name": org_name},
                    },
                }
            )

        work = call("Acme Corp")
        personal = call("Personal Space")
        self.assertEqual(work["extra"]["accountId"], "me@x.com-acme-corp")
        self.assertEqual(personal["extra"]["accountId"], "me@x.com-personal-space")
        self.assertNotEqual(
            work["extra"]["accountId"], personal["extra"]["accountId"]
        )
        # No raw uuids in the slot-key-bound account id.
        self.assertNotIn("u-123", work["extra"]["accountId"])
        self.assertNotIn("o-1", work["extra"]["accountId"])
        self.assertEqual(work["extra"]["label"], "me@x.com (Acme Corp)")

    def test_prefers_display_name_in_label(self):
        out = self._call(
            {
                "access_token": "a",
                "refresh_token": "r",
                "expires_at": 10_000_000_000_000,
                "raw": {
                    "account": {
                        "uuid": "u-9",
                        "email_address": "me@x.com",
                        "display_name": "Ada Lovelace",
                    },
                    "organization": {"uuid": "o-2", "name": "Acme Corp"},
                },
            }
        )
        self.assertEqual(out["extra"]["label"], "Ada Lovelace (Acme Corp)")
        self.assertEqual(out["extra"]["name"], "Ada Lovelace")
        # Account id still keyed on email (stable), not the display name.
        self.assertEqual(out["extra"]["accountId"], "me@x.com-acme-corp")

    def test_uuid_fallback_when_no_email(self):
        out = self._call(
            {
                "access_token": "a",
                "refresh_token": "r",
                "raw": {"account": {"uuid": "u-only"}},
            }
        )
        self.assertEqual(out["extra"]["accountId"], "u-only")

    def test_no_account_no_extra(self):
        out = self._call({"access_token": "a", "refresh_token": "r", "raw": {}})
        self.assertNotIn("extra", out)


if __name__ == "__main__":
    unittest.main()
