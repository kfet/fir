#!/usr/bin/env python3
"""Tests for the ctx (context provenance) builtin extension."""

import importlib
import json
import os
import sys
import tempfile
import unittest
from unittest import mock

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(_HERE, "..", "builtin_extensions"))
sys.path.insert(0, os.path.join(_HERE, "..", "..", "extension", "sdk", "python"))

import fir_ext


def _load():
    sys.modules.pop("ctx", None)
    with mock.patch.object(fir_ext, "run"):
        importlib.import_module("ctx")
    return sys.modules["ctx"]


def _src(turn, addr, source, owner, tokens):
    return {
        "kind": "src",
        "turn": turn,
        "addr": addr,
        "source": source,
        "owner": owner,
        "tokens": tokens,
        "hash": addr * 2,
    }


def _turn(turn, addrs, model="p/m", tokens=1000):
    return {"kind": "turn", "turn": turn, "model": model, "tokens": tokens, "addrs": addrs}


class FakeCtx:
    def __init__(self, session_file, sid):
        self.session_file = session_file
        self.sid = sid
        self.cards = []
        self.prepended = []

    def get_session_file(self):
        return self.session_file

    def get_session_id(self):
        return self.sid

    def put_observable(self, key, slug, detail=""):
        self.cards.append((key, slug, detail))

    def prepend(self, content):
        self.prepended.append(content)


