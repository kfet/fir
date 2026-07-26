#!/usr/bin/env python3
"""Tests for the tmuxspinner builtin extension."""

import os
import sys
import threading
import time
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

# Import the module without triggering the global if-guard (no TMUX env).
# We test the Spinner class and helper functions directly.
if "tmuxspinner" in sys.modules:
    del sys.modules["tmuxspinner"]

# Ensure TMUX is NOT set so the module-level if-guard doesn't register handlers.
_orig_env = os.environ.copy()
for k in ("TMUX", "TMUX_PANE"):
    os.environ.pop(k, None)

with mock.patch.object(fir_ext, "run"):
    import tmuxspinner

# Keep lifecycle tests fast; production uses a one-second tmux title tick.
tmuxspinner.TICK_INTERVAL = 0.01

# Restore env.
os.environ.update(_orig_env)


def _echo_last_set(rename_mock):
    """Return a _read_window_name side-effect that echoes back the last name
    passed to the given _rename_window mock (mirroring real tmux). Without
    this, the spin loop's user-rename detection reads the *real* tmux window
    name when the suite runs inside an actual tmux pane and clobbers
    _original_name, making these lifecycle tests environment-dependent.
    """

    def _read(_target):
        if rename_mock.call_args:
            return rename_mock.call_args[0][1]
        return ""

    return _read


class TestStripSpinnerSuffix(unittest.TestCase):
    def test_no_suffix(self):
        self.assertEqual(tmuxspinner._strip_spinner_suffix("fir"), "fir")

    def test_empty_string(self):
        self.assertEqual(tmuxspinner._strip_spinner_suffix(""), "")

    def test_non_glyph_not_stripped(self):
        self.assertEqual(tmuxspinner._strip_spinner_suffix("fir A"), "fir A")

    def test_counter_suffix_no_match(self):
        self.assertEqual(tmuxspinner._strip_spinner_suffix("fir foo bar"), "fir foo bar")

    def test_strip_glyph_only(self):
        # Each of the spinner frames is peeled when preceded by a space.
        for frame in tmuxspinner.SPINNER_FRAMES:
            self.assertEqual(tmuxspinner._strip_spinner_suffix(f"fir {frame}"), "fir")
            self.assertEqual(tmuxspinner._strip_spinner_suffix(f"fir mysess {frame}"), "fir mysess")


class TestTrimAndFit(unittest.TestCase):
    def test_trim_to_width_unchanged_when_short(self):
        self.assertEqual(tmuxspinner._trim_to_width("short", 10), "short")

    def test_trim_to_width_adds_ellipsis(self):
        out = tmuxspinner._trim_to_width("abcdef", 4)
        self.assertEqual(out, "abc…")
        self.assertLessEqual(len(out), 4)

    def test_fit_title_preserves_status_drops_tab(self):
        title = tmuxspinner._fit_title("very-long-window-name-from-shell", "", "|")
        self.assertLessEqual(len(title), tmuxspinner.MAX_TITLE_LEN)
        self.assertTrue(title.endswith(" |"))
        self.assertIn("…", title)

    def test_fit_title_keeps_session_drops_tab(self):
        # Long tab, short session — tab should be trimmed/dropped, session kept whole.
        title = tmuxspinner._fit_title("very-long-tab-name", "ms", "|")
        self.assertLessEqual(len(title), tmuxspinner.MAX_TITLE_LEN)
        self.assertIn("ms |", title)

    def test_fit_title_three_part_fits(self):
        title = tmuxspinner._fit_title("fir", "sess", "|")
        self.assertEqual(title, "fir sess |")


