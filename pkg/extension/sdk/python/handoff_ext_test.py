#!/usr/bin/env python3
"""Subprocess-driven tests for .fir/extensions/handoff.py.

Mirrors the style of ``mood_ext_test.py``: spawns the extension as a
subprocess, mocks the fir-side bridge (read JSON-RPC on its stdout,
write replies on its stdin), and exercises the bookmark tool end-to-end
through the wire protocol.

Wrapped in ``unittest.TestCase`` so ``make test-python-sdk`` (which runs
``unittest discover -s pkg/extension/sdk/python -p '*_test.py'``) picks
the cases up automatically.

What this exercises:

* init handshake — ``bookmark`` + ``self_handoff`` tools and
  ``handoff`` command are advertised.
* exact-quote bookmark → file gets the entry with ``_bookmark_note``.
* partial-but-unique-quote bookmark → resolves to the right turn.
* decoded-text match (newline / JSON-escape) resolves correctly.
* ambiguous quote → resolves to the most recent match, count surfaced.
* quote with no match → tool error.
* multiple bookmark calls → file stays sorted by original transcript
  timestamp (not bookmark-call order).
* card slug/detail updated on every bookmark call (key="bookmarks",
  slug "<N> pinned", detail starts with the file path).
* ``self_handoff`` appends the bookmarks-file pointer line to its
  ``prepend_context`` when bookmarks exist; doesn't when empty.

Each test gets its own ``Bridge`` plus a temporary "transcript" file
that we control byte-for-byte.
"""

from __future__ import annotations

import contextlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import unittest

ROOT = os.path.dirname(
    os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))
)
EXT = os.path.join(ROOT, ".fir/extensions/handoff.py")
SDK = os.path.join(ROOT, "pkg/extension/sdk/python")


# ---------------------------------------------------------------------------
# Mock fir-side JSON-RPC bridge
# ---------------------------------------------------------------------------


