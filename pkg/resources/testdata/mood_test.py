#!/usr/bin/env python3
"""Tests for the mood builtin extension: bets, surprise, lessons, recall."""

import json
import os
import sys
import tempfile
import unittest
from unittest import mock

_here = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_here, "..", "builtin_extensions"))
sys.path.insert(0, os.path.join(_here, "..", "..", "extension", "sdk", "python"))

import fir_ext


def _load_mood():
    sys.modules.pop("mood", None)
    for reg in (
        fir_ext._tools,
        fir_ext._tool_handlers,
        fir_ext._event_handlers,
        fir_ext._hook_handlers,
        fir_ext._commands,
        fir_ext._command_handlers,
    ):
        reg.clear()
    with mock.patch.object(fir_ext, "run"):
        import mood
    return mood


class FakeCtx:
    def __init__(self, provider="anthropic", model="claude-opus-5-5", session="s1"):
        self.data = {}
        self.prepended = []
        self.provider, self.model, self.session = provider, model, session
        self.tool_call_id = ""

    def get_session_data(self, k):
        return self.data.get(k)

    def set_session_data(self, k, v):
        self.data[k] = v

    def agent_info(self, timeout=5.0):
        return {
            "model": {"id": self.model, "provider": self.provider},
            "session": {"id": self.session},
        }

    def prepend(self, content):
        self.prepended.append(content)

    def put_observable(self, *a, **k):
        pass

    def set_status(self, *a):
        pass


def _text(r):
    return "\n".join(b["text"] for b in r["content"])


class MoodTest(unittest.TestCase):
    def setUp(self):
        self.mood = _load_mood()
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        p = mock.patch.object(fir_ext, "config_dirs", [self.tmp.name])
        p.start()
        self.addCleanup(p.stop)

    def _bet(self, ctx, conf=0.9, anchor="a.go:1", **extra):
        params = {"note": "n", "bet": "x holds", "conf": conf, "anchor": anchor}
        params.update(extra)
        r = self.mood.mood_note(params, ctx)
        return _text(r).split()[1]

    def test_plain_note_unchanged_and_records_model(self):
        ctx = FakeCtx()
        r = self.mood.mood_note({"note": "fine", "tag": "steady"}, ctx)
        self.assertFalse(r["is_error"])
        e = json.loads(ctx.data["mood_log"])[0]
        self.assertEqual(e["model"], "anthropic/claude-opus-5-5")
        self.assertNotIn("bet", e)
        self.assertRegex(e["id"], r"^m[0-9a-f]{5}$")

    def test_surprise_computed_not_settable(self):
        ctx = FakeCtx()
        eid = self._bet(ctx, conf=0.9, surprise=0.0)
        r = self.mood.mood_outcome(
            {"id": eid, "outcome": "failed", "correct": 0.1, "surprise": 0.0}, ctx
        )
        self.assertFalse(r["is_error"], _text(r))
        e = json.loads(ctx.data["mood_log"])[0]
        self.assertAlmostEqual(e["surprise"], 0.8)
        # Not a schema param on either tool.
        for name in ("mood_note", "mood_outcome"):
            props = next(t for t in fir_ext._tools if t["name"] == name)["parameters"]["properties"]
            self.assertNotIn("surprise", props)
        # Can't resolve twice.
        self.assertTrue(
            self.mood.mood_outcome({"id": eid, "outcome": "x", "correct": 1}, ctx)["is_error"]
        )
        recent = _text(self.mood.mood_recent({}, ctx))
        self.assertIn("surprise 0.80", recent)

    def test_model_map_grows(self):
        a = FakeCtx()
        self.mood.lesson_add({"rule": "r", "bet": "b", "anchor_pattern": "p"}, a)
        self.mood.lesson_score({"id": "L1", "hit": True}, a)
        b = FakeCtx(provider="openrouter", model="openai/gpt-5.2")
        self.mood.lesson_score({"id": "L1", "hit": False}, b)
        self.mood.lesson_score({"id": "L1", "hit": True}, a)
        models = self.mood._read_jsonl("lessons.jsonl")[0]["models"]
        self.assertEqual(models["anthropic/claude-opus-5-5"], {"hits": 2, "misses": 0})
        self.assertEqual(models["openrouter/openai/gpt-5.2"], {"hits": 0, "misses": 1})

    def test_session_start_injects_nothing_without_lessons(self):
        ctx = FakeCtx()
        self.mood.on_session_start({}, ctx)
        self.assertEqual(ctx.prepended, [])

    def test_session_start_marks(self):
        a = FakeCtx()
        self.mood.lesson_add({"rule": "strong one", "bet": "b", "anchor_pattern": "p"}, a)
        self.mood.lesson_add({"rule": "weak one", "bet": "b", "anchor_pattern": "q"}, a)
        self.mood.lesson_add({"rule": "other model", "bet": "b", "anchor_pattern": "r"}, a)
        b = FakeCtx(provider="openai", model="gpt-6")
        for _ in range(3):
            self.mood.lesson_score({"id": "L1", "hit": True}, a)
        self.mood.lesson_score({"id": "L1", "hit": True}, b)
        self.mood.lesson_score({"id": "L2", "hit": True}, a)
        self.mood.lesson_score({"id": "L3", "hit": True}, b)
        self.mood.on_session_start({}, a)
        out = a.prepended[0]
        self.assertIn("LESSONS", out)
        lines = out.splitlines()
        self.assertTrue(lines[1].startswith("L1"))  # strongest first
        self.assertIn("[strong]", out)
        self.assertIn("[weak - probe it]", out)
        self.assertIn("[unverified on this model - probe it]", out)

    def test_promotion_requires_distinct_sessions(self):
        for _ in range(3):
            ctx = FakeCtx(session="same")
            eid = self._bet(ctx, anchor="cfg.x")
            self.mood.mood_outcome({"id": eid, "outcome": "no", "correct": 0}, ctx)
        self.assertIsNone(self.mood._promote())
        for s in ("s2", "s3"):
            ctx = FakeCtx(session=s)
            eid = self._bet(ctx, anchor="cfg.x")
            self.mood.mood_outcome({"id": eid, "outcome": "no", "correct": 0}, ctx)
        self.assertEqual(self.mood._promote(), "L1")
        self.assertIsNone(self.mood._promote())  # consumed
        ls_ = self.mood._read_jsonl("lessons.jsonl")[0]
        self.assertEqual((ls_["state"], ls_["anchor_pattern"]), ("proposed", "cfg.x"))

    def test_low_surprise_not_ledgered(self):
        ctx = FakeCtx()
        eid = self._bet(ctx, conf=0.9)
        self.mood.mood_outcome({"id": eid, "outcome": "ok", "correct": 1}, ctx)
        self.assertEqual(self.mood._read_jsonl("bets.jsonl"), [])


if __name__ == "__main__":
    unittest.main()