class TestInTmux(unittest.TestCase):
    def test_in_tmux(self):
        with mock.patch.dict(os.environ, {"TMUX": "/tmp/tmux,123,0"}):
            self.assertTrue(tmuxspinner._in_tmux())

    def test_not_in_tmux(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertFalse(tmuxspinner._in_tmux())


class TestPaneId(unittest.TestCase):
    def test_pane_id(self):
        with mock.patch.dict(os.environ, {"TMUX_PANE": "%5"}):
            self.assertEqual(tmuxspinner._pane_id(), "%5")

    def test_pane_id_empty(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(tmuxspinner._pane_id(), "")


class TestRunTmux(unittest.TestCase):
    def test_success(self):
        with mock.patch("subprocess.run") as mock_run:
            mock_run.return_value = mock.MagicMock(stdout="result\n")
            out = tmuxspinner._run_tmux("display-message", "-p", "#W")
            mock_run.assert_called_once_with(
                ["tmux", "display-message", "-p", "#W"],
                capture_output=True,
                text=True,
                timeout=5,
            )
            self.assertEqual(out, "result")

    def test_exception(self):
        with mock.patch("subprocess.run", side_effect=FileNotFoundError):
            out = tmuxspinner._run_tmux("display-message")
            self.assertEqual(out, "")


class TestSpinnerBasic(unittest.TestCase):
    """Test Spinner methods without actually running the spin loop."""

    def test_display_name_no_session(self):
        s = tmuxspinner.Spinner()
        s._original_name = "fir"
        self.assertEqual(s._display_name(), "fir")

    def test_display_name_with_session(self):
        s = tmuxspinner.Spinner()
        s._original_name = "fir"
        s._session_name = "mysess"
        self.assertEqual(s._display_name(), "fir mysess")

    def test_set_session_name_strips_old_suffix(self):
        s = tmuxspinner.Spinner()
        s._pane_id = "%1"  # skip _init_pane
        s._original_name = "fir old-sess"
        s._session_name = "old-sess"
        with mock.patch.object(tmuxspinner, "_rename_window"):
            s.set_session_name("new-sess")
        self.assertEqual(s._original_name, "fir")
        self.assertEqual(s._session_name, "new-sess")

    def test_set_session_name_strips_new_suffix_duplicate(self):
        s = tmuxspinner.Spinner()
        s._pane_id = "%1"
        s._original_name = "fir mysess"
        s._session_name = ""
        with mock.patch.object(tmuxspinner, "_rename_window"):
            s.set_session_name("mysess")
        self.assertEqual(s._original_name, "fir")

    def test_set_session_name_renames_when_idle(self):
        s = tmuxspinner.Spinner()
        s._pane_id = "%1"
        s._original_name = "fir"
        s._running = False
        with mock.patch.object(tmuxspinner, "_rename_window") as mock_rn:
            s.set_session_name("test")
            mock_rn.assert_called_once_with("%1", "fir test")

    def test_set_session_name_no_rename_when_running(self):
        s = tmuxspinner.Spinner()
        s._pane_id = "%1"
        s._original_name = "fir"
        s._running = True
        with mock.patch.object(tmuxspinner, "_rename_window") as mock_rn:
            s.set_session_name("test")
            mock_rn.assert_not_called()


class TestSpinnerRender(unittest.TestCase):
    def test_render_title_basic(self):
        s = tmuxspinner.Spinner()
        s._original_name = "fir"
        self.assertEqual(s._render_title(), f"fir {tmuxspinner.SPINNER_FRAMES[0]}")

    def test_render_title_with_session(self):
        s = tmuxspinner.Spinner()
        s._original_name = "fir"
        s._session_name = "mysess"
        self.assertEqual(s._render_title(), f"fir mysess {tmuxspinner.SPINNER_FRAMES[0]}")

    def test_render_title_drops_tab_first_when_overflow(self):
        # Session + status fits, but with the tab the title would overflow.
        s = tmuxspinner.Spinner()
        s._original_name = "very-long-window-name-from-shell"  # 32 chars
        s._session_name = "mysess-with-extra-padding-chars"
        title = s._render_title()
        self.assertLessEqual(len(title), tmuxspinner.MAX_TITLE_LEN)
        # Session and status must survive; tab is trimmed or dropped.
        self.assertTrue(title.endswith(" " + tmuxspinner.SPINNER_FRAMES[0]))

    def test_render_title_trims_long_base(self):
        s = tmuxspinner.Spinner()
        s._original_name = "very-long-window-name-from-shell"
        title = s._render_title()
        self.assertLessEqual(len(title), tmuxspinner.MAX_TITLE_LEN)
        self.assertTrue(title.endswith(" " + tmuxspinner.SPINNER_FRAMES[0]))

    def test_render_title_advances_glyph(self):
        s = tmuxspinner.Spinner()
        s._original_name = "fir"
        with s._lock:
            t1 = s._title_locked(advance_frame=True)
            t2 = s._title_locked(advance_frame=True)
        self.assertEqual(t1[-1], tmuxspinner.SPINNER_FRAMES[1])
        self.assertEqual(t2[-1], tmuxspinner.SPINNER_FRAMES[2])


class TestSpinnerStartStop(unittest.TestCase):
    """Test start/stop lifecycle with mocked tmux calls."""

    def test_start_stop_cycle(self):
        s = tmuxspinner.Spinner()
        s._pane_id = "%1"
        s._original_name = "fir"

        with (
            mock.patch.object(tmuxspinner, "_rename_window") as mock_rn,
            mock.patch.object(
                tmuxspinner, "_read_window_name", side_effect=_echo_last_set(mock_rn)
            ),
        ):
            s.start()
            self.assertTrue(s._running)
            # Let spinner loop iterate at least once.
            time.sleep(tmuxspinner.TICK_INTERVAL * 2)
            s.stop()
            self.assertFalse(s._running)
            # Last rename should restore the display name (original + session).
            last_call = mock_rn.call_args
            self.assertEqual(last_call[0], ("%1", "fir"))

    def test_stop_restores_display_name_with_session(self):
        s = tmuxspinner.Spinner()
        s._pane_id = "%1"
        s._original_name = "fir"
        s._session_name = "mysess"

        with (
            mock.patch.object(tmuxspinner, "_rename_window") as mock_rn,
            mock.patch.object(
                tmuxspinner, "_read_window_name", side_effect=_echo_last_set(mock_rn)
            ),
        ):
            s.start()
            time.sleep(tmuxspinner.TICK_INTERVAL * 2)
            s.stop()
            last_call = mock_rn.call_args
            # stop() keeps session name in display
            self.assertEqual(last_call[0], ("%1", "fir mysess"))

    def test_start_noop_without_pane(self):
        s = tmuxspinner.Spinner()
        with mock.patch.object(tmuxspinner, "_pane_id", return_value=""):
            s.start()
            self.assertFalse(s._running)

    def test_start_noop_if_already_running(self):
        s = tmuxspinner.Spinner()
        s._pane_id = "%1"
        s._original_name = "fir"
        with mock.patch.object(tmuxspinner, "_rename_window"):
            s.start()
            thread1 = s._thread
            s.start()  # second call should be a no-op
            self.assertIs(s._thread, thread1)
            s.stop()

    def test_stop_noop_if_not_running(self):
        s = tmuxspinner.Spinner()
        # Should not raise.
        s.stop()


class TestSpinnerDetectsUserRename(unittest.TestCase):
    def test_user_rename_updates_original(self):
        s = tmuxspinner.Spinner()
        s._pane_id = "%1"
        s._original_name = "fir"

        rename_calls = []
        renamed_event = threading.Event()

        def fake_rename(target, name):
            rename_calls.append(name)
            renamed_event.set()

        read_count = [0]
        user_renamed_seen = threading.Event()

        def fake_read(target):
            read_count[0] += 1
            if read_count[0] >= 2:
                user_renamed_seen.set()
                return "user-renamed"
            return rename_calls[-1] if rename_calls else "fir"

        with mock.patch.object(tmuxspinner, "_rename_window", side_effect=fake_rename):
            with mock.patch.object(tmuxspinner, "_read_window_name", side_effect=fake_read):
                s.start()
                # Wait deterministically for the loop to observe the user rename.
                self.assertTrue(user_renamed_seen.wait(timeout=2.0))
                # Give the loop one more iteration to apply the update.
                time.sleep(tmuxspinner.TICK_INTERVAL * 3)
                s.stop()
                self.assertEqual(s._original_name, "user-renamed")


class TestModuleLevelGuard(unittest.TestCase):
    """Verify that handlers are only registered when inside tmux with a tty."""

    def test_no_handlers_without_tmux(self):
        saved = dict(fir_ext._event_handlers)
        fir_ext._event_handlers.clear()
        try:
            if "tmuxspinner" in sys.modules:
                del sys.modules["tmuxspinner"]
            with mock.patch.dict(os.environ, {}, clear=True):
                with mock.patch.object(fir_ext, "run"):
                    pass
            # No event handlers should be registered.
            for key in ("agent_start", "agent_end", "session_shutdown", "session_named"):
                self.assertNotIn(
                    key, fir_ext._event_handlers, f"{key} should not be registered without TMUX"
                )
        finally:
            fir_ext._event_handlers.clear()
            fir_ext._event_handlers.update(saved)


class TestHasControllingTerminal(unittest.TestCase):
    def test_with_tty(self):
        with mock.patch("os.open", return_value=3), mock.patch("os.close"):
            self.assertTrue(tmuxspinner._has_controlling_terminal())

    def test_without_tty(self):
        with mock.patch("os.open", side_effect=OSError("no tty")):
            self.assertFalse(tmuxspinner._has_controlling_terminal())


class TestSpinnerShutdown(unittest.TestCase):
    """Test shutdown() restores original name and cleans up stashed state."""

    def test_shutdown_restores_original_name(self):
        s = tmuxspinner.Spinner()
        s._pane_id = "%1"
        s._original_name = "zsh"
        s._session_name = "mysess"

        with (
            mock.patch.object(tmuxspinner, "_rename_window") as mock_rn,
            mock.patch.object(tmuxspinner, "_unset_window_option") as mock_unset,
            mock.patch.object(
                tmuxspinner, "_read_window_name", side_effect=_echo_last_set(mock_rn)
            ),
        ):
            s.start()
            time.sleep(tmuxspinner.TICK_INTERVAL * 2)
            s.shutdown()
            self.assertFalse(s._running)
            # Should restore original name WITHOUT session suffix.
            last_rename = mock_rn.call_args
            self.assertEqual(last_rename[0], ("%1", "zsh"))
            # Should unset the stashed option.
            mock_unset.assert_called_once_with("%1", "@fir_original_name")

    def test_shutdown_when_not_running(self):
        s = tmuxspinner.Spinner()
        s._pane_id = "%1"
        s._original_name = "zsh"
        s._session_name = "mysess"

        with (
            mock.patch.object(tmuxspinner, "_rename_window") as mock_rn,
            mock.patch.object(tmuxspinner, "_unset_window_option") as mock_unset,
        ):
            s.shutdown()
            mock_rn.assert_called_once_with("%1", "zsh")
            mock_unset.assert_called_once_with("%1", "@fir_original_name")

    def test_shutdown_noop_without_pane(self):
        s = tmuxspinner.Spinner()
        # No pane set — should not raise.
        with mock.patch.object(tmuxspinner, "_rename_window") as mock_rn:
            s.shutdown()
            mock_rn.assert_not_called()


class TestStashRecovery(unittest.TestCase):
    """Test that _init_pane recovers stashed name from crashed sessions."""

    def test_recovers_stashed_name(self):
        s = tmuxspinner.Spinner()
        with (
            mock.patch.dict(os.environ, {"TMUX_PANE": "%1"}),
            mock.patch.object(tmuxspinner, "_disable_auto_rename"),
            mock.patch.object(tmuxspinner, "_get_window_option", return_value="zsh"),
            mock.patch.object(tmuxspinner, "_set_window_option") as mock_set,
        ):
            with s._lock:
                s._init_pane()
            self.assertEqual(s._original_name, "zsh")
            # Should re-stash.
            mock_set.assert_called_once_with("%1", "@fir_original_name", "zsh")

    def test_reads_window_name_when_no_stash(self):
        s = tmuxspinner.Spinner()
        with (
            mock.patch.dict(os.environ, {"TMUX_PANE": "%1"}),
            mock.patch.object(tmuxspinner, "_disable_auto_rename"),
            mock.patch.object(tmuxspinner, "_get_window_option", return_value=""),
            mock.patch.object(tmuxspinner, "_read_window_name", return_value="bash"),
            mock.patch.object(tmuxspinner, "_set_window_option") as mock_set,
        ):
            with s._lock:
                s._init_pane()
            self.assertEqual(s._original_name, "bash")
            mock_set.assert_called_once_with("%1", "@fir_original_name", "bash")


if __name__ == "__main__":
    unittest.main()
