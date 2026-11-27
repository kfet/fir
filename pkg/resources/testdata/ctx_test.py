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
        self.messages = []

    def get_session_file(self):
        return self.session_file

    def get_session_id(self):
        return self.sid

    def put_observable(self, key, slug, detail=""):
        self.cards.append((key, slug, detail))

    def prepend(self, content):
        self.prepended.append(content)

    def send_message(
        self, custom_type, content, display=False, deliver_as=None, trigger_turn=False
    ):
        self.messages.append((custom_type, content, deliver_as, trigger_turn))


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

    def test_kfmt_and_label(self):
        self.assertEqual(self.mod.kfmt(41000), "41k")
        self.assertEqual(self.mod.kfmt(3100), "3.1k")
        self.assertEqual(self.mod.kfmt(950), "950")
        self.assertEqual(
            self.mod.label({"source": "skill read", "owner": "/a/b/wt/SKILL.md"}),
            "skill.wt/SKILL.md",
        )


REPO = os.path.realpath(os.path.join(_HERE, "..", "..", ".."))
EXT = os.path.join(REPO, "pkg", "resources", "builtin_extensions")
SKILL = os.path.join(REPO, "pkg", "resources", "builtin_skills", "self", "SKILL.md")
AGENTS = os.path.join(REPO, "AGENTS.md")


