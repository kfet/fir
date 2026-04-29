#!/usr/bin/env python3
# ---
# name: observe
# description: Per-session observation hooks — writes a sidecar with the
#   transcript path and accepts user-input messages on a Unix socket so
#   external observers (`fir observe` / `fir send`) can tail the session
#   and inject prompts.
# builtin: true
# ---
"""fir observe — per-session observation extension.

Two responsibilities, both per-session (one observe.py instance per ACP /
interactive / print session):

1. **Sidecar** — at session_start, write
   ``$XDG_STATE_HOME/fir/agents/<session-id>.json`` with discovery
   metadata: pid, socket_path, store_path (the JSONL transcript), cwd,
   started_at, status, session_name. Atomic-rewrite on lifecycle
   events. Persists past session_shutdown for post-mortem.

2. **Socket** — at session_start, bind a Unix socket at
   ``<runtime-dir>/fir/observe/<session-id-prefix>.sock`` (mode 0600).
   Accept connections; each connection sends NDJSON lines like
   ``{"deliver_as": "", "content": "..."}`` which we forward to fir
   via ``send_user_message``.

The transcript file (announced via store_path in the sidecar) is the
buffer, the replay, the live tail, and the post-mortem record — all
provided by the kernel + filesystem. See docs/design/observe.md.
"""

from __future__ import annotations

import contextlib
import json
import os
import socket
import sys
import threading
import time
from pathlib import Path
from typing import Any

import fir_ext

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------


def _state_dir() -> Path:
    """Sidecar dir — $XDG_STATE_HOME/fir/agents/ (default ~/.local/state/fir/agents/)."""
    base = os.environ.get("XDG_STATE_HOME") or str(Path.home() / ".local" / "state")
    return Path(base) / "fir" / "agents"


def _socket_dir() -> Path:
    """Socket dir — $FIR_OBSERVE_DIR > $XDG_RUNTIME_DIR > $TMPDIR > $HOME/.fir-tmp."""
    for env in ("FIR_OBSERVE_DIR", "XDG_RUNTIME_DIR", "TMPDIR"):
        v = os.environ.get(env)
        if v:
            return Path(v) / "fir" / "observe"
    # Last-resort fallback. Avoid /tmp on multi-user boxes; prefer per-user
    # dir under $HOME so the parent can be 0700 without permission collisions.
    return Path.home() / ".fir-tmp" / "fir" / "observe"


# Unix-domain socket sun_path is capped at ~104 bytes on macOS / 108 on Linux.
# A full 36-char UUID + TMPDIR prefix can blow the cap, so we use a 16-char
# (64-bit) prefix in the filename. Sidecar carries the full id + absolute
# socket_path so observers don't reconstruct.
_SOCKET_ID_PREFIX_LEN = 16


def _sidecar_path(session_id: str) -> Path:
    return _state_dir() / f"{session_id}.json"


def _socket_path(session_id: str) -> Path:
    return _socket_dir() / f"{session_id[:_SOCKET_ID_PREFIX_LEN]}.sock"


def _is_safe_session_id(sid: str) -> bool:
    """Reject session ids that could escape the agents/ directory.

    fir generates UUIDs; this defensive check stops a misbehaving bridge
    from injecting path traversal characters as a filename component.
    """
    if not sid or len(sid) > 128:
        return False
    # Allow only chars that are unambiguous in filenames on every fs we
    # care about (UUIDs satisfy this trivially).
    return all(c.isalnum() or c in "-_" for c in sid)


# ---------------------------------------------------------------------------
# Per-session state (one extension process == one session)
# ---------------------------------------------------------------------------

_state_lock = threading.Lock()
_state: dict[str, Any] = {
    "session_id": "",
    "pid": os.getpid(),
    "socket_path": "",
    "store_path": "",
    "cwd": "",
    "started_at": "",
    "status": "running",
    "session_name": "",
    "schema": 1,
}
_socket: socket.socket | None = None
_accept_thread: threading.Thread | None = None
_shutdown = threading.Event()


# ---------------------------------------------------------------------------
# Sidecar — atomic write
# ---------------------------------------------------------------------------


def _write_sidecar() -> None:
    """Atomically write ``_state`` to the sidecar file."""
    sid = _state["session_id"]
    if not sid:
        return
    path = _sidecar_path(sid)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with _state_lock:
        snapshot = dict(_state)
    tmp = path.with_suffix(".json.tmp")
    tmp.write_text(json.dumps(snapshot, indent=2) + "\n")
    os.chmod(tmp, 0o600)
    os.replace(tmp, path)


def _update_state(**kwargs: Any) -> None:
    """Apply updates and rewrite the sidecar."""
    with _state_lock:
        _state.update(kwargs)
    _write_sidecar()


