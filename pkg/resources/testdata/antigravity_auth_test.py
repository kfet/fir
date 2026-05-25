#!/usr/bin/env python3
"""Drift-detection for the antigravity_auth builtin extension's pre-created
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

_PROVIDER_ID = "google-antigravity"
_FROZEN_SHORT_URL = "https://tinyurl.com/fir-agr"
_FROZEN_AUTHORIZE_URL = "https://accounts.google.com/o/oauth2/v2/auth"
_FROZEN_STATIC_PARAMS = {
    "client_id": "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com",
    "response_type": "code",
    "scope": (
        "https://www.googleapis.com/auth/cloud-platform"
        " https://www.googleapis.com/auth/userinfo.email"
        " https://www.googleapis.com/auth/userinfo.profile"
        " https://www.googleapis.com/auth/cclog"
        " https://www.googleapis.com/auth/experimentsandconfigs"
    ),
    "code_challenge_method": "S256",
    "access_type": "offline",
    "prompt": "consent",
}


def _load_spec(provider_id: str) -> dict:
    sys.modules.pop("antigravity_auth", None)
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import antigravity_auth  # noqa: F401
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


class _FakeCtx:
    """Stand-in for fir_ext.AuthContext during list_models tests."""

    def __init__(self):
        self.messages: list[str] = []

    def progress(self, msg: str) -> None:
        self.messages.append(msg)


def _load_module():
    """Import antigravity_auth fresh so register_provider repopulates."""
    sys.modules.pop("antigravity_auth", None)
    fir_ext._apis.clear()
    fir_ext._providers.clear()
    fir_ext._auth_providers.clear()
    with mock.patch.object(fir_ext, "run"):
        import antigravity_auth
    return antigravity_auth


class TestListModelsProbe(unittest.TestCase):
    """Opportunistic probe: ``auth_list_models`` filters catalogue IDs by what
    Cloud Code Assist actually accepts (200/400/429/500 = live; 404 = missing).
    Fall back to ``None`` (permissive) on anything that smells like a broken
    probe rather than a stale catalogue."""

    def setUp(self):
        self.ag = _load_module()
        self.creds = {"access": "ya29.fake", "extra": {"projectId": "proj-1"}}
        self.params = {"provider_id": _PROVIDER_ID, "credentials": self.creds}

    def _patch_probe(self, response_map):
        """Patch _probe_one to return ``response_map.get(model_id, default)``."""
        default = response_map.get("*", 200)

        def fake(model_id, access, project):
            return response_map.get(model_id, default)

        return mock.patch.object(self.ag, "_probe_one", side_effect=fake)

    def test_catalogue_ids_includes_renamed_and_added(self):
        ids = self.ag._antigravity_catalogue_ids()
        self.assertIn("gemini-3.1-flash-lite", ids, "renamed from -light → -lite")
        self.assertIn("gemini-3.5-flash-low", ids, "newly added via probe skill")
        self.assertNotIn("gemini-3.1-flash-light", ids, "old typo must be gone")

    def test_preflight_ids_all_present_in_catalogue(self):
        # Pre-flight relies on at least one ID from _PREFLIGHT_PROBE_IDS
        # being in the registered catalogue; the catalogue_ids[:1] fallback
        # is paranoia, not the happy path. If this fails, the catalogue
        # was edited without updating _PREFLIGHT_PROBE_IDS — fix one or
        # the other.
        catalogue = set(self.ag._antigravity_catalogue_ids())
        registered = set(self.ag._PREFLIGHT_PROBE_IDS) & catalogue
        self.assertTrue(
            registered,
            f"none of _PREFLIGHT_PROBE_IDS={self.ag._PREFLIGHT_PROBE_IDS} "
            f"are in catalogue={sorted(catalogue)}; update the constant",
        )

    def test_all_live_returns_full_catalogue(self):
        with self._patch_probe({"*": 200}):
            out = self.ag.list_models(self.params, _FakeCtx())
        self.assertEqual(sorted(self.ag._antigravity_catalogue_ids()), out)

    def test_404_ids_are_filtered_out(self):
        stale = "claude-sonnet-4-5"  # in catalogue, observed 404 in prod
        with self._patch_probe({stale: 404}):
            out = self.ag.list_models(self.params, _FakeCtx())
        self.assertIsNotNone(out)
        assert out is not None  # for type-checker
        self.assertNotIn(stale, out)
        # Everything else still present.
        for cid in self.ag._antigravity_catalogue_ids():
            if cid != stale:
                self.assertIn(cid, out)

    def test_existence_codes_keep_model_live(self):
        catalogue = self.ag._antigravity_catalogue_ids()
        # 400 (bad request), 429 (rate-limited), 500 (server error) all
        # prove the model exists. Each must stay in the live list.
        for code in (400, 429, 500):
            with self.subTest(code=code):
                target = catalogue[0]
                with self._patch_probe({target: code}):
                    out = self.ag.list_models(self.params, _FakeCtx())
                self.assertIsNotNone(out)
                assert out is not None
                self.assertIn(target, out)

    def test_auth_failure_codes_stay_permissive(self):
        # 0 (network down), 401/403 (auth) tell us nothing about the model.
        # Pre-flight catches these and bails to None — fir treats None as
        # "no filter, show the full static catalogue", so the user-facing
        # outcome is still permissive even though we skip the parallel sweep.
        for code in (0, 401, 403):
            with self.subTest(code=code):
                with self._patch_probe({"*": code}):
                    out = self.ag.list_models(self.params, _FakeCtx())
                # None means "no filter applied" — fir interprets that as
                # the full static catalogue, which is the correct outcome.
                self.assertIsNone(out)

    def test_everything_404_falls_back_to_none(self):
        # If literally every probe is 404 it's almost certainly auth or
        # endpoint breakage, not a catalogue full of stale entries.
        with self._patch_probe({"*": 404}):
            out = self.ag.list_models(self.params, _FakeCtx())
        self.assertIsNone(out, "all-404 should bail to permissive None")

    def test_missing_creds_returns_none(self):
        for creds in ({}, {"access": ""}, {"access": "tok"}, {"extra": {"projectId": "p"}}):
            with self.subTest(creds=creds):
                out = self.ag.list_models(
                    {"provider_id": _PROVIDER_ID, "credentials": creds}, _FakeCtx()
                )
                self.assertIsNone(out)

    def test_progress_message_includes_stale_count(self):
        ctx = _FakeCtx()
        stale = "claude-sonnet-4-5-thinking"
        with self._patch_probe({stale: 404}):
            self.ag.list_models(self.params, ctx)
        joined = " ".join(ctx.messages)
        self.assertIn("Probing", joined)
        self.assertIn("stale", joined)

    def test_disable_env_var_bypasses_probe(self):
        # Opt-out for paid users who don't want diagnostic token spend.
        with mock.patch.dict(os.environ, {"FIR_ANTIGRAVITY_DISABLE_PROBE": "1"}):
            with mock.patch.object(self.ag, "_probe_one") as mocked:
                out = self.ag.list_models(self.params, _FakeCtx())
        self.assertIsNone(out)
        mocked.assert_not_called()

    def test_preflight_auth_failure_aborts_without_full_sweep(self):
        # Every probe returns 401 — pre-flight must abort before the full
        # parallel sweep fires, saving 12 wasted requests on a dead token.
        call_count = 0

        def fake(model_id, access, project):
            nonlocal call_count
            call_count += 1
            return 401

        with mock.patch.object(self.ag, "_probe_one", side_effect=fake):
            out = self.ag.list_models(self.params, _FakeCtx())
        self.assertIsNone(out)
        # Pre-flight tries known-good IDs in order; on 401 it gives up
        # without firing the rest of the catalogue. Should be ≤3 calls
        # (the size of _PREFLIGHT_PROBE_IDS), well under the full 13.
        self.assertLessEqual(call_count, len(self.ag._PREFLIGHT_PROBE_IDS))


if __name__ == "__main__":
    unittest.main()
