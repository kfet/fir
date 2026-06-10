#!/usr/bin/env python3
# forkserver.py — COW fork template for fir Python extensions.
#
# NOT an extension. fir starts ONE of these per fir process per SDK hash —
# a process-lifetime singleton shared by every session in that process (one
# `fir --mode acp` process owns many sessions). It imports the
# fir_ext SDK + stdlib exactly once (the ~7MB interpreter heap), then waits
# in a single-threaded, unbuffered, newline-JSON control loop on stdin. On a
# "spawn" request it os.fork()s; the child inherits the warm heap copy-on-write
# (Shared_Clean), reconnects its stdio to a per-extension unix socket, and runs
# the unchanged extension script via runpy. This collapses ~7MB x N toward one
# shared heap and skips re-importing the SDK on every spawn.
#
# Fork-safety: nothing here opens threads/sockets/loops before fork. The only
# import is fir_ext (+ stdlib), and it must stay that way — never import an
# extension into the template, or its module-level registries would leak into
# every child.
#
# Control protocol (one JSON object per line, fir <-> forkserver):
#   fir -> {"id":N,"cmd":"spawn","path":"/abs/ext.py","sock":"/tmp/x.sock","env":{...}}
#   srv -> {"id":N,"pid":P}              (or {"id":N,"error":"..."})
#   fir -> {"id":N,"cmd":"stop","pid":P}
#   srv -> {"id":N,"reaped":true}
#   fir -> {"id":N,"cmd":"shutdown"}
#   srv -> {"id":N,"ok":true}            (then exits)
# On startup the server emits {"ready":true} once fir_ext imported cleanly.
# argv[1] (optional) is the socket directory fir created for per-child unix
# sockets; the template removes it on exit so no /tmp/firfork* dir leaks even
# when fir never gets to clean up (reexec, crash).

import contextlib
import gc
import json
import os
import signal
import socket
import sys
import time

# Import the SDK once into the template heap. Every forked child inherits this
# COW. Keep this the ONLY heavyweight import in the template.
import fir_ext  # noqa: F401  (imported for its side-effect: warm heap)


def _writeln(fd, obj):
    os.write(fd, (json.dumps(obj, separators=(",", ":")) + "\n").encode("utf-8"))


class _LineReader:
    """Unbuffered line reader over a raw fd.

    Uses os.read directly rather than a buffered file object so that, at
    fork time, no already-read bytes for a *future* control message are
    trapped in a Python buffer that the child would inherit and the parent
    could mis-handle. The parent owns _buf entirely; children never read it.
    """

    def __init__(self, fd):
        self.fd = fd
        self._buf = b""

    def readline(self):
        while b"\n" not in self._buf:
            try:
                chunk = os.read(self.fd, 65536)
            except InterruptedError:
                continue
            if not chunk:
                if self._buf:
                    line, self._buf = self._buf, b""
                    return line
                return None
            self._buf += chunk
        line, _, self._buf = self._buf.partition(b"\n")
        return line


def _reap_zombies():
    """Opportunistically reap any children that exited on their own.

    Called every control-loop iteration to avoid a SIGCHLD handler (which
    would interrupt the blocking read with EINTR). Intentional stops are
    reaped synchronously in _do_stop; this catches self-exits/crashes.
    """
    while True:
        try:
            pid, _ = os.waitpid(-1, os.WNOHANG)
        except ChildProcessError:
            return
        except OSError:
            return
        if pid == 0:
            return


