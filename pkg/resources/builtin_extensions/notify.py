#!/usr/bin/env python3
# ---
# name: notify
# description: Send native terminal notifications when the agent finishes
# builtin: true
# modes: tui
# ---
"""Send native terminal notifications when the agent finishes.

Supports multiple terminal protocols:
  - OSC 777: Ghostty, iTerm2, WezTerm, rxvt-unicode
  - OSC 99: Kitty

When inside tmux, wraps sequences in DCS passthrough format.

This is a Python port of pkg/extensions/notify.
"""

import os

import fir_ext


def _in_tmux():
    return os.environ.get("TMUX", "") != ""


def _in_kitty():
    return os.environ.get("KITTY_WINDOW_ID", "") != ""


def _write_to_tty(data: bytes) -> None:
    """Write bytes directly to /dev/tty, bypassing any captured pipes."""
    try:
        with open("/dev/tty", "wb", buffering=0) as tty:
            tty.write(data)
    except OSError:
        pass  # no controlling terminal (e.g. headless CI) — silently skip


def _notify_osc777(title: str, body: str):
    """OSC 777 notification (Ghostty, iTerm2, WezTerm, rxvt-unicode)."""
    if _in_tmux():
        _write_to_tty(f"\x1bPtmux;\x1b\x1b]777;notify;{title};{body}\x1b\x1b\\\x1b\\".encode())
    else:
        _write_to_tty(f"\x1b]777;notify;{title};{body}\x1b\\".encode())


def _notify_osc99(title: str, body: str):
    """Kitty OSC 99 notification."""
    if _in_tmux():
        _write_to_tty(f"\x1bPtmux;\x1b\x1b]99;i=1:d=0;{title}\x1b\x1b\\\x1b\\".encode())
        _write_to_tty(f"\x1bPtmux;\x1b\x1b]99;i=1:p=body;{body}\x1b\x1b\\\x1b\\".encode())
    else:
        _write_to_tty(f"\x1b]99;i=1:d=0;{title}\x1b\\".encode())
        _write_to_tty(f"\x1b]99;i=1:p=body;{body}\x1b\\".encode())


def notify_terminal(title: str, body: str):
    if _in_kitty():
        _notify_osc99(title, body)
    else:
        _notify_osc777(title, body)


_session_name = ""


@fir_ext.on("session_named")
def on_session_named(params, ctx):
    global _session_name
    _session_name = (params or {}).get("name", "")


@fir_ext.on("agent_end")
def on_agent_end(params, ctx):
    title = f"fir — {_session_name}" if _session_name else "fir"
    notify_terminal(title, "Ready for input")


fir_ext.run(name="notify")
