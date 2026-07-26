#!/usr/bin/env python3
"""Tests for the copilot_auth builtin extension."""

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


def _load_copilot_auth():
    """Import copilot_auth.py, capturing newly registered handlers."""
    if "copilot_auth" in sys.modules:
        del sys.modules["copilot_auth"]

    with mock.patch.object(fir_ext, "run"):
        import copilot_auth
    return copilot_auth


# Load once for pure-logic tests
_mod = _load_copilot_auth()


class TestNormalizeDomain(unittest.TestCase):
    def test_blank(self):
        self.assertEqual(_mod._normalize_domain(""), "")

    def test_bare_hostname(self):
        self.assertEqual(_mod._normalize_domain("company.ghe.com"), "company.ghe.com")

    def test_with_scheme(self):
        self.assertEqual(_mod._normalize_domain("https://company.ghe.com"), "company.ghe.com")

    def test_with_path(self):
        self.assertEqual(_mod._normalize_domain("https://company.ghe.com/foo"), "company.ghe.com")

    def test_whitespace(self):
        self.assertEqual(_mod._normalize_domain("  company.ghe.com  "), "company.ghe.com")


class TestBaseURLFromToken(unittest.TestCase):
    def test_extracts_proxy_ep(self):
        token = "tid=abc;exp=123;proxy-ep=proxy.individual.githubcopilot.com;sku=free"  # noqa: S105
        self.assertEqual(
            _mod._base_url_from_token(token),
            "https://api.individual.githubcopilot.com",
        )

    def test_no_proxy_ep(self):
        self.assertEqual(_mod._base_url_from_token("tid=abc;exp=123"), "")

    def test_enterprise_proxy(self):
        token = "tid=abc;proxy-ep=proxy.business.githubcopilot.com;exp=999"  # noqa: S105
        self.assertEqual(
            _mod._base_url_from_token(token),
            "https://api.business.githubcopilot.com",
        )


class TestCopilotBaseURL(unittest.TestCase):
    def test_from_token(self):
        token = "tid=abc;proxy-ep=proxy.individual.githubcopilot.com;exp=123"  # noqa: S105
        self.assertEqual(
            _mod._copilot_base_url(token, ""),
            "https://api.individual.githubcopilot.com",
        )

    def test_enterprise_fallback(self):
        self.assertEqual(
            _mod._copilot_base_url("", "company.ghe.com"),
            "https://copilot-api.company.ghe.com",
        )

    def test_default(self):
        self.assertEqual(
            _mod._copilot_base_url("", ""),
            "https://api.individual.githubcopilot.com",
        )

    def test_token_takes_precedence(self):
        token = "tid=abc;proxy-ep=proxy.individual.githubcopilot.com;exp=123"  # noqa: S105
        self.assertEqual(
            _mod._copilot_base_url(token, "company.ghe.com"),
            "https://api.individual.githubcopilot.com",
        )


class TestGitHubURLs(unittest.TestCase):
    def test_github_com(self):
        dc, at, ct = _mod._github_urls("github.com")
        self.assertEqual(dc, "https://github.com/login/device/code")
        self.assertEqual(at, "https://github.com/login/oauth/access_token")
        self.assertEqual(ct, "https://api.github.com/copilot_internal/v2/token")

    def test_enterprise(self):
        dc, at, ct = _mod._github_urls("ghe.example.com")
        self.assertEqual(dc, "https://ghe.example.com/login/device/code")
        self.assertEqual(at, "https://ghe.example.com/login/oauth/access_token")
        self.assertEqual(ct, "https://api.ghe.example.com/copilot_internal/v2/token")


class TestModifyModels(unittest.TestCase):
    def test_sets_base_url(self):
        token = "tid=abc;proxy-ep=proxy.individual.githubcopilot.com;exp=123"  # noqa: S105
        params = {
            "credentials": {"access": token, "extra": {}},
            "models": [
                {"provider": "github-copilot", "id": "gpt-4o", "baseUrl": ""},
                {
                    "provider": "anthropic",
                    "id": "claude-3.5-sonnet",
                    "baseUrl": "https://api.anthropic.com",
                },
            ],
        }
        result = _mod.modify_models(params, None)
        self.assertEqual(result[0]["baseUrl"], "https://api.individual.githubcopilot.com")
        self.assertEqual(result[0]["headers"]["User-Agent"], "GitHubCopilotChat/0.35.0")
        self.assertIn("Editor-Version", result[0]["headers"])
        # Non-copilot model unchanged
        self.assertEqual(result[1]["baseUrl"], "https://api.anthropic.com")
        self.assertNotIn("headers", result[1])

    def test_enterprise_domain(self):
        params = {
            "credentials": {"access": "no-proxy-ep", "extra": {"enterpriseUrl": "ghe.example.com"}},
            "models": [
                {"provider": "github-copilot", "id": "gpt-4o", "baseUrl": ""},
            ],
        }
        result = _mod.modify_models(params, None)
        self.assertEqual(result[0]["baseUrl"], "https://copilot-api.ghe.example.com")


class TestAPIKey(unittest.TestCase):
    def test_returns_access_token(self):
        params = {"credentials": {"access": "tok_123", "refresh": "gh_abc"}}
        self.assertEqual(_mod.api_key(params, None), "tok_123")


class TestClientID(unittest.TestCase):
    def test_decoded(self):
        self.assertEqual(_mod._CLIENT_ID, "Iv1.b507a08c87ecfe98")


if __name__ == "__main__":
    unittest.main()
