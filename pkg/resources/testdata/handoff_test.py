#!/usr/bin/env python3
"""Tests for the handoff builtin extension.

Covers the validation logic in ``handoff.py``:

* content type / emptiness / length floor / length ceiling / line count;
* atomic-write + post-write readability check;
* default path is absolute;
* the restart prompt embeds the doc path verbatim.

The ``ctx.restart_session`` round-trip itself is exercised through the
``demo_ext_test.py`` ``restart_demo`` tool — here we only verify the
extension's *local* logic, with a stub ctx.
"""

from __future__ import annotations

import os
import shutil
import sys
import tempfile
import unittest
from unittest import mock

_ext_dir = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "..", "builtin_extensions"
)
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


def _load_handoff(cwd: str):
    """(Re-)import handoff.py with fir_ext.cwd pointed at ``cwd``.

    Forcing a re-import gives every test an isolated copy of the module's
    state and lets us redirect the default-path logic at a tempdir.
    """
    if "handoff" in sys.modules:
        del sys.modules["handoff"]
    fir_ext.cwd = cwd
    with mock.patch.object(fir_ext, "run"):
        import handoff  # type: ignore[import-not-found]
    return handoff


class _StubContext:
    """Minimal stub of fir_ext.Context used by the tool handler.

    Captures restart_session calls; everything else either no-ops or
    raises (so accidental usage during validation is surfaced).
    """

    def __init__(self, raise_on_restart: bool = False):
        self.restart_calls: list[str] = []
        self.raise_on_restart = raise_on_restart

    def restart_session(self, prompt: str) -> None:
        if self.raise_on_restart:
            raise RuntimeError("simulated restart failure")
        self.restart_calls.append(prompt)


def _good_content() -> str:
    """A content body that satisfies every validation rule.

    >=200 chars after strip, >=3 non-blank lines, <=64 KB.
    """
    return (
        "# Self-Handoff\n"
        "\n"
        "## Context\n"
        "Working on the reliable-self-handoff branch. The handoff extension "
        "writes a doc atomically and restarts the session pointing the new "
        "agent at the doc.\n"
        "\n"
        "## Next\n"
        "Run review-and-fix; merge to main.\n"
    )


# ---------------------------------------------------------------------------
# Validation
# ---------------------------------------------------------------------------


