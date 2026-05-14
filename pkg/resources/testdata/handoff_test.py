#!/usr/bin/env python3
"""Tests for the handoff builtin extension.

Covers the validation logic in ``handoff.py``:

* content type / emptiness / length floor / length ceiling / line count;
* normalisation (trailing newline);
* end-to-end self_handoff handler calls restart_session with the briefing
  in ``prepend_context`` and a short natural prompt;
* the /handoff slash command injects a user-message asking the agent to
  write a briefing.

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
    state.
    """
    if "handoff" in sys.modules:
        del sys.modules["handoff"]
    fir_ext.cwd = cwd
    with mock.patch.object(fir_ext, "run"):
        import handoff  # type: ignore[import-not-found]
    return handoff


class _StubContext:
    """Minimal stub of fir_ext.Context used by the tool handler.

    Captures restart_session calls (both prompt and prepend_context);
    everything else either no-ops or raises (so accidental usage during
    validation is surfaced).
    """

    def __init__(self, raise_on_restart: bool = False):
        self.restart_calls: list[tuple[str, str]] = []
        self.user_messages: list[str] = []
        self.raise_on_restart = raise_on_restart

    def restart_session(self, prompt: str, prepend_context: str = "") -> None:
        if self.raise_on_restart:
            raise RuntimeError("simulated restart failure")
        self.restart_calls.append((prompt, prepend_context))

    def send_user_message(self, content: str, deliver_as: str | None = None) -> None:
        del deliver_as
        self.user_messages.append(content)


def _good_content() -> str:
    """A content body that satisfies every validation rule.

    >=200 chars after strip, >=3 non-blank lines, <=64 KB.
    """
    return (
        "# Self-Handoff\n"
        "\n"
        "## Context\n"
        "Working on the reliable-self-handoff branch. The handoff extension "
        "carries the briefing in-context via restart_session's "
        "prepend_context, no filesystem artifact is written.\n"
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

    def test_happy_path_passes_briefing_via_prepend_context(self) -> None:
        ctx = _StubContext()
        good = _good_content()
        result = self.handoff.self_handoff({"content": good}, ctx)
        self.assertFalse(result.get("is_error"), result)
        self.assertEqual(len(ctx.restart_calls), 1)
        prompt, prepend = ctx.restart_calls[0]
        # Prompt is short and natural.
        self.assertTrue(0 < len(prompt) < 100, f"unexpected prompt: {prompt!r}")
        self.assertIn("handoff", prompt.lower())
        # Briefing travels in prepend_context, modulo trailing-newline
        # normalisation done by _validate_content.
        self.assertEqual(prepend.rstrip("\n"), good.rstrip("\n"))

    def test_no_filesystem_artifact_written(self) -> None:
        """Handoff must not create .fir/ or any file in the cwd."""
        ctx = _StubContext()
        self.handoff.self_handoff({"content": _good_content()}, ctx)
        # No .fir directory should be created by the handoff.
        self.assertFalse(
            os.path.exists(os.path.join(self.tmp, ".fir")),
            "handoff must not create .fir/ in cwd",
        )
        # Cwd should remain empty.
        self.assertEqual(os.listdir(self.tmp), [])

    def test_restart_failure_surfaces_as_tool_error(self) -> None:
        ctx = _StubContext(raise_on_restart=True)
        result = self.handoff.self_handoff({"content": _good_content()}, ctx)
        self.assertTrue(result.get("is_error"))
        text = result["content"][0]["text"]
        self.assertIn("restart_session failed", text)


# ---------------------------------------------------------------------------
# /handoff slash command
# ---------------------------------------------------------------------------


class TestSlashCommand(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp()
        self.handoff = _load_handoff(self.tmp)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_no_args_injects_briefing_prompt(self) -> None:
        ctx = _StubContext()
        result = self.handoff.cmd_handoff([], ctx)
        self.assertIn("message", result)
        self.assertEqual(len(ctx.user_messages), 1)
        msg = ctx.user_messages[0]
        self.assertIn("self_handoff", msg)
        self.assertIn("/handoff", msg)
        self.assertNotIn("Additional focus", msg)

    def test_args_appended_as_focus_hints(self) -> None:
        ctx = _StubContext()
        self.handoff.cmd_handoff(["focus", "on", "the", "parser"], ctx)
        self.assertEqual(len(ctx.user_messages), 1)
        self.assertIn("Additional focus", ctx.user_messages[0])
        self.assertIn("focus on the parser", ctx.user_messages[0])

    def test_does_not_call_restart_or_write(self) -> None:
        ctx = _StubContext()
        self.handoff.cmd_handoff([], ctx)
        self.assertEqual(ctx.restart_calls, [])
        self.assertEqual(os.listdir(self.tmp), [])


if __name__ == "__main__":
    unittest.main()