# ---------------------------------------------------------------------------
# Socket — accept loop
# ---------------------------------------------------------------------------


def _bind_socket(path: Path) -> socket.socket | None:
    """Bind a Unix socket at path with mode 0600. Returns None on failure."""
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    # Remove any stale socket from a previous crash. Session ids are unique
    # so this can't race with a live peer.
    with contextlib.suppress(FileNotFoundError):
        path.unlink()
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        sock.bind(str(path))
        os.chmod(str(path), 0o600)
        sock.listen(8)
    except OSError as e:
        sock.close()
        # Best-effort logging to stderr for ops visibility.
        print(f"observe: bind {path} failed: {e}", file=sys.stderr)
        return None
    return sock


def _accept_loop(sock: socket.socket, ctx: fir_ext.Context) -> None:
    """Accept connections; spawn one daemon thread per connection."""
    sock.settimeout(0.5)  # so we can check _shutdown periodically
    while not _shutdown.is_set():
        try:
            conn, _ = sock.accept()
        except socket.timeout:
            continue
        except OSError:
            return  # socket closed
        threading.Thread(
            target=_handle_conn, args=(conn, ctx), daemon=True
        ).start()


def _handle_conn(conn: socket.socket, ctx: fir_ext.Context) -> None:
    """Read NDJSON lines from a connection; forward to send_user_message."""
    try:
        with conn, conn.makefile("r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    msg = json.loads(line)
                except json.JSONDecodeError:
                    continue  # skip malformed lines
                content = msg.get("content", "")
                if not isinstance(content, str) or not content:
                    continue
                deliver_as = msg.get("deliver_as", "")
                if deliver_as not in ("", "steer", "followUp"):
                    deliver_as = ""
                # Forward; failure is logged and we continue reading.
                try:
                    ctx.send_user_message(content, deliver_as=deliver_as)
                except Exception as e:
                    print(f"observe: send_user_message failed: {e}", file=sys.stderr)
    except Exception as e:
        print(f"observe: connection handler exited: {e}", file=sys.stderr)


# ---------------------------------------------------------------------------
# Lifecycle
# ---------------------------------------------------------------------------


@fir_ext.on("session_start")
def on_session_start(params: dict[str, Any], ctx: fir_ext.Context) -> None:
    global _socket, _accept_thread

    sid = params.get("session_id", "") if params else ""
    if not sid:
        return  # nothing to register against
    # Defensive: session_id is filename input. Reject anything that could
    # escape the agents/ dir or otherwise be path-injecting. fir's own ids
    # are UUIDs; this only matters if a malicious bridge sends garbage.
    if not _is_safe_session_id(sid):
        print(f"observe: refusing unsafe session_id: {sid!r}", file=sys.stderr)
        return

    store_path = ctx.get_session_file()  # absolute path or "" for in-memory
    if not store_path:
        # In-memory session — no transcript to tail; sidecar would be useless.
        return

    sock_path = _socket_path(sid)

    _update_state(
        session_id=sid,
        socket_path=str(sock_path),
        store_path=store_path,
        cwd=os.getcwd(),
        started_at=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        status="running",
        session_name=ctx.get_session_name(),
    )

    sock = _bind_socket(sock_path)
    if sock is None:
        return
    _socket = sock
    _accept_thread = threading.Thread(
        target=_accept_loop, args=(sock, ctx), daemon=True
    )
    _accept_thread.start()


@fir_ext.on("session_named")
def on_session_named(params: dict[str, Any], ctx: fir_ext.Context) -> None:
    name = ""
    if params:
        name = params.get("name", "") or ""
    _update_state(session_name=name)


@fir_ext.on("agent_start")
def on_agent_start(params: dict[str, Any], ctx: fir_ext.Context) -> None:
    _update_state(status="running")


@fir_ext.on("agent_end")
def on_agent_end(params: dict[str, Any], ctx: fir_ext.Context) -> None:
    _update_state(status="idle")


@fir_ext.on("session_shutdown")
def on_session_shutdown(params: dict[str, Any], ctx: fir_ext.Context) -> None:
    global _socket
    _shutdown.set()
    # Mark sidecar ended (don't unlink — observers want post-mortem access).
    _update_state(status="ended")
    # Close + unlink the socket — ephemeral and useless once the session is over.
    if _socket is not None:
        with contextlib.suppress(Exception):
            _socket.close()
        _socket = None
    sid = _state.get("session_id", "")
    if sid:
        with contextlib.suppress(FileNotFoundError, OSError):
            _socket_path(sid).unlink()


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

fir_ext.run(name="observe")