class Bridge:
    """Minimal mock fir-side of the JSON-RPC channel.

    Exposes a fake transcript path / session id; auto-answers
    well-known bridge RPCs; queues incoming notifications and
    extension→bridge requests for assertions.
    """

    def __init__(self, transcript_path: str, session_id: str) -> None:
        env = {"PYTHONPATH": SDK, "PATH": "/usr/bin:/bin"}
        self.p = subprocess.Popen(
            [EXT],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=env,
        )
        # ty can't see through Popen's IO[bytes]|None typing.
        assert self.p.stdin is not None
        assert self.p.stdout is not None
        assert self.p.stderr is not None
        self._stdin = self.p.stdin
        self._stdout = self.p.stdout
        self._stderr_stream = self.p.stderr

        self.transcript_path = transcript_path
        self.session_id = session_id
        self.next_id = 1
        self.responses: dict[int, dict] = {}
        self.requests: list[dict] = []  # ext → us, with id
        self.restart_calls: list[dict] = []
        self.observable_writes: list[dict] = []

        self._stop = threading.Event()
        self._reader = threading.Thread(target=self._read_loop, daemon=True)
        self._stderr = threading.Thread(target=self._drain_stderr, daemon=True)
        self._reader.start()
        self._stderr.start()
        self.stderr_buf: list[str] = []

    def _drain_stderr(self) -> None:
        for line in self._stderr_stream:
            try:
                s = line.decode(errors="replace").rstrip()
            except Exception:  # noqa: S112 — test scaffolding
                continue
            self.stderr_buf.append(s)

    def _read_loop(self) -> None:
        while not self._stop.is_set():
            line = self._stdout.readline()
            if not line:
                return
            try:
                msg = json.loads(line.decode())
            except Exception:  # noqa: S112 — drop garbled frames
                continue
            if "id" in msg and "method" not in msg:
                self.responses[msg["id"]] = msg
            elif "id" in msg and "method" in msg:
                # ext → us request; auto-answer the well-known ones
                self.requests.append(msg)
                self._auto_answer(msg)
            # notifications (no id, no need to reply) — ignored.

    def _auto_answer(self, msg: dict) -> None:
        method = msg.get("method", "")
        mid = msg["id"]
        result: dict | None = None
        if method == "get_session_file":
            result = {"path": self.transcript_path}
        elif method == "get_session_id":
            result = {"id": self.session_id}
        elif method == "restart_session":
            self.restart_calls.append(msg.get("params") or {})
            result = {"ok": True}
        elif method == "put_observable":
            self.observable_writes.append(msg.get("params") or {})
            result = {"ok": True}
        elif method in (
            "get_session_name",
            "get_session_data",
            "set_session_data",
            "set_status",
            "notify",
            "send_user_message",
        ):
            # Handoff doesn't actually call most of these in the paths
            # we test, but the SDK may probe them during init / lifecycle
            # — return a benign success so nothing blocks.
            if method == "get_session_data":
                result = {"value": "", "ok": False}
            elif method == "get_session_name":
                result = {"name": ""}
            else:
                result = {"ok": True}
        else:
            self._send(
                {
                    "jsonrpc": "2.0",
                    "id": mid,
                    "error": {"code": -32601, "message": f"unknown {method}"},
                }
            )
            return
        self._send({"jsonrpc": "2.0", "id": mid, "result": result})

    def _send(self, msg: dict) -> None:
        self._stdin.write((json.dumps(msg) + "\n").encode())
        self._stdin.flush()

    def request(self, method: str, params: dict, timeout: float = 20.0) -> dict:
        mid = self.next_id
        self.next_id += 1
        self._send({"jsonrpc": "2.0", "id": mid, "method": method, "params": params})
        deadline = time.time() + timeout
        while time.time() < deadline:
            if mid in self.responses:
                return self.responses.pop(mid)
            time.sleep(0.01)
        raise TimeoutError(f"no response to {method} (id={mid})")

    def close(self) -> None:
        self._stop.set()
        try:
            self.p.terminate()
            self.p.wait(timeout=2)
        except Exception:
            self.p.kill()
        # Close pipes explicitly — Popen.terminate() doesn't and the
        # unclosed FDs trigger ResourceWarnings under Python 3.14's
        # gc-on-process-exit checks.
        for stream in (self._stdin, self._stdout, self._stderr_stream):
            with contextlib.suppress(Exception):
                stream.close()


def _text_of(result: dict) -> str:
    return "\n".join((b.get("text") or "") for b in (result.get("content") or []))


# ---------------------------------------------------------------------------
# Transcript fixture helpers
# ---------------------------------------------------------------------------

# Three user/assistant/tool-result turns we can search for. Timestamps
# are intentionally out-of-call-order so the sort test has something to
# do. Each entry is one JSONL line.


def _msg_entry(eid: str, ts: str, role: str, text: str, **extra) -> dict:
    """Build a realistic-looking SessionEntry (single-line JSONL)."""
    return {
        "type": "message",
        "id": eid,
        "parentId": "",
        "timestamp": ts,
        "message": {"role": role, "content": text},
        **extra,
    }


def _write_transcript(path: str, entries: list[dict]) -> None:
    """Write a header + entries to a JSONL transcript file."""
    header = {"id": "test-session", "model": "test-model"}
    with open(path, "wb") as f:
        f.write((json.dumps(header) + "\n").encode())
        for e in entries:
            f.write((json.dumps(e) + "\n").encode())


def _read_jsonl(path: str) -> list[dict]:
    with open(path, "rb") as f:
        out = []
        for line in f.read().splitlines():
            if not line.strip():
                continue
            out.append(json.loads(line))
        return out


# ---------------------------------------------------------------------------
# Test base — common setup
# ---------------------------------------------------------------------------


