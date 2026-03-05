#!/usr/bin/env python3
# ---
# name: tmuxspinner
# description: Animate a spinner in the tmux window name while the agent is working
# builtin: true
# modes: tui
# ---
"""Animate a spinner in the tmux window name while the agent is working.

When the agent is idle, the original window name is restored.
Uses `tmux rename-window` to set the window name.
No-op when not running inside tmux ($TMUX unset).

This is a Python port of pkg/extensions/tmuxspinner.
"""

import atexit
import os
import signal
import subprocess
import threading

import fir_ext

FRAMES = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
SPIN_INTERVAL = 0.15  # seconds


def _in_tmux():
    return os.environ.get("TMUX", "") != ""


def _pane_id():
    return os.environ.get("TMUX_PANE", "")


def _run_tmux(*args):
    try:
        result = subprocess.run(["tmux", *list(args)], capture_output=True, text=True, timeout=5)
        return result.stdout.strip()
    except Exception:
        return ""


def _read_window_name(target):
    return _run_tmux("display-message", "-t", target, "-p", "#W")


def _rename_window(target, name):
    _run_tmux("rename-window", "-t", target, name)


def _disable_auto_rename(target):
    _run_tmux("set-window-option", "-t", target, "automatic-rename", "off")


def _strip_spinner_suffix(name):
    """Remove trailing ' <braille>' suffixes."""
    while len(name) >= 2:
        last = name[-1]
        if name[-2] == " " and "\u2800" <= last <= "\u28ff":
            name = name[:-2]
        else:
            break
    return name


class Spinner:
    def __init__(self):
        self._lock = threading.Lock()
        self._pane_id = ""
        self._original_name = ""  # actual tmux window name (before fir touched it)
        self._session_name = ""  # fir session name to append
        self._stop_event = None
        self._thread = None
        self._running = False

    def _display_name(self):
        """Compute display name: original window name with session name appended."""
        if self._session_name:
            return f"{self._original_name} {self._session_name}"
        return self._original_name

    def _init_pane(self):
        """Initialise pane ID and original window name if not done yet. Caller holds lock."""
        if not self._pane_id:
            self._pane_id = _pane_id()
            if self._pane_id:
                _disable_auto_rename(self._pane_id)
                self._original_name = _strip_spinner_suffix(
                    _read_window_name(self._pane_id) or "fir"
                )

    def set_session_name(self, name):
        """Append the fir session name to the window name."""
        with self._lock:
            self._init_pane()
            self._session_name = name
            if self._pane_id and not self._running:
                _rename_window(self._pane_id, self._display_name())

    def start(self):
        with self._lock:
            if self._running:
                return
            self._init_pane()
            if not self._pane_id:
                return

            self._stop_event = threading.Event()
            self._running = True
            self._thread = threading.Thread(target=self._loop, daemon=True)
            self._thread.start()

    def stop(self):
        with self._lock:
            if not self._running:
                return
            assert self._stop_event is not None
            self._stop_event.set()
            self._running = False
            thread = self._thread
            pane = self._pane_id
            base = self._display_name()

        # Wait for the spinner loop to finish so no rename races with us.
        if thread:
            thread.join(timeout=2)

        # Now restore the window name — guaranteed no more loop renames.
        if pane:
            _rename_window(pane, base)

    def _loop(self):
        assert self._stop_event is not None
        i = 0
        last_set = ""
        while not self._stop_event.wait(SPIN_INTERVAL):
            # Re-check after waking — stop() may have been called during the wait.
            if self._stop_event.is_set():
                break

            with self._lock:
                target = self._pane_id

            # Detect user renames: if the window name changed from what we last set,
            # update the original name (stripping any spinner suffix we may have added).
            if last_set:
                current = _read_window_name(target)
                if current and current != last_set:
                    with self._lock:
                        self._original_name = _strip_spinner_suffix(current)

            with self._lock:
                base = self._display_name()

            name = f"{base} {FRAMES[i % len(FRAMES)]}"
            _rename_window(target, name)
            last_set = name
            i += 1


def _has_controlling_terminal():
    """Check if the process has a controlling terminal."""
    try:
        fd = os.open("/dev/tty", os.O_RDONLY | os.O_NOCTTY)
        os.close(fd)
        return True
    except OSError:
        return False


# Only activate if inside tmux AND we have a controlling terminal.
# When fir is spawned as a subprocess (e.g. ACP mode), there's no
# controlling terminal so the spinner stays dormant.
if _in_tmux() and _has_controlling_terminal():
    _spinner = Spinner()

    # Safety net: restore window name on exit regardless of how we shut down.
    atexit.register(lambda: _spinner.stop())

    def _sigterm_handler(signum, frame):
        _spinner.stop()
        # Re-raise so the process actually exits
        signal.signal(signal.SIGTERM, signal.SIG_DFL)
        os.kill(os.getpid(), signal.SIGTERM)

    signal.signal(signal.SIGTERM, _sigterm_handler)

    @fir_ext.on("agent_start")
    def on_agent_start(params, ctx):
        _spinner.start()

    @fir_ext.on("agent_end")
    def on_agent_end(params, ctx):
        _spinner.stop()

    @fir_ext.on("session_shutdown")
    def on_session_shutdown(params, ctx):
        _spinner.stop()

    @fir_ext.on("session_named")
    def on_session_named(params, ctx):
        name = (params or {}).get("name", "")
        if name:
            _spinner.set_session_name(name)


fir_ext.run(name="tmuxspinner")