class TestValidateContent(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp()
        self.handoff = _load_handoff(self.tmp)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_rejects_non_string(self) -> None:
        err, _ = self.handoff._validate_content(42)
        self.assertIsNotNone(err)
        assert err is not None
        self.assertIn("must be a string", err)

    def test_rejects_empty(self) -> None:
        err, _ = self.handoff._validate_content("")
        self.assertIsNotNone(err)

    def test_rejects_whitespace_only(self) -> None:
        err, _ = self.handoff._validate_content("   \n\n  \t\n")
        self.assertIsNotNone(err)

    def test_rejects_too_short(self) -> None:
        err, _ = self.handoff._validate_content("short briefing\nline two\nline three")
        self.assertIsNotNone(err)
        assert err is not None
        self.assertIn("too short", err)

    def test_rejects_too_long(self) -> None:
        body = "x" * (self.handoff.MAX_CONTENT_LEN + 1)
        err, _ = self.handoff._validate_content(body)
        self.assertIsNotNone(err)
        assert err is not None
        self.assertIn("too long", err)

    def test_rejects_too_few_lines(self) -> None:
        # 220 chars on a single line.
        single = "x" * 220
        err, _ = self.handoff._validate_content(single)
        self.assertIsNotNone(err)
        assert err is not None
        self.assertIn("non-blank line", err)

    def test_accepts_good_content(self) -> None:
        err, normalised = self.handoff._validate_content(_good_content())
        self.assertIsNone(err)
        self.assertTrue(normalised.endswith("\n"))

    def test_normalises_trailing_newline(self) -> None:
        body = _good_content().rstrip("\n")
        _, normalised = self.handoff._validate_content(body)
        self.assertTrue(normalised.endswith("\n"))
        self.assertFalse(normalised.endswith("\n\n"))


# ---------------------------------------------------------------------------
# Default path
# ---------------------------------------------------------------------------


class TestDefaultPath(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp()
        self.handoff = _load_handoff(self.tmp)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_default_path_is_absolute(self) -> None:
        path = self.handoff._default_path()
        self.assertTrue(os.path.isabs(path), f"path not absolute: {path}")

    def test_default_path_lives_under_dot_fir(self) -> None:
        path = self.handoff._default_path()
        dot_fir = os.path.join(self.tmp, ".fir")
        self.assertTrue(
            path.startswith((os.path.realpath(dot_fir), dot_fir)),
            f"unexpected path: {path}",
        )

    def test_default_path_creates_dot_fir(self) -> None:
        self.handoff._default_path()
        self.assertTrue(os.path.isdir(os.path.join(self.tmp, ".fir")))


# ---------------------------------------------------------------------------
# Atomic write + verify_readable
# ---------------------------------------------------------------------------


class TestAtomicWriteAndVerify(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp()
        self.handoff = _load_handoff(self.tmp)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_write_then_verify(self) -> None:
        path = os.path.join(self.tmp, "h.md")
        self.handoff._atomic_write(path, "# hello\nbody body body\n")
        self.assertIsNone(self.handoff._verify_readable(path))
        with open(path) as f:
            self.assertIn("body body body", f.read())

    def test_verify_rejects_missing(self) -> None:
        msg = self.handoff._verify_readable(os.path.join(self.tmp, "nope.md"))
        self.assertIsNotNone(msg)
        assert msg is not None
        self.assertIn("not found", msg)

    def test_verify_rejects_directory(self) -> None:
        msg = self.handoff._verify_readable(self.tmp)
        self.assertIsNotNone(msg)
        assert msg is not None
        self.assertIn("not a regular file", msg)

    def test_verify_rejects_empty(self) -> None:
        path = os.path.join(self.tmp, "empty.md")
        open(path, "w").close()
        msg = self.handoff._verify_readable(path)
        self.assertIsNotNone(msg)
        assert msg is not None
        self.assertIn("empty", msg)


# ---------------------------------------------------------------------------
# End-to-end self_handoff handler
# ---------------------------------------------------------------------------


class TestSelfHandoffHandler(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp()
        self.handoff = _load_handoff(self.tmp)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_validation_failure_returns_error_no_restart(self) -> None:
        ctx = _StubContext()
        result = self.handoff.self_handoff({"content": "too short"}, ctx)
        self.assertTrue(result.get("is_error"))
        self.assertEqual(ctx.restart_calls, [])

    def test_missing_content_returns_error(self) -> None:
        ctx = _StubContext()
        result = self.handoff.self_handoff({}, ctx)
        self.assertTrue(result.get("is_error"))
        self.assertEqual(ctx.restart_calls, [])

    def test_happy_path_writes_file_and_calls_restart(self) -> None:
        ctx = _StubContext()
        result = self.handoff.self_handoff({"content": _good_content()}, ctx)
        self.assertFalse(result.get("is_error"), result)
        self.assertEqual(len(ctx.restart_calls), 1)
        prompt = ctx.restart_calls[0]
        # Prompt embeds a path that exists on disk.
        # Extract the path between "at " and " — ".
        marker = "at "
        idx = prompt.index(marker) + len(marker)
        end = prompt.index(" — ", idx)
        path = prompt[idx:end]
        self.assertTrue(os.path.isabs(path), f"prompt path not absolute: {path}")
        self.assertTrue(os.path.isfile(path), f"prompt path missing: {path}")
        with open(path) as f:
            body = f.read()
        # Post-strip equality: written body matches input modulo trailing nl.
        self.assertEqual(body.rstrip("\n"), _good_content().rstrip("\n"))

    def test_restart_failure_surfaces_as_tool_error(self) -> None:
        ctx = _StubContext(raise_on_restart=True)
        result = self.handoff.self_handoff({"content": _good_content()}, ctx)
        self.assertTrue(result.get("is_error"))
        text = result["content"][0]["text"]
        self.assertIn("restart_session failed", text)
        # The doc was still written; the error message should mention the path.
        self.assertIn(self.tmp, text)


if __name__ == "__main__":
    unittest.main()
