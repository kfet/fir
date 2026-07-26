#!/usr/bin/env python3
"""Drift-detection for the poe_auth builtin extension's pre-created
TinyURL short link."""

import email.message
import io
import json
import os
import shutil
import sys
import tempfile
import unittest
import urllib.error
import urllib.request
from unittest import mock

_ext_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "builtin_extensions")
_sdk_dir = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "..", "..", "extension", "sdk", "python"
)
sys.path.insert(0, _ext_dir)
sys.path.insert(0, _sdk_dir)

import fir_ext

_PROVIDER_ID = "poe"
_FROZEN_SHORT_URL = "https://tinyurl.com/fir-poe"
_FROZEN_AUTHORIZE_URL = "https://poe.com/oauth/authorize"
_FROZEN_STATIC_PARAMS = {
    "client_id": "client_9962de5dfb824c669587e4069666c5ee",
    "response_type": "code",
    "scope": "apikey:create",
    "code_challenge_method": "S256",
}


def _load_spec(provider_id: str) -> dict:
    # Ensure no FIR_POE_* env vars steer us off the default code path.
    for k in ("FIR_POE_CLIENT_ID", "FIR_POE_REDIRECT_URI"):
        os.environ.pop(k, None)
    sys.modules.pop("poe_auth", None)
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import poe_auth  # noqa: F401
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


class TestResolveEndpoint(unittest.TestCase):
    """On-demand endpoint/callability resolution memo + probe behaviour."""

    def setUp(self):
        # Fresh import with a temp config dir so the memo persists to disk.
        self._tmp = tempfile.mkdtemp()
        for k in ("FIR_POE_CLIENT_ID", "FIR_POE_REDIRECT_URI"):
            os.environ.pop(k, None)
        sys.modules.pop("poe_auth", None)
        fir_ext._apis.clear()
        fir_ext._providers.clear()
        fir_ext._auth_providers.clear()
        fir_ext.config_dirs = [self._tmp]
        with mock.patch.object(fir_ext, "run"):
            import poe_auth
        self.poe = poe_auth
        # Reset module caches between tests.
        self.poe._memo_cache = None
        self.poe._empty_ids_cache = None

    def tearDown(self):
        shutil.rmtree(self._tmp, ignore_errors=True)

    def _resolve(self, model_id, api_key="k"):
        return self.poe.resolve_endpoint(
            {"provider_id": "poe", "model_id": model_id, "api_key": api_key}, None
        )

    def test_miss_probes_and_persists(self):
        with (
            mock.patch.object(self.poe, "_fetch_empty_endpoint_ids", return_value={"ambi"}),
            mock.patch.object(self.poe, "_probe_model", return_value={"callable": True}) as probe,
        ):
            out = self._resolve("ambi")
        self.assertEqual(out, {"callable": True})
        probe.assert_called_once()
        # Persisted to disk.
        with open(os.path.join(self._tmp, "poe-endpoints.json"), encoding="utf-8") as fh:
            self.assertEqual(json.load(fh), {"ambi": {"callable": True}})

    def test_hit_no_network(self):
        # Seed the memo file, then a second resolve must not probe.
        with (
            mock.patch.object(self.poe, "_fetch_empty_endpoint_ids", return_value={"ambi"}),
            mock.patch.object(self.poe, "_probe_model", return_value={"callable": True}) as probe,
        ):
            self._resolve("ambi")  # miss -> one probe
            self._resolve("ambi")  # hit -> no probe
            self._resolve("ambi")  # hit -> no probe
        probe.assert_called_once()

    def test_404_records_not_callable(self):
        with (
            mock.patch.object(self.poe, "_fetch_empty_endpoint_ids", return_value={"ambi"}),
            mock.patch.object(self.poe, "_probe_model", return_value={"callable": False}),
        ):
            out = self._resolve("ambi")
        self.assertEqual(out, {"callable": False})
        with open(os.path.join(self._tmp, "poe-endpoints.json"), encoding="utf-8") as fh:
            self.assertEqual(json.load(fh), {"ambi": {"callable": False}})

    def test_explicit_endpoint_model_not_probed(self):
        # Model not in the empty-endpoint set -> no correction, no probe.
        with (
            mock.patch.object(self.poe, "_fetch_empty_endpoint_ids", return_value={"ambi"}),
            mock.patch.object(self.poe, "_probe_model") as probe,
        ):
            out = self._resolve("explicit")
        self.assertIsNone(out)
        probe.assert_not_called()

    def test_probe_404_not_found_body(self):
        # _probe_model classifies a not_found_error 404 as callable=false.
        err = urllib.error.HTTPError(
            self.poe._CHAT_URL,
            404,
            "Not Found",
            email.message.Message(),
            io.BytesIO(b'{"error":{"type":"not_found_error"}}'),
        )
        with mock.patch.object(urllib.request, "urlopen", side_effect=err):
            self.assertEqual(self.poe._probe_model("x", "k"), {"callable": False})

    def test_probe_transient_stays_callable(self):
        err = urllib.error.HTTPError(
            self.poe._CHAT_URL, 429, "Too Many Requests", email.message.Message(), io.BytesIO(b"")
        )
        with mock.patch.object(urllib.request, "urlopen", side_effect=err):
            self.assertEqual(self.poe._probe_model("x", "k"), {"callable": True})


if __name__ == "__main__":
    unittest.main()