class _BookmarkBase(unittest.TestCase):
    """Spawn the extension + a fake transcript for each test."""

    SESSION_ID = "11111111-2222-3333-4444-555555555555"

    def setUp(self) -> None:
        self.tmpdir = tempfile.mkdtemp(prefix="handoff-bookmark-test-")
        self.transcript_path = os.path.join(
            self.tmpdir,
            f"20250522-100000_{self.SESSION_ID}.jsonl",
        )
        self.bookmarks_path = os.path.join(
            self.tmpdir,
            f"bookmarks-{self.SESSION_ID}.jsonl",
        )
        self._populate_transcript()
        self.bridge = Bridge(self.transcript_path, self.SESSION_ID)
        # Drive the init handshake so the SDK registers everything.
        self.init_result = self.bridge.request(
            "init",
            {"version": "1", "cwd": self.tmpdir, "config_dirs": []},
        )

    def tearDown(self) -> None:
        try:
            self.bridge.close()
        finally:
            shutil.rmtree(self.tmpdir, ignore_errors=True)

    def _populate_transcript(self) -> None:
        """Default transcript — three turns, ts out of bookmark-call order."""
        self.entries = [
            _msg_entry(
                "e-001", "2025-05-22T14:00:00", "user", "Use SQLite for the MVP, no Postgres."
            ),
            _msg_entry(
                "e-002",
                "2025-05-22T14:05:00",
                "assistant",
                "Acknowledged. Final DB schema: users(id, name).",
            ),
            _msg_entry("e-003", "2025-05-22T14:10:00", "user", "Skip auth entirely for the MVP."),
        ]
        _write_transcript(self.transcript_path, self.entries)

    # --- helpers --------------------------------------------------------

    def call_bookmark(self, quote: str, note: str, tool_call_id: str = "tc-1") -> dict:
        r = self.bridge.request(
            "tool_call",
            {
                "tool_call_id": tool_call_id,
                "name": "bookmark",
                "params": {"quote": quote, "note": note},
            },
        )
        return r["result"]

    def call_self_handoff(self, content: str, tool_call_id: str = "ho-1") -> dict:
        r = self.bridge.request(
            "tool_call",
            {
                "tool_call_id": tool_call_id,
                "name": "self_handoff",
                "params": {"content": content},
            },
        )
        return r["result"]


# ---------------------------------------------------------------------------
# Test cases
# ---------------------------------------------------------------------------


class TestBookmarkExcludesBookmarkCall(_BookmarkBase):
    """Regression: a quote must resolve to the real turn, not the call.

    Reproduces the original bug — the assistant turn that invokes
    bookmark() carries the quote verbatim in its tool-call arguments and
    is the newest transcript entry, so an unguarded reverse-scan always
    self-matched the call instead of the earlier turn the model meant to
    pin. The scanner now skips bookmark-call turns.
    """

    QUOTE = "msgs = agent.StripUnmatchedToolCalls(msgs)"

    def _populate_transcript(self) -> None:
        self.entries = [
            # The real turn the model wants to pin (a tool result).
            _msg_entry(
                "e-real",
                "2025-05-22T14:00:00",
                "assistant",
                f"Applied the fix:\n{self.QUOTE}\nbuild is green.",
            ),
            # A *later* assistant turn that is itself a bookmark call,
            # carrying the same quote in its arguments — the trap.
            {
                "type": "message",
                "id": "e-bmcall",
                "parentId": "",
                "timestamp": "2025-05-22T14:09:00",
                "message": {
                    "role": "assistant",
                    "content": [
                        {
                            "type": "toolCall",
                            "name": "bookmark",
                            "arguments": {"quote": self.QUOTE, "note": "the fix"},
                        },
                    ],
                },
            },
        ]
        _write_transcript(self.transcript_path, self.entries)

    def test_resolves_to_real_turn_not_the_bookmark_call(self) -> None:
        r = self.call_bookmark(self.QUOTE, "the fix")
        self.assertFalse(r.get("is_error"), r)
        rows = _read_jsonl(self.bookmarks_path)
        self.assertEqual(len(rows), 1)
        # Must be the substantive turn, never the bookmark-call turn.
        self.assertEqual(rows[0]["id"], "e-real")
        self.assertEqual(rows[0]["_bookmark_note"], "the fix")
        # The bookmark call is excluded from the match count too.
        self.assertIn("matches=1", _text_of(r))


