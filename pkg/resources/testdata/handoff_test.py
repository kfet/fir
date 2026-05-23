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

    def __init__(
        self,
        raise_on_restart: bool = False,
        session_file: str = "",
        session_id: str = "",
    ):
        self.restart_calls: list[tuple[str, str]] = []
        self.user_messages: list[str] = []
        self.raise_on_restart = raise_on_restart
        self.observable_writes: list[tuple[str, str, str]] = []
        self._session_file = session_file
        self._session_id = session_id

    def restart_session(self, prompt: str, prepend_context: str = "") -> None:
        if self.raise_on_restart:
            raise RuntimeError("simulated restart failure")
        self.restart_calls.append((prompt, prepend_context))

    def send_user_message(self, content: str, deliver_as: str | None = None) -> None:
        del deliver_as
        self.user_messages.append(content)

    def get_session_file(self) -> str:
        return self._session_file

    def get_session_id(self) -> str:
        return self._session_id

    def put_observable(self, key: str, slug: str, detail: str = "") -> None:
        self.observable_writes.append((key, slug, detail))


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


# ---------------------------------------------------------------------------
# Bookmark helpers — pure-function unit tests
# ---------------------------------------------------------------------------


def _write_jsonl(path: str, rows: list[dict]) -> None:
    import json
    with open(path, "wb") as f:
        for row in rows:
            f.write((json.dumps(row) + "\n").encode())


