#!/usr/bin/env python3
"""Tests for the Python extension examples — imports the real extension files."""

import importlib.util
import os
import sys
import unittest
from unittest.mock import MagicMock, patch

# Locate .fir/extensions/ relative to this file (4 levels up = project root).
_HERE = os.path.dirname(os.path.abspath(__file__))
_PROJECT_ROOT = os.path.normpath(os.path.join(_HERE, "..", "..", "..", ".."))
_EXTENSIONS_DIR = os.path.join(_PROJECT_ROOT, ".fir", "extensions")


def _load_extension(name: str):
    """Load a .fir/extensions/<name>.py module with fir_ext.run() stubbed out.

    fir_ext.run() blocks on stdin, so we replace it with a no-op. The
    fir_ext.on() decorator is kept as a passthrough so handler functions are
    still defined on the module.
    """
    path = os.path.join(_EXTENSIONS_DIR, f"{name}.py")
    fake_fir_ext = MagicMock()
    fake_fir_ext.on = lambda _event: (lambda fn: fn)
    with patch.dict(sys.modules, {"fir_ext": fake_fir_ext}):
        spec = importlib.util.spec_from_file_location(name, path)
        mod = importlib.util.module_from_spec(spec)  # type: ignore[arg-type]
        spec.loader.exec_module(mod)  # type: ignore[union-attr]
    return mod


_tmuxspinner = _load_extension("tmuxspinner")
_notify = _load_extension("notify")


class TestStripSpinnerSuffix(unittest.TestCase):
    def _strip(self, name: str) -> str:
        return _tmuxspinner._strip_spinner_suffix(name)

    def test_plain(self):
        self.assertEqual(self._strip("hello"), "hello")

    def test_single_suffix(self):
        self.assertEqual(self._strip("fir ⠋"), "fir")

    def test_multiple_suffixes(self):
        self.assertEqual(self._strip("fir ⠋ ⠙"), "fir")

    def test_empty(self):
        self.assertEqual(self._strip(""), "")

    def test_just_braille(self):
        self.assertEqual(self._strip(" ⠋"), "")

    def test_no_space_before_braille(self):
        self.assertEqual(self._strip("fir⠋"), "fir⠋")


class TestNotifyFormats(unittest.TestCase):
    """Verify notification escape sequences produced by the real notify.py."""

    def _capture_tty(self, fn, *args, **kwargs):
        """Call fn and return the bytes that would have been written to /dev/tty."""
        written: list[bytes] = []

        def fake_write_to_tty(data: bytes) -> None:
            written.append(data)

        with patch.object(_notify, "_write_to_tty", side_effect=fake_write_to_tty):
            fn(*args, **kwargs)
        return b"".join(written).decode()

    def test_osc777_no_tmux(self):
        with patch.dict(os.environ, {"TMUX": ""}, clear=False):
            result = self._capture_tty(_notify._notify_osc777, "title", "body")
        self.assertEqual(result, "\x1b]777;notify;title;body\x1b\\")

    def test_osc777_in_tmux(self):
        with patch.dict(
            os.environ, {"TMUX": "/tmp/tmux-1234/default,1,0"}, clear=False
        ):
            result = self._capture_tty(_notify._notify_osc777, "title", "body")
        self.assertEqual(
            result,
            "\x1bPtmux;\x1b\x1b]777;notify;title;body\x1b\x1b\\\x1b\\",
        )

    def test_osc99_no_tmux(self):
        with patch.dict(os.environ, {"TMUX": ""}, clear=False):
            result = self._capture_tty(_notify._notify_osc99, "title", "body")
        self.assertIn("99;i=1:d=0;title", result)
        self.assertIn("99;i=1:p=body;body", result)

    def test_osc99_in_tmux(self):
        with patch.dict(
            os.environ, {"TMUX": "/tmp/tmux-1234/default,1,0"}, clear=False
        ):
            result = self._capture_tty(_notify._notify_osc99, "title", "body")
        self.assertIn("Ptmux;", result)
        self.assertIn("99;i=1:d=0;title", result)
        self.assertIn("99;i=1:p=body;body", result)

    def test_notify_terminal_uses_osc99_in_kitty(self):
        with patch.dict(
            os.environ, {"KITTY_WINDOW_ID": "1", "TMUX": ""}, clear=False
        ):
            result = self._capture_tty(_notify.notify_terminal, "fir", "Ready for input")
        self.assertIn("99;", result)

    def test_notify_terminal_uses_osc777_outside_kitty(self):
        env = {k: v for k, v in os.environ.items() if k != "KITTY_WINDOW_ID"}
        env["TMUX"] = ""
        with patch.dict(os.environ, env, clear=True):
            result = self._capture_tty(_notify.notify_terminal, "fir", "Ready for input")
        self.assertIn("777;", result)

    def test_write_to_tty_silently_skips_on_oserror(self):
        """_write_to_tty must not raise even if /dev/tty is unavailable."""
        with patch("builtins.open", side_effect=OSError("no tty")):
            _notify._write_to_tty(b"hello")  # should not raise


if __name__ == "__main__":
    unittest.main()