class TestInit(_BookmarkBase):
    def test_handshake_advertises_bookmark_and_self_handoff(self) -> None:
        result = self.init_result["result"]
        tool_names = [t["name"] for t in result.get("tools", [])]
        self.assertIn("bookmark", tool_names)
        self.assertIn("self_handoff", tool_names)

    def test_handshake_advertises_handoff_command(self) -> None:
        result = self.init_result["result"]
        cmd_names = [c["name"] for c in result.get("commands", [])]
        self.assertIn("handoff", cmd_names)

    def test_handshake_subscribes_session_start(self) -> None:
        # We subscribe to session_start to run the one-time migration
        # that repairs pre-existing (v1) bookmarks files whose entries
        # self-matched the bookmark call. The card itself still survives
        # /reexec via the cards file read on session construct.
        result = self.init_result["result"]
        events = result.get("events", [])
        self.assertEqual(events, ["session_start"])

    def test_bookmark_description_mentions_all_turn_types(self) -> None:
        result = self.init_result["result"]
        spec = next(t for t in result.get("tools", []) if t["name"] == "bookmark")
        desc = spec.get("description", "")
        # Must explicitly cover every turn kind so the model knows the
        # search is unscoped.
        for needle in ("user message", "assistant message", "tool call", "tool result"):
            self.assertIn(
                needle,
                desc.lower(),
                f"bookmark description must mention {needle!r}: got {desc[:300]!r}",
            )


class TestBookmarkExactQuote(_BookmarkBase):
    def test_exact_quote_writes_entry_with_note(self) -> None:
        r = self.call_bookmark(
            "Final DB schema: users(id, name).",
            "final DB schema",
        )
        self.assertFalse(r.get("is_error"), r)
        self.assertTrue(os.path.exists(self.bookmarks_path))
        rows = _read_jsonl(self.bookmarks_path)
        self.assertEqual(len(rows), 1)
        row = rows[0]
        # The entire turn entry is preserved as-is.
        self.assertEqual(row["id"], "e-002")
        self.assertEqual(row["timestamp"], "2025-05-22T14:05:00")
        self.assertEqual(row["message"]["role"], "assistant")
        # Only the note is injected; the rest of the turn entry remains
        # the transcript object as-is.
        self.assertEqual(row["_bookmark_note"], "final DB schema")
        self.assertNotIn("_bookmark_ts", row)
        self.assertNotIn("_bookmark_entry_id", row)


class TestBookmarkPartialQuote(_BookmarkBase):
    def test_short_unique_substring_resolves_to_right_turn(self) -> None:
        # "Skip auth" appears only in e-003.
        r = self.call_bookmark("Skip auth", "user constraint: no auth")
        self.assertFalse(r.get("is_error"), r)
        rows = _read_jsonl(self.bookmarks_path)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["id"], "e-003")
        self.assertEqual(rows[0]["_bookmark_note"], "user constraint: no auth")


class TestBookmarkDecodedQuote(_BookmarkBase):
    """Quotes copied from model context match decoded turn text.

    Go's encoding/json escapes ``<``, ``>``, ``&``, and embedded
    newlines. A quote like ``use <tag> now`` (copied from what the
    model sees in its context) is NOT a substring of the raw JSONL
    bytes, but it IS a substring of the decoded ``content`` field.
    The bookmark scan walks decoded fields, so this just works.
    """

    def _populate_transcript(self) -> None:
        self.entries = [
            _msg_entry("e-html", "2025-05-22T15:00:00", "user", "use <tag> now"),
            _msg_entry("e-nl", "2025-05-22T15:05:00", "assistant", "line one\nline two"),
        ]
        _write_transcript(self.transcript_path, self.entries)

    def test_quote_with_html_chars_matches(self) -> None:
        r = self.call_bookmark("use <tag> now", "html sample")
        self.assertFalse(r.get("is_error"), r)
        rows = _read_jsonl(self.bookmarks_path)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["id"], "e-html")

    def test_quote_with_embedded_newline_matches(self) -> None:
        r = self.call_bookmark("line one\nline two", "multiline sample")
        self.assertFalse(r.get("is_error"), r)
        rows = _read_jsonl(self.bookmarks_path)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["id"], "e-nl")