def _child_main(path, sock_path, env):
    """Runs in the forked child. MUST NOT raise into the caller and MUST NOT
    write to fd 1 before dup2 (fd 1 is still the control pipe to fir). Any
    failure ends in os._exit, never an exception that could unwind into a
    stray print on the control stream."""
    try:
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.connect(sock_path)
        sfd = s.fileno()
        # Redirect stdio to the socket as the very first effective action so
        # nothing downstream (imports, prints, tracebacks) can corrupt the
        # control pipe that fd 1 still pointed at a moment ago.
        os.dup2(sfd, 0)
        os.dup2(sfd, 1)
        if sfd > 2:
            os.close(sfd)
    except BaseException:
        os._exit(1)

    try:
        if env:
            os.environ.update({str(k): str(v) for k, v in env.items()})
        # Rebuild the Python stdio objects on the new fds so fir_ext.run()'s
        # default sys.stdin/sys.stdout speak the socket.
        sys.stdin = os.fdopen(0, "r")
        sys.stdout = os.fdopen(1, "w")
        sys.argv = [path]
        import runpy

        # Runs the unchanged extension module (which calls fir_ext.run()).
        # Blocks until the socket EOFs (fir closes it on teardown).
        runpy.run_path(path, run_name="__main__")
        with contextlib.suppress(Exception):
            sys.stdout.flush()
        os._exit(0)
    except SystemExit as exc:
        with contextlib.suppress(Exception):
            sys.stdout.flush()
        code = exc.code if isinstance(exc.code, int) else 0
        os._exit(code)
    except BaseException:  # child must never unwind into the caller
        with contextlib.suppress(Exception):
            import traceback

            traceback.print_exc(file=sys.stderr)
        os._exit(1)


def _do_spawn(msg):
    path = msg.get("path") or ""
    sock_path = msg.get("sock") or ""
    env = msg.get("env") or {}
    if not path or not sock_path:
        return {"id": msg.get("id"), "error": "spawn: missing path/sock"}
    try:
        pid = os.fork()
    except OSError as exc:
        return {"id": msg.get("id"), "error": f"fork: {exc}"}
    if pid == 0:
        # Child never returns.
        _child_main(path, sock_path, env)
        os._exit(0)  # defensive; _child_main always exits
    return {"id": msg.get("id"), "pid": pid}


def _do_stop(msg):
    pid = msg.get("pid")
    if not isinstance(pid, int) or pid <= 0:
        return {"id": msg.get("id"), "error": "stop: bad pid"}
    # Graceful first: fir has already closed the child's socket, so the
    # extension's read loop should be exiting. SIGTERM, poll up to 2s, SIGKILL.
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        _try_reap(pid)
        return {"id": msg.get("id"), "reaped": True}
    except OSError:
        pass

    deadline = time.monotonic() + 2.0
    while time.monotonic() < deadline:
        if _try_reap(pid):
            return {"id": msg.get("id"), "reaped": True}
        time.sleep(0.01)

    with contextlib.suppress(OSError):
        os.kill(pid, signal.SIGKILL)
    # Block until reaped (child is being killed).
    for _ in range(500):
        if _try_reap(pid):
            break
        time.sleep(0.01)
    return {"id": msg.get("id"), "reaped": True}


def _try_reap(pid):
    """Return True if pid has been reaped (or is already gone)."""
    try:
        wpid, _ = os.waitpid(pid, os.WNOHANG)
    except ChildProcessError:
        return True  # already reaped elsewhere
    except OSError:
        return True
    return wpid == pid


def main():
    reader = _LineReader(0)
    # Maximise copy-on-write sharing across forked children: move every
    # object allocated during import into a permanent GC generation that
    # collect() never scans again, so a child gc.collect() cannot dirty
    # (COW-copy) the shared template heap (~58% less Private_Dirty/child).
    # gc.freeze() exists since CPython 3.7; our floor is 3.9.
    gc.collect()
    gc.freeze()
    _writeln(1, {"ready": True})
    while True:
        _reap_zombies()
        line = reader.readline()
        if line is None:
            break
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except Exception:  # noqa: S112 — ignore malformed control lines
            continue
        cmd = msg.get("cmd")
        if cmd == "spawn":
            _writeln(1, _do_spawn(msg))
        elif cmd == "stop":
            _writeln(1, _do_stop(msg))
        elif cmd == "shutdown":
            _writeln(1, {"id": msg.get("id"), "ok": True})
            break
        else:
            _writeln(1, {"id": msg.get("id"), "error": f"unknown cmd: {cmd!r}"})
    # Best-effort: reap whatever is left so we don't leave zombies behind,
    # and remove the socket dir fir passed as argv[1] (fir's Close() also
    # removes it, but fir may be gone — reexec, crash — by the time we exit).
    _reap_zombies()
    if len(sys.argv) > 1 and sys.argv[1]:
        import shutil

        shutil.rmtree(sys.argv[1], ignore_errors=True)


if __name__ == "__main__":
    main()
