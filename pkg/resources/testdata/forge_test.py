#!/usr/bin/env python3
"""Tests for the forge builtin extension."""

import json
import os
import sys
import tempfile
import unittest
from unittest import mock

_ext_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "builtin_extensions")
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


def _load_forge():
    if "forge" in sys.modules:
        del sys.modules["forge"]
    fir_ext._tools.clear()
    fir_ext._tool_handlers.clear()
    fir_ext._event_handlers.clear()
    fir_ext._hook_handlers.clear()
    fir_ext._commands.clear()
    fir_ext._command_handlers.clear()
    with mock.patch.object(fir_ext, "run"):
        import forge
    return forge


def _make_ctx(before_tools, after_tools, loaded=None, mode="interactive"):
    """ctx.list_tools() returns before_tools first, then after_tools.

    ``loaded`` is the list of extension names ctx.list_extensions() reports;
    None makes the host look like one that cannot report them.
    """
    ctx = mock.MagicMock()
    ctx.list_tools.side_effect = [before_tools, after_tools]
    if loaded is None:
        ctx.list_extensions.return_value = []
    else:
        ctx.list_extensions.return_value = [{"name": n} for n in loaded]
    ctx.agent_info.return_value = {"mode": mode}
    return ctx


_TINY_EXT = (
    "import fir_ext\n"
    "@fir_ext.tool('echo_x', 'echo')\n"
    "def echo_x(params, ctx):\n"
    "    return {'content': [{'type': 'text', 'text': 'x'}]}\n"
    "fir_ext.run(name='tinyext')\n"
)


class TestRegistration(unittest.TestCase):
    def test_registers_forge_tool(self):
        _load_forge()
        names = [t["name"] for t in fir_ext._tools]
        self.assertIn("forge_tool", names)


class TestGlobalConfigDir(unittest.TestCase):
    def setUp(self):
        self.mod = _load_forge()

    def test_prefers_non_project_dir(self):
        with (
            mock.patch.object(fir_ext, "cwd", "/proj"),
            mock.patch.object(fir_ext, "config_dirs", ["/proj/.fir", "/home/u/.config/fir"]),
        ):
            self.assertEqual(self.mod._global_config_dir(), "/home/u/.config/fir")

    def test_skips_project_fir_even_if_last(self):
        with (
            mock.patch.object(fir_ext, "cwd", "/proj"),
            mock.patch.object(fir_ext, "config_dirs", ["/home/u/.config/fir", "/proj/.fir"]),
        ):
            self.assertEqual(self.mod._global_config_dir(), "/home/u/.config/fir")

    def test_only_project_dir_falls_back(self):
        with (
            mock.patch.object(fir_ext, "cwd", "/proj"),
            mock.patch.object(fir_ext, "config_dirs", ["/proj/.fir"]),
        ):
            self.assertEqual(self.mod._global_config_dir(), "/proj/.fir")

    def test_no_dirs_returns_none(self):
        with mock.patch.object(fir_ext, "config_dirs", []):
            self.assertIsNone(self.mod._global_config_dir())

    def test_no_cwd(self):
        with (
            mock.patch.object(fir_ext, "cwd", ""),
            mock.patch.object(fir_ext, "config_dirs", ["/home/u/.config/fir"]),
        ):
            self.assertEqual(self.mod._global_config_dir(), "/home/u/.config/fir")