class TestBookmarkAmbiguousQuote(_BookmarkBase):
    def _populate_transcript(self) -> None:
        # Three turns that all share the substring "MVP".
        self.entries = [
            _msg_entry("e-a", "2025-05-22T13:00:00", "user", "MVP iteration one."),
            _msg_entry("e-b", "2025-05-22T13:10:00", "assistant", "MVP iteration two."),
            _msg_entry("e-c", "2025-05-22T13:20:00", "user", "MVP iteration three."),
        ]
        _write_transcript(self.transcript_path, self.entries)

    def test_ambiguous_quote_resolves_to_most_recent_match(self) -> None:
        r = self.call_bookmark("MVP iteration", "scope marker")
        self.assertFalse(r.get("is_error"), r)
        rows = _read_jsonl(self.bookmarks_path)
        self.assertEqual(len(rows), 1)
        # Most recent matching line wins — e-c, the third turn.
        self.assertEqual(rows[0]["id"], "e-c")
        # The result message surfaces the match count so the model can
        # refine if it actually wanted an older turn.
        text = _text_of(r)
        self.assertIn("matches=3", text)


class TestBookmarkNoMatch(_BookmarkBase):
    def test_unmatched_quote_returns_tool_error(self) -> None:
        r = self.call_bookmark("this phrase appears nowhere", "ignored")
        self.assertTrue(r.get("is_error"), r)
        self.assertFalse(
            os.path.exists(self.bookmarks_path), "no bookmarks file should be created on miss"
        )
        # The error tells the model how to recover.
        text = _text_of(r)
        self.assertIn("not found", text.lower())


class TestBookmarkSortsByOriginalTs(_BookmarkBase):
    def _populate_transcript(self) -> None:
        # Timestamps assigned so that bookmark-call order != ts order.
        self.entries = [
            _msg_entry("e-x", "2025-05-22T16:00:00", "user", "AAA later but bookmarked first"),
            _msg_entry("e-y", "2025-05-22T14:00:00", "user", "BBB earlier but bookmarked second"),
            _msg_entry("e-z", "2025-05-22T15:00:00", "user", "CCC middle, bookmarked third"),
        ]
        _write_transcript(self.transcript_path, self.entries)

    def test_file_stays_sorted_by_original_timestamp(self) -> None:
        # Call order: AAA, BBB, CCC. Timestamps: 16, 14, 15.
        # On-disk order should be: BBB, CCC, AAA.
        self.call_bookmark("AAA", "first call", tool_call_id="tc-A")
        self.call_bookmark("BBB", "second call", tool_call_id="tc-B")
        self.call_bookmark("CCC", "third call", tool_call_id="tc-C")
        rows = _read_jsonl(self.bookmarks_path)
        self.assertEqual([r["id"] for r in rows], ["e-y", "e-z", "e-x"])
        # The injected notes follow the entries.
        self.assertEqual(
            [r["_bookmark_note"] for r in rows], ["second call", "third call", "first call"]
        )


