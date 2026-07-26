#!/usr/bin/env python3
"""Tests for the notify builtin extension."""

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


def _load_notify():
    """(Re-)import notify.py, capturing its handlers."""
    if "notify" in sys.modules:
        del sys.modules["notify"]
    before = {k: id(v) for k, v in fir_ext._event_handlers.items()}
    with mock.patch.object(fir_ext, "run"):
        import notify
    new_handlers = {
        k: v for k, v in fir_ext._event_handlers.items() if k not in before or id(v) != before[k]
    }
    return notify, new_handlers


class TestInTmux(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load_notify()

    def test_in_tmux_yes(self):
        with mock.patch.dict(os.environ, {"TMUX": "/tmp/tmux-1000/default,12345,0"}):
            self.assertTrue(self.mod._in_tmux())

    def test_in_tmux_no(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertFalse(self.mod._in_tmux())


class TestInKitty(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load_notify()

    def test_in_kitty_yes(self):
        with mock.patch.dict(os.environ, {"KITTY_WINDOW_ID": "1"}):
            self.assertTrue(self.mod._in_kitty())

    def test_in_kitty_no(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertFalse(self.mod._in_kitty())


class TestWriteToTty(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load_notify()

    def test_write_to_tty_success(self):
        fake_tty = mock.mock_open()
        with mock.patch("builtins.open", fake_tty):
            self.mod._write_to_tty(b"hello")
        fake_tty.assert_called_once_with("/dev/tty", "wb", buffering=0)
        fake_tty().write.assert_called_once_with(b"hello")

    def test_write_to_tty_no_terminal(self):
        with mock.patch("builtins.open", side_effect=OSError("no tty")):
            self.mod._write_to_tty(b"hello")


class TestNotifyOSC777(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load_notify()

    def test_plain(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            with mock.patch.object(self.mod, "_write_to_tty") as mock_write:
                self.mod._notify_osc777("title", "body")
                mock_write.assert_called_once()
                data = mock_write.call_args[0][0]
                self.assertIn(b"777;notify;title;body", data)
                self.assertNotIn(b"Ptmux", data)

    def test_inside_tmux(self):
        with mock.patch.dict(os.environ, {"TMUX": "1"}):
            with mock.patch.object(self.mod, "_write_to_tty") as mock_write:
                self.mod._notify_osc777("t", "b")
                data = mock_write.call_args[0][0]
                self.assertIn(b"Ptmux", data)
                self.assertIn(b"777;notify;t;b", data)


class TestNotifyOSC99(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load_notify()

    def test_plain(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            with mock.patch.object(self.mod, "_write_to_tty") as mock_write:
                self.mod._notify_osc99("title", "body")
                self.assertEqual(mock_write.call_count, 2)
                calls = [c[0][0] for c in mock_write.call_args_list]
                self.assertIn(b"99;", calls[0])
                self.assertIn(b"title", calls[0])
                self.assertIn(b"body", calls[1])

    def test_inside_tmux(self):
        with mock.patch.dict(os.environ, {"TMUX": "1"}):
            with mock.patch.object(self.mod, "_write_to_tty") as mock_write:
                self.mod._notify_osc99("t", "b")
                self.assertEqual(mock_write.call_count, 2)
                for call in mock_write.call_args_list:
                    self.assertIn(b"Ptmux", call[0][0])

    def test_uses_tmux_session_as_id(self):
        # $TMUX = socket,pid,sessionid — sanitised "/" and "," to "_".
        with mock.patch.dict(os.environ, {"TMUX": "/tmp/tmux-1000/default,12345,7"}):
            with mock.patch.object(self.mod, "_write_to_tty") as mock_write:
                self.mod._notify_osc99("t", "b")
                data = mock_write.call_args_list[0][0][0]
                self.assertIn(b"i=_tmp_tmux-1000_default_12345_7:", data)

    def test_default_id_outside_tmux(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            with mock.patch.object(self.mod, "_write_to_tty") as mock_write:
                self.mod._notify_osc99("t", "b")
                data = mock_write.call_args_list[0][0][0]
                self.assertIn(b"i=1:", data)


class TestNotifyTerminal(unittest.TestCase):
    def setUp(self):
        self.mod, _ = _load_notify()

    def test_uses_osc99_for_kitty(self):
        with mock.patch.dict(os.environ, {"KITTY_WINDOW_ID": "1"}, clear=False):
            with mock.patch.object(self.mod, "_notify_osc99") as mock99:
                with mock.patch.object(self.mod, "_notify_osc777") as mock777:
                    self.mod.notify_terminal("t", "b")
                    mock99.assert_called_once_with("t", "b")
                    mock777.assert_not_called()

    def test_uses_osc777_by_default(self):
        env = {k: v for k, v in os.environ.items() if k != "KITTY_WINDOW_ID"}
        with mock.patch.dict(os.environ, env, clear=True):
            with mock.patch.object(self.mod, "_notify_osc99") as mock99:
                with mock.patch.object(self.mod, "_notify_osc777") as mock777:
                    self.mod.notify_terminal("t", "b")
                    mock777.assert_called_once_with("t", "b")
                    mock99.assert_not_called()


class TestAgentEndHandler(unittest.TestCase):
    def test_agent_end_notifies(self):
        mod, handlers = _load_notify()
        self.assertIn("agent_end", handlers)
        handler = handlers["agent_end"]
        with mock.patch.object(mod, "notify_terminal") as mock_nt:
            handler({}, mock.MagicMock())
            mock_nt.assert_called_once_with("fir", "Ready for input")


if __name__ == "__main__":
    unittest.main()
