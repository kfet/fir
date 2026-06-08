#!/usr/bin/env python3
# ---
# name: tmuxspinner
# description: Show agent work status in the tmux window name
# builtin: true
# modes: tui
# ---
"""Show agent work status in the tmux window name while the agent is working.

When the agent is idle, the original window name is restored.
Uses `tmux rename-window` to set the window name.
No-op when not running inside tmux ($TMUX unset).

Title layout while running:

    {tab} {session} {glyph}

A box-drawing spinner glyph (4 frames at codepoints U+2502, U+2571,
U+2500, U+2572 — vertical, diagonal-up, horizontal, diagonal-down)
cycles once per tick (1 Hz) as a peripheral-vision liveness cue. The Box
Drawing block is the one Unicode range that NVDA, VoiceOver, JAWS, and
Orca all commonly categorise as "drawing characters" and skip at normal
verbosity, so a11y stays quiet. ASCII alternatives with a backslash frame
don't survive tmux's `rename-window`: tmux runs the title through
`strvis(3)` for terminal-escape-injection defense, which encodes a literal
backslash as two backslashes (and cascades on every format-expand pass -
tmux issue #2070).

When the composed title exceeds MAX_TITLE_LEN columns, parts are dropped
in priority order: tab first, then session; the glyph is always preserved.
"""

import atexit
import os
import signal
import subprocess
import threading

import fir_ext

TICK_INTERVAL = 1.0
MAX_TITLE_LEN = 30
SPINNER_FRAMES = ("│", "╱", "─", "╲")  # noqa: RUF001


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


def _get_window_option(target, option):
    return _run_tmux("show-window-option", "-t", target, "-v", option)


def _set_window_option(target, option, value):
    _run_tmux("set-window-option", "-t", target, option, value)


def _unset_window_option(target, option):
    _run_tmux("set-window-option", "-t", target, "-u", option)


def _trim_to_width(s, max_w):
    """Trim to at most max_w columns, appending an ellipsis if trimmed.

    Assumes ASCII / narrow chars — `len()` is used as the width function.
    """
    if max_w <= 0:
        return ""
    if len(s) <= max_w:
        return s
    ellipsis = "…"
    if max_w <= len(ellipsis):
        return ellipsis
    return s[: max_w - len(ellipsis)] + ellipsis


def _fit_title(tab, session, status, max_len=MAX_TITLE_LEN):
    """Compose '<tab> <session> <status>' within max_len columns.

    Drop priority (lowest first): tab, then session. The status block is
    always preserved; only truncated if it alone exceeds max_len.
    """
    if len(status) >= max_len:
        return _trim_to_width(status, max_len)

    # Try increasingly-stripped prefixes. For each, fit-whole or trim the
    # leftmost (lowest-priority) piece. If trimming would leave no room,
    # drop the piece entirely and try the next combination.
    for parts in ((tab, session, status), (session, status), (status,)):
        parts = [p for p in parts if p]
        if not parts:
            continue
        candidate = " ".join(parts)
        if len(candidate) <= max_len:
            return candidate
        if len(parts) >= 2:
            fixed = " ".join(parts[1:])
            avail = max_len - len(fixed) - 1
            if avail > 0:
                trimmed = _trim_to_width(parts[0], avail)
                if trimmed:
                    return f"{trimmed} {fixed}"
    return status


def _strip_spinner_suffix(name):
    """Strip the trailing ' <glyph>' suffix we may have appended."""
    if len(name) >= 2 and name[-2] == " " and name[-1] in SPINNER_FRAMES:
        name = name[:-2]
    return name


