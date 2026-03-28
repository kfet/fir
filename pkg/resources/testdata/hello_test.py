#!/usr/bin/env python3
"""Tests for the hello builtin extension."""

import io
import os
import sys
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


def _load_hello():
    """Import hello.py, capturing newly registered handlers."""
    if "hello" in sys.modules:
        del sys.modules["hello"]
    before_handlers = {k: id(v) for k, v in fir_ext._event_handlers.items()}
    before_tools = list(fir_ext._tools)
    with mock.patch.object(fir_ext, "run"):
        import hello  # noqa: F401
    new_handlers = {
        k: v for k, v in fir_ext._event_handlers.items()
        if k not in before_handlers or id(v) != before_handlers[k]
    }
    new_tools = fir_ext._tools[len(before_tools):]
    return hello, new_handlers, new_tools


class TestHelloRegistration(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers, self.tools = _load_hello()

    def test_registers_session_start_handler(self):
        self.assertIn("session_start", self.handlers)

    def test_registers_agent_end_handler(self):
        self.assertIn("agent_end", self.handlers)

    def test_no_tools_registered(self):
        self.assertEqual(len(self.tools), 0)

    def test_session_start_prints_to_stderr(self):
        handler = self.handlers["session_start"]
        ctx = mock.MagicMock()
        with mock.patch("sys.stderr", new_callable=io.StringIO) as fake_err:
            handler({}, ctx)
            self.assertIn("session_start", fake_err.getvalue())

    def test_agent_end_prints_to_stderr(self):
        handler = self.handlers["agent_end"]
        ctx = mock.MagicMock()
        with mock.patch("sys.stderr", new_callable=io.StringIO) as fake_err:
            handler({}, ctx)
            self.assertIn("agent_end", fake_err.getvalue())


if __name__ == "__main__":
    unittest.main()
