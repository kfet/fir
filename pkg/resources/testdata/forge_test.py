#!/usr/bin/env python3
"""Tests for the forge builtin extension."""

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


if __name__ == "__main__":
    unittest.main()
