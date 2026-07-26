#!/usr/bin/env python3
"""Tests for the auto_namer builtin extension."""

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


def _load_auto_namer():
    """(Re-)import auto_namer.py, resetting its global state and capturing handlers."""
    if "auto_namer" in sys.modules:
        del sys.modules["auto_namer"]
    before = {k: id(v) for k, v in fir_ext._event_handlers.items()}
    with mock.patch.object(fir_ext, "run"):
        import auto_namer
    new_handlers = {
        k: v for k, v in fir_ext._event_handlers.items() if k not in before or id(v) != before[k]
    }
    return auto_namer, new_handlers


class TestRegistration(unittest.TestCase):
    def test_registers_expected_events(self):
        _, handlers = _load_auto_namer()
        for event in ("session_start", "session_named", "tool_execution_start", "turn_end"):
            self.assertIn(event, handlers, f"{event} handler should be registered")


class TestToolExecutionStartNaming(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_auto_namer()
        # Reset globals via session_start
        ctx = mock.MagicMock()
        self.handlers["session_start"]({}, ctx)

    def test_names_session_on_first_tool_call(self):
        ctx = mock.MagicMock()
        ctx.side_query.return_value = "fix-login-bug"
        self.handlers["tool_execution_start"]({}, ctx)
        ctx.set_session_name.assert_called_once_with("fix-login-bug")

    def test_does_not_name_twice(self):
        ctx = mock.MagicMock()
        ctx.side_query.return_value = "fix-login-bug"
        self.handlers["tool_execution_start"]({}, ctx)
        ctx.side_query.return_value = "other-name"
        self.handlers["tool_execution_start"]({}, ctx)
        ctx.set_session_name.assert_called_once_with("fix-login-bug")

    def test_skips_if_already_named(self):
        ctx = mock.MagicMock()
        self.handlers["session_named"]({}, ctx)
        self.handlers["tool_execution_start"]({}, ctx)
        ctx.side_query.assert_not_called()

    def test_sanitises_response(self):
        ctx = mock.MagicMock()
        ctx.side_query.return_value = '  `"Fix--Login-Bug"`  \nextra'
        self.handlers["tool_execution_start"]({}, ctx)
        ctx.set_session_name.assert_called_once_with("fix-login-bug")

    def test_handles_side_query_exception(self):
        ctx = mock.MagicMock()
        ctx.side_query.side_effect = RuntimeError("fail")
        self.handlers["tool_execution_start"]({}, ctx)
        ctx.set_session_name.assert_not_called()


class TestTurnEndNaming(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_auto_namer()
        ctx = mock.MagicMock()
        self.handlers["session_start"]({}, ctx)

    def test_names_session_on_turn_end(self):
        ctx = mock.MagicMock()
        ctx.side_query.return_value = "explain-quantum-physics"
        self.handlers["turn_end"]({}, ctx)
        ctx.set_session_name.assert_called_once_with("explain-quantum-physics")

    def test_turn_end_skipped_if_tool_already_named(self):
        ctx = mock.MagicMock()
        ctx.side_query.return_value = "fix-login-bug"
        self.handlers["tool_execution_start"]({}, ctx)
        ctx.reset_mock()
        self.handlers["turn_end"]({}, ctx)
        ctx.side_query.assert_not_called()

    def test_tool_skipped_if_turn_end_already_named(self):
        ctx = mock.MagicMock()
        ctx.side_query.return_value = "fix-login-bug"
        self.handlers["turn_end"]({}, ctx)
        ctx.reset_mock()
        self.handlers["tool_execution_start"]({}, ctx)
        ctx.side_query.assert_not_called()


class TestSessionStartResets(unittest.TestCase):
    def setUp(self):
        self.mod, self.handlers = _load_auto_namer()

    def test_reset_allows_renaming(self):
        ctx = mock.MagicMock()
        ctx.side_query.return_value = "first-name"
        self.handlers["session_start"]({}, ctx)
        self.handlers["tool_execution_start"]({}, ctx)
        ctx.set_session_name.assert_called_once_with("first-name")
        # Reset
        self.handlers["session_start"]({}, ctx)
        ctx.reset_mock()
        ctx.side_query.return_value = "second-name"
        self.handlers["turn_end"]({}, ctx)
        ctx.set_session_name.assert_called_once_with("second-name")


if __name__ == "__main__":
    unittest.main()