class TestCardOnEveryBookmark(_BookmarkBase):
    def test_card_updates_with_count_path_and_recent_notes(self) -> None:
        self.call_bookmark("Final DB schema", "final DB schema", tool_call_id="tc-1")
        # First put_observable: 1 pinned.
        ow = self.bridge.observable_writes
        self.assertGreater(len(ow), 0)
        first = ow[-1]
        self.assertEqual(first["key"], "bookmarks")
        self.assertEqual(first["slug"], "1 pinned")
        self.assertIn(self.bookmarks_path, first["detail"])
        self.assertIn("final DB schema", first["detail"])
        # The detail line includes the HH:MM time prefix of the
        # original turn (14:05).
        self.assertIn("14:05", first["detail"])

        # Second bookmark: count bumps and slug reflects it.
        self.call_bookmark("Skip auth", "skip auth for MVP", tool_call_id="tc-2")
        second = self.bridge.observable_writes[-1]
        self.assertEqual(second["key"], "bookmarks")
        self.assertEqual(second["slug"], "2 pinned")
        self.assertIn("skip auth for MVP", second["detail"])
        self.assertIn("final DB schema", second["detail"])

    def test_card_slug_never_exceeds_24_chars(self) -> None:
        # Pin a bunch so the count grows — slug stays "<N> pinned".
        for i in range(15):
            ts = f"2025-05-22T15:{i:02d}:00"
            self.entries.append(_msg_entry(f"e-x-{i}", ts, "user", f"line marker XYZ-{i}"))
        _write_transcript(self.transcript_path, self.entries)
        for i in range(12):
            r = self.call_bookmark(f"line marker XYZ-{i}", f"n{i}", tool_call_id=f"tc-{i}")
            self.assertFalse(r.get("is_error"), r)
        last = self.bridge.observable_writes[-1]
        self.assertLessEqual(len(last["slug"]), 24, last)


class TestSelfHandoffPointer(_BookmarkBase):
    GOOD = (
        "# Self-Handoff\n\n"
        "## Context\n"
        "Working on the bookmark continuity feature. Implementation in "
        "pkg/resources/builtin_extensions/handoff.py; design in "
        "docs/design/handoff-bookmarks.md.\n\n"
        "## Next\n"
        "Run make lint test; commit and merge.\n"
    )

    def test_no_bookmarks_no_pointer_in_prepend_context(self) -> None:
        r = self.call_self_handoff(self.GOOD)
        self.assertFalse(r.get("is_error"), r)
        self.assertEqual(len(self.bridge.restart_calls), 1)
        prepend = self.bridge.restart_calls[0].get("prepend_context", "")
        # Briefing carried through with at most a trailing-newline tweak.
        self.assertEqual(prepend.rstrip("\n"), self.GOOD.rstrip("\n"))
        self.assertNotIn("Bookmarks from parent session", prepend)

    def test_existing_bookmarks_appends_pointer_line(self) -> None:
        # Drop one bookmark so the file is non-empty.
        b = self.call_bookmark("Skip auth", "skip auth")
        self.assertFalse(b.get("is_error"), b)
        # File is on disk; now handoff.
        r = self.call_self_handoff(self.GOOD, tool_call_id="ho-2")
        self.assertFalse(r.get("is_error"), r)
        self.assertEqual(len(self.bridge.restart_calls), 1)
        prepend = self.bridge.restart_calls[0].get("prepend_context", "")
        self.assertIn("Bookmarks from parent session", prepend)
        # Pointer uses the absolute path so the child can read directly.
        self.assertIn(self.bookmarks_path, prepend)
        # Pointer lands AFTER the briefing body, not before.
        body_idx = prepend.index("Working on the bookmark continuity")
        ptr_idx = prepend.index("Bookmarks from parent session")
        self.assertLess(body_idx, ptr_idx)

    def test_empty_bookmarks_file_does_not_trigger_pointer(self) -> None:
        # Touch an empty bookmarks file; the pointer logic must check
        # for *content*, not just existence.
        open(self.bookmarks_path, "wb").close()
        r = self.call_self_handoff(self.GOOD)
        self.assertFalse(r.get("is_error"), r)
        prepend = self.bridge.restart_calls[0].get("prepend_context", "")
        self.assertNotIn("Bookmarks from parent session", prepend)


# ---------------------------------------------------------------------------
# Main hook for manual ad-hoc runs (matches mood_ext_test.py affordance).
# `unittest discover` picks up the TestCase classes above automatically.
# ---------------------------------------------------------------------------


if __name__ == "__main__":
    sys.exit(0 if unittest.main(exit=False).result.wasSuccessful() else 1)