class CtxTest(unittest.TestCase):
    def setUp(self):
        self.mod = _load()
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = self.tmp.name
        self.sdir = os.path.join(self.root, "--proj--")
        os.makedirs(self.sdir)
        self.sfile = os.path.join(self.sdir, "t_sid1.jsonl")
        self.log = os.path.join(self.sdir, "ctx-sid1.jsonl")
        cfg = os.path.join(self.root, "cfg")
        os.makedirs(cfg)
        p1 = mock.patch.object(fir_ext, "config_dirs", [cfg])
        p2 = mock.patch.object(fir_ext, "ext_name", "ctx")
        p1.start()
        p2.start()
        self.addCleanup(p1.stop)
        self.addCleanup(p2.stop)
        self.cfg = cfg
        self.ctx = FakeCtx(self.sfile, "sid1")

    def write(self, rows, path=None):
        with open(path or self.log, "a", encoding="utf-8") as f:
            for r in rows:
                f.write(json.dumps(r) + "\n")

    def base_rows(self):
        return [
            _src(1, "aaaaa", "tool schema", "mcp:gmail", 3100),
            _src(1, "bbbbb", "AGENTS.md", "/repo/AGENTS.md", 2000),
            _src(1, "ccccc", "user msg", "user", 50),
            _src(1, "ccccc", "tool result", "bash", 50),
            _turn(1, ["aaaaa", "bbbbb", "ccccc"], tokens=5200),
        ]

    def test_ctx_used_records_cite_joined_to_last_turn(self):
        self.write(self.base_rows())
        res = self.mod.ctx_used({"addrs": ["[ctx:bbbbb]", "zzzzz"], "why": "rules"}, self.ctx)
        self.assertEqual(res, {"ok": True, "unknown": ["zzzzz"]})
        with open(self.log, encoding="utf-8") as f:
            last = json.loads(f.read().splitlines()[-1])
        self.assertEqual(
            last,
            {
                "kind": "cite",
                "turn": 1,
                "model": "p/m",
                "addrs": ["bbbbb", "zzzzz"],
                "why": "rules",
            },
        )

    def test_ctx_used_tolerates_bad_input(self):
        self.write(self.base_rows())
        self.assertEqual(self.mod.ctx_used({"addrs": "aaaaa"}, self.ctx), {"ok": True})
        self.assertEqual(self.mod.ctx_used({"addrs": [None, 3, "  "]}, self.ctx)["ok"], True)

    def test_empty_aggregate_does_not_retrigger(self):
        env = mock.patch.dict(os.environ, {"FIR_CTX_REEVAL_TURNS": "1"})
        with env, mock.patch.object(self.mod, "load_logs", return_value=[]) as ll:
            self.mod.on_session_start({}, self.ctx)
            self.write(self.base_rows())
            self.mod.on_agent_end(None, self.ctx)
            self.mod.on_agent_end(None, self.ctx)
            self.assertEqual(ll.call_count, 1)
            self.assertEqual(self.ctx.prepended, [])

    def test_card_slug_detail(self):
        self.write(self.base_rows())
        self.mod.ctx_used({"addrs": ["bbbbb"]}, self.ctx)
        self.mod.on_turn_end(None, self.ctx)
        key, slug, detail = self.ctx.cards[-1]
        self.assertEqual(key, "ctx")
        self.assertEqual(slug, "5.2k · 3 src · 1 cited")
        self.assertLessEqual(len(slug), 24)
        self.assertIn("top idle: tools.mcp:gmail 3.1k", detail.splitlines()[0])
        self.assertIn("cited:", detail)
        self.assertIn("duplicates:", detail)
        self.assertIn("result.bash = user.user", detail)

    def test_report_per_model_sorted_with_pins(self):
        self.write(self.base_rows())
        other = os.path.join(self.root, "--other--")
        os.makedirs(other)
        self.write(
            [
                _src(1, "ddddd", "system prompt", "fir", 900),
                _turn(1, ["ddddd"], model="q/n", tokens=900),
            ],
            os.path.join(other, "ctx-sid2.jsonl"),
        )
        self.mod.ctx_used({"addrs": ["aaaaa"]}, self.ctx)
        out = self.mod.cmd_ctx([], self.ctx)["message"]
        self.assertLess(out.index("## p/m"), out.index("## q/n"))
        self.assertIn("system.fir", out)
        self.assertIn("PINNED", out)
        lines = [ln for ln in out.splitlines() if "tools.mcp:gmail" in ln]
        self.assertIn("100%", lines[0])
        only = self.mod.cmd_ctx(["q/n"], self.ctx)["message"]
        self.assertNotIn("p/m", only)

    def test_review_prompt_after_threshold_once(self):
        with open(os.path.join(self.cfg, "ctx.json"), "w") as f:
            json.dump({"reeval_turns": 2}, f)
        with mock.patch.dict(os.environ, {"FIR_CTX_REEVAL_TURNS": ""}):
            self.mod.on_session_start({}, self.ctx)
            self.write(self.base_rows())
            self.mod.on_agent_end(None, self.ctx)
            self.assertEqual(self.ctx.prepended, [])
            self.write([_turn(2, ["aaaaa"])])
            self.mod.on_agent_end(None, self.ctx)
            self.assertEqual(len(self.ctx.prepended), 1)
            self.assertIn("[ctx review] p/m: 2 new turns", self.ctx.prepended[0])
            self.assertIn("do not apply", self.ctx.prepended[0])
            self.mod.on_agent_end(None, self.ctx)  # nothing new
            self.assertEqual(len(self.ctx.prepended), 1)
        with open(os.path.join(self.cfg, "ctx-state.json")) as f:
            state = json.load(f)
        self.assertIn("tools.mcp:gmail", state["models"]["p/m"]["scores"])

    def test_review_disabled_by_env(self):
        with mock.patch.dict(os.environ, {"FIR_CTX_REEVAL_TURNS": "0"}):
            self.write(self.base_rows())
            self.mod.on_agent_end(None, self.ctx)
            self.assertEqual(self.ctx.prepended, [])

    def test_resumed_turns_not_recounted(self):
        with mock.patch.dict(os.environ, {"FIR_CTX_REEVAL_TURNS": "1"}):
            self.write(self.base_rows())  # pre-existing (resumed) data
            self.mod.on_session_start({}, self.ctx)
            self.mod.on_agent_end(None, self.ctx)
            self.assertEqual(self.ctx.prepended, [])

    def test_kfmt_and_label(self):
        self.assertEqual(self.mod.kfmt(41000), "41k")
        self.assertEqual(self.mod.kfmt(3100), "3.1k")
        self.assertEqual(self.mod.kfmt(950), "950")
        self.assertEqual(
            self.mod.label({"source": "skill read", "owner": "/a/b/wt/SKILL.md"}),
            "skill.wt/SKILL.md",
        )


if __name__ == "__main__":
    unittest.main()