class TestBookmarksPath(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp()
        self.handoff = _load_handoff(self.tmp)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_path_is_sibling_of_transcript(self) -> None:
        session_file = os.path.join(self.tmp, "20250522-100000_abc-xyz.jsonl")
        ctx = _StubContext(session_file=session_file, session_id="abc-xyz")
        got = self.handoff._bookmarks_path(ctx)
        self.assertEqual(got, os.path.join(self.tmp, "bookmarks-abc-xyz.jsonl"))

    def test_no_session_file_returns_empty_string(self) -> None:
        ctx = _StubContext(session_file="", session_id="abc")
        self.assertEqual(self.handoff._bookmarks_path(ctx), "")


class TestFindTurnByQuote(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp()
        self.handoff = _load_handoff(self.tmp)
        self.path = os.path.join(self.tmp, "transcript.jsonl")
        # Header first (no `type` field — must be skipped).
        _write_jsonl(self.path, [
            {"id": "test-session", "model": "test"},
            {"type": "message", "id": "e1", "timestamp": "2025-05-22T14:00:00",
             "message": {"role": "user", "content": "alpha bravo charlie"}},
            {"type": "message", "id": "e2", "timestamp": "2025-05-22T14:05:00",
             "message": {"role": "assistant", "content": "alpha delta"}},
            {"type": "message", "id": "e3", "timestamp": "2025-05-22T14:10:00",
             "message": {"role": "user", "content": "epsilon zeta"}},
            {"type": "message", "id": "e4", "timestamp": "2025-05-22T14:15:00",
             "message": {"role": "assistant", "content": "line one\nline two"}},
        ])
        # Go's encoding/json escapes HTML-sensitive bytes by default;
        # a quote copied from context contains "<tag>", not the raw
        # "\\u003ctag\\u003e" bytes in the transcript. Append one such
        # line manually so the decoded-field search path is exercised.
        with open(self.path, "ab") as f:
            f.write(
                b'{"type":"message","id":"e5",'
                b'"timestamp":"2025-05-22T14:20:00",'
                b'"message":{"role":"user",'
                b'"content":"use \\u003ctag\\u003e now"}}\n'
            )

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_exact_match_returns_entry(self) -> None:
        entry, count = self.handoff._find_turn_by_quote(
            self.path, "epsilon zeta")
        assert entry is not None
        self.assertEqual(entry["id"], "e3")
        self.assertEqual(count, 1)

    def test_ambiguous_match_returns_most_recent(self) -> None:
        entry, count = self.handoff._find_turn_by_quote(self.path, "alpha")
        assert entry is not None
        # e2 is more recent than e1.
        self.assertEqual(entry["id"], "e2")
        self.assertEqual(count, 2)

    def test_decoded_newline_match_returns_entry(self) -> None:
        entry, count = self.handoff._find_turn_by_quote(
            self.path, "line one\nline two")
        assert entry is not None
        self.assertEqual(entry["id"], "e4")
        self.assertEqual(count, 1)

    def test_decoded_go_html_escape_match_returns_entry(self) -> None:
        entry, count = self.handoff._find_turn_by_quote(
            self.path, "use <tag> now")
        assert entry is not None
        self.assertEqual(entry["id"], "e5")
        self.assertEqual(count, 1)

    def test_no_match_returns_none(self) -> None:
        entry, count = self.handoff._find_turn_by_quote(
            self.path, "nonexistent substring")
        self.assertIsNone(entry)
        self.assertEqual(count, 0)

    def test_missing_file_returns_none(self) -> None:
        entry, count = self.handoff._find_turn_by_quote(
            os.path.join(self.tmp, "does-not-exist.jsonl"), "anything")
        self.assertIsNone(entry)
        self.assertEqual(count, 0)

    def test_header_line_is_skipped(self) -> None:
        # The header has neither `type` nor a message; substring match on
        # "test-session" should find nothing bookmarkable.
        entry, count = self.handoff._find_turn_by_quote(
            self.path, "test-session")
        self.assertIsNone(entry)
        self.assertEqual(count, 0)


class TestRenderCard(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp()
        self.handoff = _load_handoff(self.tmp)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_zero_bookmarks(self) -> None:
        slug, detail = self.handoff._render_card("/p/bookmarks.jsonl", [])
        self.assertEqual(slug, "0 pinned")
        self.assertIn("/p/bookmarks.jsonl", detail)

    def test_singular_phrasing(self) -> None:
        rows = [{
            "id": "e1", "timestamp": "2025-05-22T14:32:00",
            "_bookmark_note": "schema",
        }]
        slug, detail = self.handoff._render_card("/p/b.jsonl", rows)
        self.assertEqual(slug, "1 pinned")
        self.assertIn("1 bookmark (", detail)  # singular, not "bookmarks"
        self.assertIn("14:32  schema", detail)

    def test_multiple_bookmarks_include_path_and_time_prefix(self) -> None:
        rows = [
            {"id": "e1", "timestamp": "2025-05-22T14:32:00",
             "_bookmark_note": "schema"},
            {"id": "e2", "timestamp": "2025-05-22T14:45:00",
             "_bookmark_note": "skip auth"},
        ]
        slug, detail = self.handoff._render_card("/p/b.jsonl", rows)
        self.assertEqual(slug, "2 pinned")
        self.assertIn("2 bookmarks (/p/b.jsonl):", detail)
        self.assertIn("14:32  schema", detail)
        self.assertIn("14:45  skip auth", detail)

    def test_slug_never_exceeds_24_chars(self) -> None:
        rows = [{"_bookmark_note": f"n{i}",
                 "timestamp": f"2025-05-22T14:{i:02d}:00", "id": f"e{i}"}
                for i in range(999)]
        slug, _detail = self.handoff._render_card("/p/b.jsonl", rows)
        self.assertLessEqual(len(slug), 24, slug)


class TestEntrySortKey(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp()
        self.handoff = _load_handoff(self.tmp)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_primary_key_is_original_timestamp(self) -> None:
        a = {"id": "a", "timestamp": "2025-05-22T14:00:00"}
        b = {"id": "b", "timestamp": "2025-05-22T13:00:00"}
        # Sorting [a, b] ascending should put b first.
        got = sorted([a, b], key=self.handoff._entry_sort_key)
        self.assertEqual([e["id"] for e in got], ["b", "a"])

    def test_id_breaks_ties(self) -> None:
        a = {"id": "alpha", "timestamp": "2025-05-22T14:00:00"}
        b = {"id": "bravo", "timestamp": "2025-05-22T14:00:00"}
        got = sorted([b, a], key=self.handoff._entry_sort_key)
        self.assertEqual([e["id"] for e in got], ["alpha", "bravo"])


class TestSelfHandoffPointerStub(unittest.TestCase):
    """Stub-context coverage for the pointer-line append in self_handoff."""

    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp()
        self.handoff = _load_handoff(self.tmp)
        self.session_file = os.path.join(self.tmp, "transcript.jsonl")
        self.bookmarks = os.path.join(self.tmp, "bookmarks-sid.jsonl")
        # Minimal transcript so any quote scan that happens during
        # self_handoff (it shouldn't) doesn't crash.
        _write_jsonl(self.session_file, [
            {"id": "h", "model": "m"},
            {"type": "message", "id": "e1", "timestamp": "2025-05-22T14:00:00",
             "message": {"role": "user", "content": "hi"}},
        ])

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def _good(self) -> str:
        return _good_content()

    def test_no_bookmarks_file_no_pointer(self) -> None:
        ctx = _StubContext(session_file=self.session_file, session_id="sid")
        self.handoff.self_handoff({"content": self._good()}, ctx)
        prepend = ctx.restart_calls[0][1]
        self.assertNotIn("Bookmarks from parent session", prepend)

    def test_empty_file_no_pointer(self) -> None:
        open(self.bookmarks, "wb").close()
        ctx = _StubContext(session_file=self.session_file, session_id="sid")
        self.handoff.self_handoff({"content": self._good()}, ctx)
        prepend = ctx.restart_calls[0][1]
        self.assertNotIn("Bookmarks from parent session", prepend)

    def test_non_empty_file_appends_pointer_with_absolute_path(self) -> None:
        _write_jsonl(self.bookmarks, [{
            "type": "message", "id": "e1",
            "timestamp": "2025-05-22T14:00:00",
            "message": {"role": "user", "content": "hi"},
            "_bookmark_note": "the first turn",
        }])
        ctx = _StubContext(session_file=self.session_file, session_id="sid")
        self.handoff.self_handoff({"content": self._good()}, ctx)
        prepend = ctx.restart_calls[0][1]
        self.assertIn("Bookmarks from parent session", prepend)
        self.assertIn(self.bookmarks, prepend)
        # Briefing body still present and ahead of the pointer.
        self.assertIn("Self-Handoff", prepend)
        self.assertLess(
            prepend.index("Self-Handoff"),
            prepend.index("Bookmarks from parent session"),
        )


if __name__ == "__main__":
    unittest.main()