class TestForgeTool(unittest.TestCase):
    def setUp(self):
        self.mod = _load_forge()
        self.handler = fir_ext._tool_handlers["forge_tool"]
        self.tmp = tempfile.mkdtemp()
        self._patches = [
            mock.patch.object(fir_ext, "cwd", "/proj"),
            mock.patch.object(fir_ext, "config_dirs", ["/proj/.fir", self.tmp]),
            # The sandbox is exercised by its own tests; here it passes.
            mock.patch.object(self.mod, "run_sandbox", return_value=(["echo_x"], None)),
        ]
        for p in self._patches:
            p.start()

    def tearDown(self):
        for p in self._patches:
            p.stop()

    def _written(self, name):
        with open(os.path.join(self.tmp, "extensions", f"{name}.py")) as f:
            return f.read()

    def test_invalid_name_rejected(self):
        ctx = mock.MagicMock()
        res = self.handler({"name": "bad name!", "code": _TINY_EXT}, ctx)
        self.assertTrue(res["is_error"])
        ctx.reload_extension.assert_not_called()

    def test_name_forge_rejected(self):
        ctx = mock.MagicMock()
        res = self.handler({"name": "forge", "code": _TINY_EXT}, ctx)
        self.assertTrue(res["is_error"])
        ctx.reload_extension.assert_not_called()

    def test_no_global_dir(self):
        with mock.patch.object(fir_ext, "config_dirs", []):
            ctx = mock.MagicMock()
            res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("global config dir", res["content"][0]["text"])

    def test_makedirs_error(self):
        ctx = mock.MagicMock()
        with mock.patch.object(self.mod.os, "makedirs", side_effect=OSError("denied")):
            res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("extensions dir", res["content"][0]["text"])

    def test_write_error(self):
        ctx = _make_ctx([], [])
        with mock.patch("builtins.open", side_effect=OSError("disk full")):
            res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("could not write", res["content"][0]["text"])

    def test_success_new_tool(self):
        ctx = _make_ctx(
            [{"name": "forge_tool"}],
            [{"name": "forge_tool"}, {"name": "echo_x"}],
        )
        res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertFalse(res["is_error"])
        self.assertIn("echo_x", res["content"][0]["text"])
        ctx.reload_extension.assert_called_once_with("tinyext")
        path = os.path.join(self.tmp, "extensions", "tinyext.py")
        self.assertTrue(os.path.exists(path))
        self.assertEqual(os.stat(path).st_mode & 0o777, 0o755)

    def test_success_no_new_tools(self):
        ctx = _make_ctx(
            [{"name": "forge_tool"}],
            [{"name": "forge_tool"}],
            loaded=["forge", "tinyext"],
        )
        res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertFalse(res["is_error"])
        text = res["content"][0]["text"]
        self.assertIn("loaded extension 'tinyext'", text)
        self.assertIn("registers no tools", text)

    def test_not_loaded_is_hard_error(self):
        ctx = _make_ctx([], [], loaded=["forge"])
        res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertTrue(res["is_error"])
        text = res["content"][0]["text"]
        self.assertIn("NOT loaded", text)
        self.assertIn("tinyext.py", text)

    def test_not_loaded_but_mode_gated_is_not_an_error(self):
        code = "#!/usr/bin/env python3\n# ---\n# name: tinyext\n# modes: acp\n# ---\n" + _TINY_EXT
        ctx = _make_ctx([], [], loaded=["forge"], mode="text")
        res = self.handler({"name": "tinyext", "code": code}, ctx)
        self.assertFalse(res["is_error"])
        self.assertIn("excludes the current mode", res["content"][0]["text"])

    def test_unknown_loaded_state_does_not_assert(self):
        """Host that cannot report extensions: no success/failure assertion."""
        ctx = _make_ctx([], [], loaded=None)
        res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertFalse(res["is_error"])
        self.assertIn("wrote and reloaded", res["content"][0]["text"])

    def test_injects_shebang_and_frontmatter(self):
        ctx = _make_ctx([], [], loaded=["tinyext"])
        res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertFalse(res["is_error"])
        written = self._written("tinyext")
        lines = written.split("\n")
        self.assertEqual(lines[0], "#!/usr/bin/env python3")
        self.assertEqual(lines[1], "# ---")
        self.assertIn("# name: tinyext", lines)
        self.assertEqual(lines[4], "# ---")
        self.assertIn("import fir_ext", written)

    def test_preserves_existing_frontmatter(self):
        code = (
            "#!/usr/bin/env python3\n"
            "# ---\n"
            "# name: tinyext\n"
            "# description: hand written\n"
            "# ---\n" + _TINY_EXT
        )
        ctx = _make_ctx([], [], loaded=["tinyext"])
        res = self.handler({"name": "tinyext", "code": code}, ctx)
        self.assertFalse(res["is_error"])
        written = self._written("tinyext")
        self.assertEqual(written, code)

    def test_adds_shebang_to_frontmatter_only_source(self):
        code = "# ---\n# name: tinyext\n# ---\n" + _TINY_EXT
        ctx = _make_ctx([], [], loaded=["tinyext"])
        res = self.handler({"name": "tinyext", "code": code}, ctx)
        self.assertFalse(res["is_error"])
        written = self._written("tinyext")
        self.assertTrue(written.startswith("#!/usr/bin/env python3\n# ---\n"))

    def test_injects_missing_name_key(self):
        code = "#!/usr/bin/env python3\n# ---\n# description: d\n# ---\n" + _TINY_EXT
        ctx = _make_ctx([], [], loaded=["tinyext"])
        res = self.handler({"name": "tinyext", "code": code}, ctx)
        self.assertFalse(res["is_error"])
        written = self._written("tinyext")
        self.assertIn("# name: tinyext", written)

    def test_name_mismatch_rejected(self):
        code = "#!/usr/bin/env python3\n# ---\n# name: other\n# ---\n" + _TINY_EXT
        ctx = mock.MagicMock()
        res = self.handler({"name": "tinyext", "code": code}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("name: 'other'", res["content"][0]["text"])
        ctx.reload_extension.assert_not_called()
        self.assertFalse(os.path.exists(os.path.join(self.tmp, "extensions", "tinyext.py")))

    def test_builtin_true_rejected(self):
        code = "#!/usr/bin/env python3\n# ---\n# name: tinyext\n# builtin: true\n# ---\n"
        ctx = mock.MagicMock()
        res = self.handler({"name": "tinyext", "code": code}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("builtin", res["content"][0]["text"])
        ctx.reload_extension.assert_not_called()

    def test_malformed_frontmatter_rejected(self):
        code = "#!/usr/bin/env python3\n# ---\n# name: tinyext\nimport os\n"
        ctx = mock.MagicMock()
        res = self.handler({"name": "tinyext", "code": code}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("malformed", res["content"][0]["text"])
        ctx.reload_extension.assert_not_called()

    def test_syntax_error_rejected(self):
        ctx = mock.MagicMock()
        res = self.handler({"name": "tinyext", "code": "def broken(:\n"}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("syntax error", res["content"][0]["text"])
        ctx.reload_extension.assert_not_called()

    def test_reload_error(self):
        ctx = mock.MagicMock()
        ctx.list_tools.return_value = []
        ctx.reload_extension.side_effect = RuntimeError("handshake timeout")
        res = self.handler({"name": "broken", "code": "import nope_xyz\n"}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("handshake timeout", res["content"][0]["text"])


_MCP_EXT = (
    "import fir_ext\n"
    "@fir_ext.tool('mcp_reload', 'reload mcp')\n"
    "def r(params, ctx):\n"
    "    return {'content': [{'type': 'text', 'text': 'x'}]}\n"
    "fir_ext.run(name='mcpfix')\n"
)


class TestToolNameRules(unittest.TestCase):
    def setUp(self):
        self.mod = _load_forge()

    def test_mcp_single_underscore_rejected(self):
        # The 2026-11-15 incident: this name broke every Anthropic OAuth session.
        err = self.mod.check_tool_names(["mcp_reload"])
        self.assertIsNotNone(err)
        self.assertIn("'mcp_reload'", err)
        self.assertIn("mcp_", err)
        self.assertIn("out of", err)

    def test_mcp_namespace_rejected(self):
        err = self.mod.check_tool_names(["mcp__srv__tool"])
        self.assertIn("MCP server tools", err)

    def test_invalid_chars_and_length_rejected(self):
        self.assertIsNotNone(self.mod.check_tool_names(["has space"]))
        self.assertIsNotNone(self.mod.check_tool_names(["x" * 65]))
        self.assertIsNotNone(self.mod.check_tool_names([""]))

    def test_good_names_pass(self):
        # Mcp_x and reload_mcp were verified to work against the live API.
        self.assertIsNone(self.mod.check_tool_names(["reload_mcp", "Mcp_x", "a-b", "x" * 64]))

    def test_every_problem_reported(self):
        err = self.mod.check_tool_names(["mcp_a", "ok", "mcp_b"])
        self.assertIn("'mcp_a'", err)
        self.assertIn("'mcp_b'", err)
        self.assertNotIn("'ok'", err)

    def test_rules_are_data(self):
        self.assertTrue(all(len(r) == 2 for r in self.mod.TOOL_NAME_RULES))

    def test_literal_tool_names(self):
        code = (
            "import fir_ext\n"
            "from fir_ext import tool\n"
            "@fir_ext.tool('a', 'd')\n"
            "def a(p, c): pass\n"
            "@fir_ext.tool(name='b', description='d')\n"
            "def b(p, c): pass\n"
            "@tool('c', 'd')\n"
            "def c(p, c2): pass\n"
            "N = 'dyn'\n"
            "@fir_ext.tool(N, 'd')\n"
            "def d(p, c): pass\n"
        )
        self.assertEqual(sorted(self.mod._literal_tool_names(code)), ["a", "b", "c"])


class TestForgeSafety(unittest.TestCase):
    """forge_tool installs nothing unless static checks and the sandbox pass."""

    def setUp(self):
        self.mod = _load_forge()
        self.handler = fir_ext._tool_handlers["forge_tool"]
        self.tmp = tempfile.mkdtemp()
        self._patches = [
            mock.patch.object(fir_ext, "cwd", "/proj"),
            mock.patch.object(fir_ext, "config_dirs", ["/proj/.fir", self.tmp]),
        ]
        for p in self._patches:
            p.start()
        self.global_file = os.path.join(self.tmp, "extensions", "mcpfix.py")

    def tearDown(self):
        for p in self._patches:
            p.stop()

    def test_mcp_prefix_rejected_before_sandbox(self):
        ctx = mock.MagicMock()
        with mock.patch.object(self.mod, "run_sandbox") as sb:
            res = self.handler({"name": "mcpfix", "code": _MCP_EXT}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("'mcp_reload' is rejected", res["content"][0]["text"])
        sb.assert_not_called()
        ctx.reload_extension.assert_not_called()
        self.assertFalse(os.path.exists(self.global_file))

    def test_sandbox_failure_returned_and_nothing_installed(self):
        ctx = _make_ctx([], [], loaded=["forge"])
        ctx.agent_info.return_value = {"mode": "interactive", "model": {"provider": "p", "id": "m"}}
        err = "sandbox sub-agent (p/m) failed with exit code 1: 400 You're out of extra usage"
        with mock.patch.object(self.mod, "run_sandbox", return_value=(None, err)) as sb:
            res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertTrue(res["is_error"])
        text = res["content"][0]["text"]
        self.assertIn("nothing installed", text)
        self.assertIn("out of extra usage", text)
        self.assertEqual(sb.call_args[0][0], "tinyext")
        self.assertEqual(sb.call_args[0][2:], ("p", "m"))  # caller's provider and model
        ctx.reload_extension.assert_not_called()
        self.assertFalse(os.path.exists(os.path.join(self.tmp, "extensions", "tinyext.py")))

    def test_dynamic_bad_name_caught_after_sandbox(self):
        ctx = _make_ctx([], [], loaded=["forge"])
        with mock.patch.object(self.mod, "run_sandbox", return_value=(["mcp_dyn"], None)):
            res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("'mcp_dyn'", res["content"][0]["text"])
        ctx.reload_extension.assert_not_called()

    def test_session_scope_never_touches_global_dir(self):
        ctx = _make_ctx([], [{"name": "echo_x"}], loaded=["tinyext"])
        with mock.patch.object(self.mod, "run_sandbox", return_value=(["echo_x"], None)):
            res = self.handler({"name": "tinyext", "code": _TINY_EXT, "scope": "session"}, ctx)
        self.assertFalse(res["is_error"], res)
        self.assertIn("this session only", res["content"][0]["text"])
        name, path = ctx.reload_extension.call_args[0]
        self.assertEqual(name, "tinyext")
        self.assertTrue(path.endswith("tinyext.py"))
        self.assertFalse(path.startswith(self.tmp))
        self.assertFalse(os.path.exists(os.path.join(self.tmp, "extensions")))
        # A later global forge of the same name must re-point the session load.
        ctx2 = _make_ctx([], [], loaded=["tinyext"])
        with mock.patch.object(self.mod, "run_sandbox", return_value=(["echo_x"], None)):
            res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx2)
        self.assertFalse(res["is_error"], res)
        self.assertEqual(
            ctx2.reload_extension.call_args[0],
            ("tinyext", os.path.join(self.tmp, "extensions", "tinyext.py")),
        )

    def test_global_failure_removes_new_file(self):
        ctx = _make_ctx([], [], loaded=["forge"])
        ctx.reload_extension.side_effect = [RuntimeError("handshake timeout"), None]
        with mock.patch.object(self.mod, "run_sandbox", return_value=(["echo_x"], None)):
            res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("removed it", res["content"][0]["text"])
        self.assertFalse(os.path.exists(os.path.join(self.tmp, "extensions", "tinyext.py")))

    def test_global_failure_restores_previous_version(self):
        ext = os.path.join(self.tmp, "extensions")
        os.makedirs(ext)
        with open(os.path.join(ext, "tinyext.py"), "w") as f:
            f.write("OLD")
        ctx = _make_ctx([], [], loaded=["forge"])  # never loads
        with mock.patch.object(self.mod, "run_sandbox", return_value=(["echo_x"], None)):
            res = self.handler({"name": "tinyext", "code": _TINY_EXT}, ctx)
        self.assertTrue(res["is_error"])
        self.assertIn("restored the previous version", res["content"][0]["text"])
        with open(os.path.join(ext, "tinyext.py")) as f:
            self.assertEqual(f.read(), "OLD")

    def test_session_failure_removes_temp_dir(self):
        ctx = _make_ctx([], [], loaded=["forge"])
        ctx.reload_extension.side_effect = RuntimeError("boom")
        with mock.patch.object(self.mod, "run_sandbox", return_value=(["echo_x"], None)):
            res = self.handler({"name": "tinyext", "code": _TINY_EXT, "scope": "session"}, ctx)
        self.assertTrue(res["is_error"])
        _, path = ctx.reload_extension.call_args[0]
        self.assertFalse(os.path.exists(os.path.dirname(path)))
        self.assertNotIn("tinyext", self.mod._session_loaded)

    def test_session_not_loaded_is_not_recorded(self):
        ctx = _make_ctx([], [], loaded=["forge"])
        with mock.patch.object(self.mod, "run_sandbox", return_value=(["echo_x"], None)):
            res = self.handler({"name": "tinyext", "code": _TINY_EXT, "scope": "session"}, ctx)
        self.assertTrue(res["is_error"])
        self.assertNotIn("tinyext", self.mod._session_loaded)
        self.assertEqual(self.mod._session_dirs, [])

    def test_session_dirs_cleaned_at_exit(self):
        ctx = _make_ctx([], [], loaded=["tinyext"])
        with mock.patch.object(self.mod, "run_sandbox", return_value=(["echo_x"], None)):
            self.handler({"name": "tinyext", "code": _TINY_EXT, "scope": "session"}, ctx)
        _, path = ctx.reload_extension.call_args[0]
        self.assertTrue(os.path.exists(path))
        self.mod._cleanup_session_dirs()
        self.assertFalse(os.path.exists(os.path.dirname(path)))

    def test_sandbox_error_mentions_other_extensions(self):
        with mock.patch.object(self.mod, "run_sandbox", return_value=(None, "x")):
            res = self.handler({"name": "tinyext", "code": _TINY_EXT}, _make_ctx([], []))
        self.assertIn("other global extensions", res["content"][0]["text"])

    def test_bad_scope_rejected(self):
        res = self.handler(
            {"name": "tinyext", "code": _TINY_EXT, "scope": "project"}, mock.MagicMock()
        )
        self.assertTrue(res["is_error"])

    def test_probe_name_reserved(self):
        res = self.handler({"name": "forgeprobe", "code": _TINY_EXT}, mock.MagicMock())
        self.assertTrue(res["is_error"])


# A stand-in for the fir binary. It records its argv and cwd, then acts out
# the scenario named by $FAKE_FIR_SCENARIO the way `fir -p` + the probe would.
_FAKE_FIR = r"""#!/usr/bin/env python3
import json, os, sys
d = os.getcwd()
with open(os.environ["FAKE_FIR_LOG"], "w") as f:
    exts = sorted(os.listdir(os.path.join(d, ".fir", "extensions")))
    cand = open(os.path.join(d, ".fir", "extensions", "tinyext.py")).read()
    json.dump({"argv": sys.argv[1:], "exts": exts, "cand": cand}, f)
sc = os.environ["FAKE_FIR_SCENARIO"]
probe = os.path.join(d, ".fir", "probe.json")
def w(rec):
    json.dump(rec, open(probe, "w"))
if sc == "http400":
    w({"model_ok": False, "error": "400 You're out of extra usage."})
    sys.stderr.write("400 You're out of extra usage.\n"); sys.exit(1)
if sc == "ok":
    w({"model_ok": True, "tools": ["echo_x", "read"],
       "extensions": [{"name": "tinyext", "tools": ["echo_x"]}]})
if sc == "hidden":
    w({"model_ok": True, "tools": ["read"],
       "extensions": [{"name": "tinyext", "tools": ["echo_x"]}]})
if sc == "notloaded":
    w({"model_ok": True, "tools": ["read"], "extensions": [{"name": "forgeprobe"}]})
if sc == "badcall":
    w({"model_ok": False, "error": "assistant message ended with stop_reason=error"})
print("OK")
"""


class TestRunSandbox(unittest.TestCase):
    def setUp(self):
        self.mod = _load_forge()
        self.dir = tempfile.mkdtemp()
        self.fir = os.path.join(self.dir, "fir")
        with open(self.fir, "w") as f:
            f.write(_FAKE_FIR)
        os.chmod(self.fir, 0o755)  # noqa: S103 — fake binary must be executable
        self.log = os.path.join(self.dir, "log.json")
        self.env = mock.patch.dict(os.environ, {"FIR_BIN": self.fir, "FAKE_FIR_LOG": self.log})
        self.env.start()

    def tearDown(self):
        self.env.stop()

    def _run(self, scenario, source=None):
        os.environ["FAKE_FIR_SCENARIO"] = scenario
        src = source or ("#!/usr/bin/env python3\n# ---\n# name: tinyext\n# ---\n" + _TINY_EXT)
        return self.mod.run_sandbox("tinyext", src, "prov", "mod-1")

    def test_success_returns_declared_tools(self):
        tools, err = self._run("ok")
        self.assertIsNone(err)
        self.assertEqual(tools, ["echo_x"])
        with open(self.log) as f:
            rec = json.load(f)
        argv = rec["argv"]
        self.assertIn("-p", argv)
        self.assertEqual(argv[argv.index("--provider") + 1], "prov")
        self.assertEqual(argv[argv.index("--model") + 1], "mod-1")
        self.assertIn("--no-session", argv)
        self.assertEqual(rec["exts"], ["forgeprobe.py", "tinyext.py"])

    def test_provider_400_is_returned(self):
        tools, err = self._run("http400")
        self.assertIsNone(tools)
        self.assertIn("exit code 1", err)
        self.assertIn("out of extra usage", err)

    def test_tool_not_visible(self):
        _, err = self._run("hidden")
        self.assertIn("not visible", err)
        self.assertIn("echo_x", err)

    def test_extension_not_loaded(self):
        _, err = self._run("notloaded")
        self.assertIn("did not load", err)

    def test_no_model_call(self):
        _, err = self._run("silent")
        self.assertIn("no successful model call", err)

    def test_failed_model_call(self):
        _, err = self._run("badcall")
        self.assertIn("stop_reason=error", err)

    def test_sandbox_dir_removed(self):
        before = set(os.listdir(tempfile.gettempdir()))
        self._run("ok")
        leaked = [
            d
            for d in set(os.listdir(tempfile.gettempdir())) - before
            if d.startswith("fir-forge-sandbox-")
        ]
        self.assertEqual(leaked, [])

    def test_mode_gated_candidate_loads_in_sandbox(self):
        src = "#!/usr/bin/env python3\n# ---\n# name: tinyext\n# modes: acp\n# ---\n" + _TINY_EXT
        _, err = self._run("ok", src)
        self.assertIsNone(err)
        with open(self.log) as f:
            cand = json.load(f)["cand"]
        self.assertNotIn("modes", cand)
        self.assertIn("# name: tinyext", cand)

    def test_unknown_model(self):
        _, err = self.mod.run_sandbox("tinyext", "x", "", "")
        self.assertIn("provider and model", err)

    def test_probe_source_compiles(self):
        compile(self.mod._PROBE_SOURCE, "forgeprobe.py", "exec")


@unittest.skipUnless(
    os.environ.get("FIR_FORGE_E2E_PROVIDER") and os.environ.get("FIR_FORGE_E2E_MODEL"),
    "live sandbox e2e: set FIR_FORGE_E2E_PROVIDER/FIR_FORGE_E2E_MODEL (and FIR_BIN)",
)
class TestRunSandboxLive(unittest.TestCase):
    """Real `fir -p` against a real provider. Against the Anthropic subscription
    (OAuth) provider the mcp_ tool must fail the sandbox — the incident."""

    def setUp(self):
        self.mod = _load_forge()
        self.p = os.environ["FIR_FORGE_E2E_PROVIDER"]
        self.m = os.environ["FIR_FORGE_E2E_MODEL"]

    def test_good_extension_passes(self):
        src = "#!/usr/bin/env python3\n# ---\n# name: tinyext\n# ---\n" + _TINY_EXT
        tools, err = self.mod.run_sandbox("tinyext", src, self.p, self.m)
        self.assertIsNone(err)
        self.assertEqual(tools, ["echo_x"])

    def test_mcp_prefixed_tool_fails_sandbox(self):
        src = "#!/usr/bin/env python3\n# ---\n# name: mcpfix\n# ---\n" + _MCP_EXT
        tools, err = self.mod.run_sandbox("mcpfix", src, self.p, self.m)
        self.assertIsNone(tools)
        self.assertIn("out of extra usage", err)


if __name__ == "__main__":
    unittest.main()
