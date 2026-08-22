#!/usr/bin/env python3
"""Unit tests for the openrouter_auth builtin extension.

Unlike the declarative providers (codex/poe/antigravity), openrouter uses the
imperative `fir_ext.auth_provider` path, so there is no static authorize-URL
drift to guard. Instead we exercise the login handler end-to-end against a
fake OpenRouter key-exchange server and the refresh no-op.
"""

import json
import os
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from unittest import mock

_ext_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "builtin_extensions")
_sdk_dir = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "..", "..", "extension", "sdk", "python"
)
sys.path.insert(0, _ext_dir)
sys.path.insert(0, _sdk_dir)

import fir_ext

_PROVIDER_ID = "openrouter"


def _load_module():
    """Fresh-import openrouter_auth with fir_ext.run patched out."""
    sys.modules.pop("openrouter_auth", None)
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import openrouter_auth
    return openrouter_auth


class _FakeContext:
    """Minimal stand-in for fir_ext.AuthContext."""

    def __init__(self, code="auth-code-123"):
        self.pkce = {"verifier": "v" * 64, "challenge": "c" * 43}
        self.code = code
        self.server = {
            "addr": "127.0.0.1:53692",
            "redirect_uri": "http://localhost:53692/callback",
        }
        self.calls = []

    def generate_pkce(self):
        self.calls.append("generate_pkce")
        return self.pkce

    def start_callback_server(self, addr="", path="", state=""):
        self.calls.append(("start_callback_server", addr, path, state))
        return self.server

    def await_callback(self):
        self.calls.append("await_callback")
        return {"code": self.code}

    def stop_callback_server(self):
        self.calls.append("stop_callback_server")

    def open_url(self, url, short_url="", instructions=""):
        self.calls.append(("open_url", url))
        self.opened_url = url

    def progress(self, message):
        self.calls.append("progress")


class _FakeServer(HTTPServer):
    """HTTPServer that remembers the last exchange body for assertions."""

    last_body: dict


class _Handler(BaseHTTPRequestHandler):
    # last_body is stashed per-server by do_POST for assertions.
    server: "_FakeServer"

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length) or b"{}")
        self.server.last_body = body
        if body.get("code") == "good":
            payload, status = {"key": "sk-or-v1-fake"}, 200
        elif self.path == "/api/v1/auth/keys":
            payload, status = {"error": "bad code"}, 403
        else:
            payload, status = {"error": "not found"}, 404
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, format, *args):  # noqa: A002 - silence logging
        pass


class OpenRouterAuthTest(unittest.TestCase):
    def setUp(self):
        for k in ("FIR_OPENROUTER_AUTH_URL", "FIR_OPENROUTER_KEYS_URL"):
            os.environ.pop(k, None)
        self.mod = _load_module()

    def test_provider_registered(self):
        ids = [p["id"] for p in fir_ext._auth_providers]
        self.assertIn(_PROVIDER_ID, ids)
        p = next(p for p in fir_ext._auth_providers if p["id"] == _PROVIDER_ID)
        self.assertTrue(p.get("uses_callback_server", False))

    def test_login_happy_path(self):
        srv = _FakeServer(("127.0.0.1", 0), _Handler)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        self.addCleanup(srv.shutdown)
        self.addCleanup(srv.server_close)
        keys_url = f"http://127.0.0.1:{srv.server_port}/api/v1/auth/keys"

        ctx = _FakeContext(code="good")
        with mock.patch.dict(
            os.environ,
            {
                "FIR_OPENROUTER_AUTH_URL": "https://openrouter.ai/auth",
                "FIR_OPENROUTER_KEYS_URL": keys_url,
            },
        ):
            creds = self.mod.login({"provider_id": _PROVIDER_ID}, ctx)

        # Credential shape: long-lived user-owned key, no refresh.
        self.assertEqual(creds["access"], "sk-or-v1-fake")
        self.assertEqual(creds["refresh"], "")
        self.assertEqual(creds["expires"], 0)

        # Exchange body must carry PKCE verifier + S256 method.
        self.assertEqual(
            srv.last_body,
            {
                "code": "good",
                "code_verifier": ctx.pkce["verifier"],
                "code_challenge_method": "S256",
            },
        )

        # Auth URL carries callback_url + S256 challenge, no client_id/state.
        opened = dict(c for c in ctx.calls if isinstance(c, tuple) and c[0] == "open_url")
        url = opened["open_url"]
        self.assertTrue(url.startswith("https://openrouter.ai/auth?"))
        from urllib.parse import parse_qs, urlparse

        q = parse_qs(urlparse(url).query)
        self.assertEqual(q["callback_url"], [ctx.server["redirect_uri"]])
        self.assertEqual(q["code_challenge"], [ctx.pkce["challenge"]])
        self.assertEqual(q["code_challenge_method"], ["S256"])
        self.assertNotIn("client_id", q)
        self.assertNotIn("state", q)

        # Callback server torn down exactly once.
        self.assertEqual(ctx.calls.count("stop_callback_server"), 1)

        # The raw key must never appear in progress output (redaction contract).
        progresses = [c[1] for c in ctx.calls if isinstance(c, tuple) and c[0] == "progress"]
        for msg in progresses:
            self.assertNotIn("sk-or-v1-fake", msg)

    def test_login_missing_code_raises(self):
        ctx = _FakeContext(code="")
        with self.assertRaises(RuntimeError):
            self.mod.login({"provider_id": _PROVIDER_ID}, ctx)
        # The finally-block tears down the callback server during the
        # browser/callback phase; the missing-code raise comes after that.
        self.assertEqual(ctx.calls.count("stop_callback_server"), 1)

    def test_exchange_http_error_is_actionable(self):
        srv = _FakeServer(("127.0.0.1", 0), _Handler)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        self.addCleanup(srv.shutdown)
        self.addCleanup(srv.server_close)
        keys_url = f"http://127.0.0.1:{srv.server_port}/api/v1/auth/keys"

        ctx = _FakeContext(code="bad")
        with mock.patch.dict(
            os.environ,
            {"FIR_OPENROUTER_KEYS_URL": keys_url},
        ):
            with self.assertRaises(RuntimeError) as cm:
                self.mod.login({"provider_id": _PROVIDER_ID}, ctx)
        self.assertIn("HTTP 403", str(cm.exception))
        self.assertIn("invalid code or code_verifier", str(cm.exception))

    def test_refresh_noop_returns_credentials(self):
        creds = {"access": "sk-or-v1-x", "refresh": "", "expires": 0}
        out = self.mod.refresh({"provider_id": _PROVIDER_ID, "credentials": creds}, None)
        self.assertEqual(out, {**creds, "extra": {}})

    def test_refresh_without_key_raises(self):
        with self.assertRaises(RuntimeError):
            self.mod.refresh({"provider_id": _PROVIDER_ID, "credentials": {}}, None)


if __name__ == "__main__":
    unittest.main()