class Spinner:
    def __init__(self):
        self._lock = threading.Lock()
        self._pane_id = ""
        self._original_name = ""  # actual tmux window name (tab) before fir touched it
        self._session_name = ""  # fir session name
        self._frame_idx = 0
        self._last_set = ""
        self._stop_event = None
        self._thread = None
        self._running = False

    def _display_name(self):
        """Idle display (no status block): tab + session, space-joined."""
        if self._session_name and self._original_name:
            return f"{self._original_name} {self._session_name}"
        return self._session_name or self._original_name

    def _init_pane(self):
        """Initialise pane ID and original window name if not done yet. Caller holds lock."""
        if not self._pane_id:
            self._pane_id = _pane_id()
            if self._pane_id:
                _disable_auto_rename(self._pane_id)
                # Recover stashed name from a previous session that crashed/was killed.
                stashed = _get_window_option(self._pane_id, "@fir_original_name")
                if stashed:
                    self._original_name = _strip_spinner_suffix(stashed)
                else:
                    self._original_name = _strip_spinner_suffix(
                        _read_window_name(self._pane_id) or "fir"
                    )
                # Stash for crash recovery by future sessions.
                _set_window_option(self._pane_id, "@fir_original_name", self._original_name)

    def _title_locked(self, advance_frame=False):
        if advance_frame:
            self._frame_idx = (self._frame_idx + 1) % len(SPINNER_FRAMES)
        status = SPINNER_FRAMES[self._frame_idx]
        return _fit_title(self._original_name, self._session_name, status)

    def _render_title(self):
        """Render the current title. Intended for tests; does not rename tmux."""
        with self._lock:
            return self._title_locked()

    def _rename_to_current_title(self, advance_frame=True):
        with self._lock:
            target = self._pane_id
            if not target:
                return ""
            name = self._title_locked(advance_frame=advance_frame)

        _rename_window(target, name)
        with self._lock:
            self._last_set = name
        return name

    def set_session_name(self, name):
        """Update the fir session name component of the window title."""
        with self._lock:
            self._init_pane()
            old = self._session_name
            self._session_name = name

            # If the stashed/recovered original_name ends with the OLD or
            # NEW session suffix (e.g. crash recovery from a previous fir
            # process that had baked the session name into the window), peel
            # it off so _display_name() doesn't double-print it.
            for suffix in (old, name):
                if suffix and self._original_name.endswith(" " + suffix):
                    self._original_name = self._original_name[: -(len(suffix) + 1)]

            pane = self._pane_id
            should_rename = pane and not self._running
            display_name = self._display_name()

        if should_rename:
            _rename_window(pane, display_name)

    def start(self):
        with self._lock:
            if self._running:
                return
            self._init_pane()
            if not self._pane_id:
                return

            self._frame_idx = 0
            self._last_set = ""
            self._stop_event = threading.Event()
            self._running = True
            self._thread = threading.Thread(target=self._loop, daemon=True)
            self._thread.start()

        # Show status promptly instead of waiting for the first one-second tick.
        self._rename_to_current_title(advance_frame=False)

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
            self._last_set = ""

        # Wait for the ticker loop to finish so no rename races with us.
        if thread:
            thread.join(timeout=5)

        # Now restore the window name — guaranteed no more loop renames.
        if pane:
            _rename_window(pane, base)

    def shutdown(self):
        """Full cleanup: stop ticker, restore original name, remove stashed state."""
        # Stop the ticker loop first.
        with self._lock:
            if self._running:
                assert self._stop_event is not None
                self._stop_event.set()
                self._running = False
                thread = self._thread
            else:
                thread = None
            pane = self._pane_id
            original = self._original_name
            self._last_set = ""

        if thread:
            thread.join(timeout=5)

        if pane:
            _rename_window(pane, original)
            _unset_window_option(pane, "@fir_original_name")

    def _loop(self):
        assert self._stop_event is not None
        while not self._stop_event.wait(TICK_INTERVAL):
            with self._lock:
                target = self._pane_id
                last_set = self._last_set

            # Detect user renames: if the window name changed from what we last set,
            # update the original name (stripping any status suffix we may have added).
            if target and last_set:
                current = _read_window_name(target)
                if current and current != last_set:
                    with self._lock:
                        self._original_name = _strip_spinner_suffix(current)

            # If shutdown/stop signalled us while we were mid-tick, do not
            # emit another rename — otherwise it can land AFTER the restore
            # rename and leave the window showing a stale spinner frame.
            if self._stop_event.is_set():
                break
            self._rename_to_current_title()


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
# controlling terminal so the status updater stays dormant.
if _in_tmux() and _has_controlling_terminal():
    _spinner = Spinner()

    # Safety net: restore window name on exit regardless of how we shut down.
    atexit.register(lambda: _spinner.shutdown())

    def _sigterm_handler(signum, frame):
        _spinner.shutdown()
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
        _spinner.shutdown()

    @fir_ext.on("session_named")
    def on_session_named(params, ctx):
        name = (params or {}).get("name", "")
        _spinner.set_session_name(name)


fir_ext.run(name="tmuxspinner")