class FindingsTest(CtxTest):
    def dup_rows(self, turns=4):
        rows = [
            _src(1, "l1111", "lesson", "lessons", 900),
            _src(1, "l1111", "SYS_EXT section", "mood", 900),
            _src(1, "t2222", "tool schema", "lessons", 400),
            _src(1, "t2222", "tool schema", "mood", 400),
            _src(1, "p3333", "tool schema", "pipe", 5000),
            _src(1, "s4444", "skill read", SKILL, 4000),
            _src(1, "a5555", "AGENTS.md", AGENTS, 3500),
            _src(1, "r6666", "tool result", "bash", 9000),
            _src(1, "r6666", "user msg", "user", 9000),
        ]
        addrs = ["l1111", "t2222", "p3333", "s4444", "a5555", "r6666"]
        rows += [_turn(i, addrs) for i in range(1, turns + 1)]
        return rows

    def fs(self):
        self.write(self.dup_rows())
        return self.mod.current_findings(self.ctx, force=True)

    def test_duplicates_detected_results_excluded(self):
        fs = self.fs()
        self.assertIn("dup:lesson.lessons", fs)
        self.assertIn("dup:section.mood", fs)
        self.assertIn("dup:tools.mood", fs)
        self.assertIn("1 chunk duplicated with", fs["dup:tools.lessons"]["text"])
        self.assertIn("section.mood", fs["dup:lesson.lessons"]["text"])
        self.assertEqual(fs["dup:lesson.lessons"]["tokens"], 3600)
        # tool results / user msgs: stats only, never findings
        self.assertFalse([f for f in fs if "result." in f or "user." in f])
        self.assertIn("result.bash", self.mod.cmd_ctx([], self.ctx)["message"])

    def test_cost_outliers_and_rare_gated_by_coverage(self):
        fs = self.fs()
        self.assertIn("cost:tools.pipe", fs)
        self.assertNotIn("cost:tools.lessons", fs)  # small
        self.assertFalse([f for f in fs if f.startswith("rare:")])  # no cites yet
        # >=5% of turns cited -> rare findings appear
        rows = [_turn(i, ["p3333", "a5555"]) for i in range(5, 30)]
        self.write(rows)
        self.write(
            [
                {"kind": "cite", "turn": 5, "addrs": ["a5555"]},
                {"kind": "cite", "turn": 6, "addrs": ["a5555"]},
            ]
        )
        fs = self.mod.current_findings(self.ctx, force=True)
        self.assertIn("rare:tools.pipe", fs)
        self.assertNotIn("rare:agents." + "/".join(AGENTS.split("/")[-2:]), fs)

    def test_owner_file_mapping_real_owners(self):
        fs = self.fs()
        f4 = self.mod.findings_for_path

        def ids(p):
            return {f["id"] for f in f4(p, fs)}

        self.assertEqual(
            ids(os.path.join(EXT, "lessons.py")), {"dup:lesson.lessons", "dup:tools.lessons"}
        )
        self.assertEqual(ids(os.path.join(EXT, "mood.py")), {"dup:section.mood", "dup:tools.mood"})
        self.assertEqual(
            ids(os.path.join(EXT, "pipe.py")), {"cost:tools.pipe"}
        )  # tool description owner
        self.assertEqual({f["kind"] for f in f4(SKILL, fs)}, {"cost"})
        self.assertEqual({f["source"] for f in f4(SKILL, fs)}, {"skill read"})
        self.assertEqual({f["source"] for f in f4(AGENTS, fs)}, {"AGENTS.md"})
        # wrong files get nothing
        self.assertEqual(ids(os.path.join(EXT, "doctor.py")), set())
        self.assertEqual(
            ids(os.path.join(REPO, "pkg", "resources", "builtin_skills", "wt", "SKILL.md")), set()
        )
        fake = os.path.join(self.root, "lessons.py")  # same stem, no frontmatter
        with open(fake, "w") as f:
            f.write("print('x')\n")
        self.assertEqual(ids(fake), set())
        other = os.path.join(self.root, "AGENTS.md")
        open(other, "w").close()
        self.assertEqual(ids(other), set())

    def test_hint_one_line_per_turn(self):
        self.fs()
        self.mod.on_agent_start(None, self.ctx)
        for name in ("lessons.py", "mood.py", "pipe.py"):
            self.mod.on_tool_call(
                {"tool_name": "read", "params": {"path": os.path.join(EXT, name)}}, self.ctx
            )
        hints = [m for m in self.ctx.messages if m[0] == "ctx_hint"]
        self.assertEqual(len(hints), 1)
        line = hints[0][1]
        self.assertNotIn("\n", line)
        self.assertTrue(line.startswith("[ctx] lessons.py: "))
        self.assertIn('ctx_report(owner="lessons")', line)
        self.assertIn("2 findings ~5.2k tok", line)
        # next turn: a different owner may hint; the same owner does not repeat
        self.mod.on_agent_end(None, self.ctx)
        self.ctx.messages.clear()
        self.mod.on_agent_start(None, self.ctx)
        self.mod.on_tool_call(
            {"tool_name": "edit", "params": {"path": os.path.join(EXT, "lessons.py")}}, self.ctx
        )
        self.mod.on_tool_call({"tool_name": "bash", "params": {"command": "cat mood.py"}}, self.ctx)
        self.mod.on_tool_call(
            {"tool_name": "Edit", "params": {"path": os.path.join(EXT, "mood.py")}}, self.ctx
        )
        hints = [m[1] for m in self.ctx.messages if m[0] == "ctx_hint"]
        self.assertEqual(len(hints), 1)
        self.assertTrue(hints[0].startswith("[ctx] mood.py: "))

    def test_nudge_once_per_finding_and_on_change(self):
        with mock.patch.dict(os.environ, {"FIR_CTX_NUDGE": ""}):
            self.write(self.dup_rows())
            self.mod.on_agent_end(None, self.ctx)
            nudges = [m for m in self.ctx.messages if m[0] == "ctx_nudge"]
            self.assertEqual(len(nudges), 1)
            body = nudges[0][1]
            self.assertTrue(body.startswith("[interrupt:ctx] "))
            self.assertEqual(len(body.splitlines()), 2)
            self.assertIn('say "ctx review"', body)
            self.assertIsNone(nudges[0][2])  # not a steer / prompt
            self.assertFalse(nudges[0][3])  # never triggers a turn
            self.mod.FINDINGS_TTL = 0
            self.mod.on_agent_end(None, self.ctx)
            self.assertEqual(len([m for m in self.ctx.messages if m[0] == "ctx_nudge"]), 1)
            # changed finding -> one more nudge
            self.write(
                [
                    _src(9, "t7777", "tool schema", "lessons", 400),
                    _src(9, "t7777", "tool schema", "mood", 400),
                    _turn(9, ["t7777"]),
                ]
            )
            self.mod.on_agent_end(None, self.ctx)
            nudges = [m for m in self.ctx.messages if m[0] == "ctx_nudge"]
            self.assertEqual(len(nudges), 2)
            self.mod.on_agent_end(None, self.ctx)
            self.assertEqual(len([m for m in self.ctx.messages if m[0] == "ctx_nudge"]), 2)

    def test_dismiss_and_snooze(self):
        self.mod.FINDINGS_TTL = 0
        self.write(self.dup_rows())
        res = self.mod.ctx_report({"action": "dismiss"}, self.ctx)
        self.assertIn("dup:lesson.lessons", res["dismissed"])
        self.mod.on_agent_end(None, self.ctx)
        self.assertEqual(self.ctx.messages, [])
        self.mod.ctx_report({"action": "snooze", "days": 1}, self.ctx)
        self.write(
            [
                _src(9, "t7777", "tool schema", "lessons", 400),
                _src(9, "t7777", "tool schema", "mood", 400),
                _turn(9, ["t7777"]),
            ]
        )
        self.mod.on_agent_end(None, self.ctx)
        self.assertEqual(self.ctx.messages, [])

    def test_nudge_disabled(self):
        self.write(self.dup_rows())
        with mock.patch.dict(os.environ, {"FIR_CTX_NUDGE": "0"}):
            self.mod.on_agent_end(None, self.ctx)
        with open(os.path.join(self.cfg, "ctx.json"), "w") as f:
            json.dump({"nudge": False}, f)
        self.mod.on_agent_end(None, self.ctx)
        self.assertEqual(self.ctx.messages, [])

    def test_ext_name_of_shell_and_js(self):
        sh = os.path.join(self.root, "x.sh")
        with open(sh, "w") as f:
            f.write("#!/bin/sh\n# ---\n# name: shx\n# ---\n")
        js = os.path.join(self.root, "y.js")
        with open(js, "w") as f:
            f.write("// ---\n// name: jsy\n// ---\n")
        self.assertEqual(self.mod.ext_name_of(sh), "shx")
        self.assertEqual(self.mod.ext_name_of(js), "jsy")
        self.assertEqual(self.mod.ext_name_of(os.path.join(self.root, "nope.py")), "")

    def test_report_by_owner(self):
        self.fs()
        out = self.mod.ctx_report({"owner": "lessons"}, self.ctx)["message"]
        self.assertIn("dup:lesson.lessons", out)
        self.assertNotIn("pipe", out)
        self.assertIn("cost:tools.pipe", self.mod.cmd_ctx(["findings"], self.ctx)["message"])
        self.assertIn("no findings", self.mod.ctx_report({"owner": "nope"}, self.ctx)["message"])

    def test_agent_end_never_prepends(self):
        self.mod.FINDINGS_TTL = 0
        with mock.patch.dict(os.environ, {"FIR_CTX_NUDGE": ""}):
            self.mod.on_session_start({}, self.ctx)
            self.write(self.dup_rows(turns=400))
            for _ in range(3):
                self.mod.on_agent_end(None, self.ctx)
        self.assertEqual(self.ctx.prepended, [])
        self.assertFalse(hasattr(self.mod, "maybe_review"))


if __name__ == "__main__":
    unittest.main()
