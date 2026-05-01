#!/usr/bin/env python3
# ---
# name: observe
# description: Per-session observation hooks plus the `fir observe` and
#   `fir send` CLI verbs — writes a sidecar with the transcript path,
#   accepts user-input messages on a Unix socket, and provides command-line
#   tools to list, tail, and steer running fir sessions.
# builtin: true
# cli_verbs: observe, send
# ---
"""fir observe / fir send — per-session observation extension and CLI verbs.

Two responsibilities run in the per-session subprocess (one observe.py
instance per ACP / interactive / print session):

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

Two more responsibilities run as **CLI verbs** (``fir observe``,
``fir send``) when this extension is invoked cold via fir's verb
dispatcher (see docs/design/extension-cli-verbs.md). Verb handlers do not
have a session — they read sidecars from disk, tail transcript files, and
optionally connect to the per-session socket to inject input.

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
from datetime import datetime, timezone
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
_SOCKET_ID_PREFIX_LEN = 16


def _sidecar_path(session_id: str) -> Path:
    return _state_dir() / f"{session_id}.json"


def _socket_path(session_id: str) -> Path:
    return _socket_dir() / f"{session_id[:_SOCKET_ID_PREFIX_LEN]}.sock"


def _is_safe_session_id(sid: str) -> bool:
    """Reject session ids that could escape the agents/ directory."""
    if not sid or len(sid) > 128:
        return False
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
    with _state_lock:
        _state.update(kwargs)
    _write_sidecar()


# ---------------------------------------------------------------------------
# Socket — accept loop (per-session side)
# ---------------------------------------------------------------------------


def _bind_socket(path: Path) -> socket.socket | None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with contextlib.suppress(FileNotFoundError):
        path.unlink()
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        sock.bind(str(path))
        os.chmod(str(path), 0o600)
        sock.listen(8)
    except OSError as e:
        sock.close()
        print(f"observe: bind {path} failed: {e}", file=sys.stderr)
        return None
    return sock


def _accept_loop(sock: socket.socket, ctx: fir_ext.Context) -> None:
    sock.settimeout(0.5)
    while not _shutdown.is_set():
        try:
            conn, _ = sock.accept()
        except socket.timeout:
            continue
        except OSError:
            return
        threading.Thread(target=_handle_conn, args=(conn, ctx), daemon=True).start()


def _handle_conn(conn: socket.socket, ctx: fir_ext.Context) -> None:
    try:
        with conn, conn.makefile("r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    msg = json.loads(line)
                except json.JSONDecodeError:
                    continue
                content = msg.get("content", "")
                if not isinstance(content, str) or not content:
                    continue
                deliver_as = msg.get("deliver_as", "")
                if deliver_as not in ("", "steer", "followUp"):
                    deliver_as = ""
                try:
                    ctx.send_user_message(content, deliver_as=deliver_as)
                except Exception as e:
                    print(f"observe: send_user_message failed: {e}", file=sys.stderr)
    except Exception as e:
        print(f"observe: connection handler exited: {e}", file=sys.stderr)


# ---------------------------------------------------------------------------
# Per-session lifecycle handlers
# ---------------------------------------------------------------------------


@fir_ext.on("session_start")
def on_session_start(params: dict[str, Any], ctx: fir_ext.Context) -> None:
    global _socket, _accept_thread

    sid = params.get("session_id", "") if params else ""
    if not sid:
        return
    if not _is_safe_session_id(sid):
        print(f"observe: refusing unsafe session_id: {sid!r}", file=sys.stderr)
        return

    store_path = ctx.get_session_file()
    if not store_path:
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
    _accept_thread = threading.Thread(target=_accept_loop, args=(sock, ctx), daemon=True)
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
    _update_state(status="ended")
    if _socket is not None:
        with contextlib.suppress(Exception):
            _socket.close()
        _socket = None
    sid = _state.get("session_id", "")
    if sid:
        with contextlib.suppress(FileNotFoundError, OSError):
            _socket_path(sid).unlink()


# ---------------------------------------------------------------------------
# CLI verb support — sidecar discovery
# ---------------------------------------------------------------------------


def _read_sidecars() -> list[dict[str, Any]]:
    """Read all sidecars, reclassify dead pids as 'crashed', sort newest first."""
    d = _state_dir()
    if not d.is_dir():
        return []
    out: list[dict[str, Any]] = []
    for entry in os.listdir(d):
        if not entry.endswith(".json"):
            continue
        full = d / entry
        try:
            with open(full, encoding="utf-8") as f:
                s = json.load(f)
        except (OSError, ValueError):
            continue
        if not isinstance(s, dict):
            continue
        s["_sidecar_path"] = str(full)
        status = s.get("status", "")
        if status in ("running", "idle"):
            try:
                os.kill(int(s.get("pid") or 0), 0)
            except (ProcessLookupError, ValueError, OverflowError):
                s["status"] = "crashed"
            except PermissionError:
                pass  # process exists, just not ours; treat as alive
            except OSError:
                s["status"] = "crashed"
        out.append(s)
    out.sort(key=lambda s: s.get("started_at", ""), reverse=True)
    return out


def _resolve_sidecar(id_prefix: str, cwd_flag: str) -> dict[str, Any]:
    """Find the sidecar matching id_prefix or cwd_flag. Raises ValueError on
    miss/ambiguity."""
    all_sidecars = _read_sidecars()
    if not all_sidecars:
        raise ValueError(f"no fir sessions found (no sidecars in {_state_dir()})")

    if cwd_flag:
        want = cwd_flag
        if want == ".":
            want = os.getcwd()
        want = os.path.abspath(want)
        matches = [s for s in all_sidecars if s.get("cwd") == want]
        if not matches:
            raise ValueError(f"no session in cwd {want}")
        if len(matches) > 1:
            raise ValueError(_ambiguity_message(matches))
        return matches[0]

    matches = []
    for s in all_sidecars:
        sid = s.get("session_id", "") or ""
        name = s.get("session_name", "") or ""
        cwd = s.get("cwd", "") or ""
        if (
            sid.startswith(id_prefix)
            or (name and name.startswith(id_prefix))
            or os.path.basename(cwd).startswith(id_prefix)
        ):
            matches.append(s)
    if not matches:
        raise ValueError(f"no session matching {id_prefix!r}")
    if len(matches) > 1:
        raise ValueError(_ambiguity_message(matches))
    return matches[0]


def _ambiguity_message(matches: list[dict[str, Any]]) -> str:
    lines = ["ambiguous match — candidates:"]
    for s in matches:
        sid = (s.get("session_id") or "")[:8]
        lines.append(f"  {sid}  {s.get('session_name', '')}  cwd={s.get('cwd', '')}")
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# CLI verb support — formatter
# ---------------------------------------------------------------------------


_HIDDEN_TYPES = {"label", "branch_summary", "custom", "custom_message"}


def _short_time(rfc3339: str) -> str:
    if not rfc3339:
        return ""
    s = rfc3339.replace("Z", "+00:00")
    try:
        dt = datetime.fromisoformat(s)
    except ValueError:
        return ""
    return dt.astimezone().strftime("%H:%M:%S")


def _trunc(s: str, max_runes: int) -> str:
    if max_runes <= 0:
        return ""
    if len(s) <= max_runes:
        return s
    return s[: max_runes - 1] + "…"


def _trunc_one_line(s: str, max_runes: int) -> str:
    s = s.replace("\n", " ").strip()
    if max_runes <= 0:
        return s
    return _trunc(s, max_runes)


def _summarise_content(content: Any, full: bool) -> str:
    """Reduce a Message.Content blob (string or list of blocks) to one line."""
    limit = 0 if full else 200
    if isinstance(content, str):
        return _trunc_one_line(content, limit)
    if isinstance(content, list):
        parts: list[str] = []
        for item in content:
            if not isinstance(item, dict):
                continue
            t = item.get("type", "")
            if t == "text":
                txt = item.get("text", "")
                if txt:
                    parts.append(_trunc_one_line(txt, limit))
            elif t == "tool_use":
                parts.append("→ " + str(item.get("name", "")))
            elif t == "tool_result":
                r_limit = 100 if (limit and limit > 100) else limit
                parts.append("← " + _trunc_one_line(str(item.get("content", "")), r_limit))
            elif t == "image":
                parts.append("[image]")
            elif t == "thinking":
                parts.append("(thinking)")
        return "  ".join(parts)
    return "(unrenderable)"


_ANSI_CODES = {
    "dim": "\x1b[2m",
    "bold": "\x1b[1m",
    "cyan": "\x1b[36m",
    "green": "\x1b[32m",
    "yellow": "\x1b[33m",
    "magenta": "\x1b[35m",
    "red": "\x1b[31m",
    "reset": "\x1b[0m",
}


class _Formatter:
    def __init__(self, raw_json: bool, full_text: bool, color: bool) -> None:
        self.raw_json = raw_json
        self.full_text = full_text
        self.color = color

    def _wrap(self, s: str, code: str) -> str:
        if not self.color:
            return s
        return _ANSI_CODES[code] + s + _ANSI_CODES["reset"]

    def render(self, line: str) -> str | None:
        """Return a formatted line for one JSONL record, or None to suppress."""
        if self.raw_json:
            return line
        try:
            d = json.loads(line)
        except json.JSONDecodeError:
            return self._wrap("?? " + line, "dim")
        if not isinstance(d, dict):
            return None
        ts = _short_time(d.get("timestamp", "") or "")
        prefix = self._wrap(f"[{ts}] ", "dim") + " " if ts else ""
        ty = d.get("type", "")
        if ty == "session":
            sid = (d.get("id") or "")[:8]
            return (
                self._wrap(f"◆ session {sid}", "bold")
                + "  "
                + self._wrap(f"v{d.get('version', 0)}", "dim")
                + f"  cwd={d.get('cwd', '')}"
            )
        if ty == "message":
            return prefix + self._render_message(d.get("message"))
        if ty == "model_change":
            text = f"✎ model → {d.get('provider', '')}/{d.get('modelId', '')}"
            return prefix + self._wrap(text, "dim")
        if ty == "thinking_level_change":
            return prefix + self._wrap("✎ thinking level changed", "dim")
        if ty == "compaction":
            return prefix + self._wrap("⟳", "yellow") + " compaction: " + str(d.get("summary", ""))
        if ty == "session_info":
            return prefix + self._wrap("✎ session named: ", "dim") + str(d.get("name", ""))
        if ty == "command":
            args = str(d.get("args", "") or "")
            if not self.full_text:
                args = _trunc(args, 60)
            return prefix + self._wrap("$ ", "dim") + str(d.get("command", "")) + " " + args
        if ty == "plan_update":
            return prefix + "📋 plan: " + str(d.get("planTitle", ""))
        if ty in _HIDDEN_TYPES:
            return None
        return prefix + self._wrap(ty, "dim")

    def _render_message(self, raw: Any) -> str:
        if not raw:
            return self._wrap("(empty message)", "dim")
        if not isinstance(raw, dict):
            return self._wrap("(unparseable message)", "dim")
        role = raw.get("role", "")
        body = _summarise_content(raw.get("content"), self.full_text)
        if role == "user":
            return self._wrap("▸ user", "cyan") + "  " + body
        if role == "assistant":
            return self._wrap("◆ assistant", "green") + "  " + body
        if role == "tool":
            return self._wrap("✓ tool", "magenta") + "  " + body
        if role == "system":
            return self._wrap("· system  ", "dim") + body
        return self._wrap(f"· {role} ", "dim") + body


# ---------------------------------------------------------------------------
# CLI verb support — `fir send` wire format
# ---------------------------------------------------------------------------


def _encode_send(line: str, default_deliver_as: str) -> bytes | None:
    """Encode one user-typed line as an NDJSON byte string.

    Sigil rules (first-line only):
      !msg     → deliver_as=steer
      +msg     → deliver_as=followUp
      \\!msg   → escaped literal '!'
      \\+msg   → escaped literal '+'
    Returns None if the line is empty after stripping.
    """
    deliver_as = default_deliver_as
    if line.startswith(("\\!", "\\+")):
        line = line[1:]
    elif line.startswith("!"):
        deliver_as = "steer"
        line = line[1:]
    elif line.startswith("+"):
        deliver_as = "followUp"
        line = line[1:]
    if not line.strip():
        return None
    return (json.dumps({"deliver_as": deliver_as, "content": line}) + "\n").encode()


# ---------------------------------------------------------------------------
# Shared snapshot helpers (used by /commands, AI tools, and verb list path)
# ---------------------------------------------------------------------------


_NO_SESSIONS_NOTICE = "no fir sessions found"


def _snapshot_session_list() -> str:
    """Return a formatted table of live sessions (or empty notice)."""
    sidecars = _read_sidecars()
    if not sidecars:
        return _NO_SESSIONS_NOTICE
    id_w, name_w, cwd_w = 8, 4, 3
    for s in sidecars:
        sid = s.get("session_id", "") or ""
        id_w = max(id_w, min(8, len(sid)))
        name_w = max(name_w, len(s.get("session_name", "") or ""))
        cwd_w = max(cwd_w, len(os.path.basename(s.get("cwd", "") or "")))
    name_w = min(name_w, 30)
    cwd_w = min(cwd_w, 30)
    lines = [
        f"{'ID':<{id_w}}  {'NAME':<{name_w}}  {'CWD':<{cwd_w}}  {'STATUS':<9}  AGE"
    ]
    now = time.time()
    for s in sidecars:
        sid = (s.get("session_id", "") or "")[:8]
        name = _trunc(s.get("session_name", "") or "-", name_w)
        cwd = _trunc(os.path.basename(s.get("cwd", "") or ""), cwd_w)
        status = s.get("status", "") or ""
        age = _age_string(s.get("started_at", "") or "", now)
        lines.append(f"{sid:<{id_w}}  {name:<{name_w}}  {cwd:<{cwd_w}}  {status:<9}  {age}")
    return "\n".join(lines)


def _tail_lines(path: str, n: int, chunk_size: int = 8192) -> list[str]:
    """Return the last ``n`` newline-terminated lines from ``path`` without
    reading the entire file into memory. Reads backwards in ``chunk_size``
    blocks until enough newlines are seen.
    """
    if n <= 0:
        return []
    try:
        size = os.path.getsize(path)
    except OSError:
        return []
    if size == 0:
        return []
    buf = bytearray()
    with open(path, "rb") as f:
        # We want the last n lines, so collect at least n+1 newline boundaries
        # walking backwards (the +1 captures the partial leading line).
        offset = size
        newlines = 0
        while offset > 0 and newlines <= n:
            read_size = min(chunk_size, offset)
            offset -= read_size
            f.seek(offset)
            block = f.read(read_size)
            buf[:0] = block  # prepend
            newlines = buf.count(b"\n")
    text = bytes(buf).decode("utf-8", errors="replace")
    lines = text.splitlines()
    return lines[-n:]


def _snapshot_transcript(
    id_prefix: str,
    cwd_flag: str,
    lines: int,
    raw_json: bool,
    full_text: bool = False,
) -> str:
    """Return the last `lines` formatted (or raw) lines of a session transcript.

    Snapshot semantics — does not live-tail. Use `fir observe` from another
    terminal for live observation.
    """
    s = _resolve_sidecar(id_prefix, cwd_flag)
    store_path = s.get("store_path", "") or ""
    if not store_path:
        sid8 = (s.get("session_id", "") or "")[:8]
        raise ValueError(f"session {sid8} has no transcript on disk (in-memory)")
    try:
        tail = _tail_lines(store_path, max(1, lines))
    except OSError as e:
        raise ValueError(f"open transcript {store_path}: {e}") from e
    fmt = _Formatter(raw_json=raw_json, full_text=full_text, color=False)
    out: list[str] = []
    for ln in tail:
        if not ln:
            continue
        rendered = fmt.render(ln)
        if rendered is not None:
            out.append(rendered)
    return "\n".join(out) if out else "(no displayable lines)"


def _send_one(id_prefix: str, cwd_flag: str, content: str, deliver_as: str) -> None:
    """Connect to the session's input socket and send one NDJSON message.

    Sigils on `content` are NOT parsed here — callers pre-parse if they want
    that behaviour. `deliver_as` must be "", "steer", or "followUp".
    """
    if deliver_as not in ("", "steer", "followUp"):
        raise ValueError(f"deliver_as must be '', 'steer', or 'followUp' (got {deliver_as!r})")
    if not content or not content.strip():
        raise ValueError("content is empty")
    s = _resolve_sidecar(id_prefix, cwd_flag)
    sock_path = s.get("socket_path", "") or ""
    if not sock_path:
        sid8 = (s.get("session_id", "") or "")[:8]
        raise ValueError(f"session {sid8} has no input socket (ended or not started)")
    payload = (json.dumps({"deliver_as": deliver_as, "content": content}) + "\n").encode()
    conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        conn.connect(sock_path)
        conn.sendall(payload)
    finally:
        with contextlib.suppress(Exception):
            conn.close()


# ---------------------------------------------------------------------------
# Slash commands — `/observe`, `/send`
# ---------------------------------------------------------------------------


@fir_ext.command(
    name="observe",
    description="List live fir sessions, or snapshot a session's transcript "
    "(use `fir observe <id>` from another terminal for live tail).",
)
def cmd_observe(args: list[str], ctx: fir_ext.Context) -> dict[str, Any]:
    id_prefix = ""
    cwd_flag = ""
    raw_json = False
    full_text = False
    lines = 50
    i = 0
    while i < len(args):
        a = args[i]
        if a == "--json":
            raw_json = True
        elif a == "--full":
            full_text = True
        elif a == "--cwd":
            if i + 1 < len(args):
                cwd_flag = args[i + 1]
                i += 1
            else:
                return {"message": "/observe: --cwd requires an argument"}
        elif a.startswith("--cwd="):
            cwd_flag = a[len("--cwd="):]
        elif a.startswith("--lines="):
            try:
                lines = int(a[len("--lines="):])
            except ValueError:
                return {"message": f"/observe: invalid --lines value: {a}"}
        elif a.startswith("--"):
            return {"message": f"/observe: unknown flag: {a}"}
        else:
            id_prefix = a
        i += 1
    if not id_prefix and not cwd_flag:
        return {"message": _snapshot_session_list(), "print_response": True}
    try:
        out = _snapshot_transcript(id_prefix, cwd_flag, lines, raw_json, full_text)
    except ValueError as e:
        return {"message": str(e)}
    return {"message": out, "print_response": True}


@fir_ext.command(
    name="send",
    description="Send a message to a live fir session "
    "(usage: /send <id-prefix> [--steer|--follow] <message...>).",
)
def cmd_send(args: list[str], ctx: fir_ext.Context) -> dict[str, Any]:
    if not args:
        return {"message": "/send: usage: /send <id-prefix> [--steer|--follow] <message...>"}
    id_prefix = ""
    cwd_flag = ""
    deliver_as = ""
    msg_parts: list[str] = []
    i = 0
    while i < len(args):
        a = args[i]
        if a == "--steer":
            deliver_as = "steer"
        elif a == "--follow":
            deliver_as = "followUp"
        elif a == "--cwd":
            if i + 1 < len(args):
                cwd_flag = args[i + 1]
                i += 1
            else:
                return {"message": "/send: --cwd requires an argument"}
        elif a.startswith("--cwd="):
            cwd_flag = a[len("--cwd="):]
        elif a.startswith("--") and not msg_parts:
            return {"message": f"/send: unknown flag: {a}"}
        elif not id_prefix and not cwd_flag and not msg_parts:
            id_prefix = a
        else:
            msg_parts.append(a)
        i += 1
    if not id_prefix and not cwd_flag:
        return {"message": "/send: session id or --cwd required"}
    if not msg_parts:
        return {"message": "/send: message text required"}
    # Apply first-line sigils for symmetry with `fir send`. `--steer`/`--follow`
    # set the default; sigil overrides.
    content = " ".join(msg_parts)
    encoded = _encode_send(content, deliver_as)
    if encoded is None:
        return {"message": "/send: empty message after sigil stripping"}
    parsed = json.loads(encoded.decode().rstrip("\n"))
    try:
        _send_one(id_prefix, cwd_flag, parsed["content"], parsed["deliver_as"])
    except (ValueError, OSError) as e:
        return {"message": f"/send: {e}"}
    return {"message": f"sent ({parsed['deliver_as'] or 'prompt'})"}


# ---------------------------------------------------------------------------
# AI tools — observe_session, send_session
# ---------------------------------------------------------------------------


@fir_ext.tool(
    name="observe_session",
    description=(
        "Inspect another running fir session. Without arguments, returns a "
        "table of live sessions. With id_prefix or cwd, returns a snapshot "
        "of the last `lines` formatted entries from that session's transcript "
        "(does NOT live-tail). Useful for checking what a sibling agent is "
        "doing or auditing a long-running session. id_prefix matches the "
        "session id, session name, or basename(cwd)."
    ),
    parameters={
        "type": "object",
        "properties": {
            "id_prefix": {
                "type": "string",
                "description": "Prefix matching session id / name / basename(cwd). Optional.",
            },
            "cwd": {
                "type": "string",
                "description": "Resolve by working directory (use '.' for current). Optional.",
            },
            "lines": {
                "type": "integer",
                "description": "How many trailing transcript lines to include (default 50).",
                "default": 50,
            },
            "raw_json": {
                "type": "boolean",
                "description": "Return raw JSONL instead of formatted text. Default false.",
                "default": False,
            },
            "full_text": {
                "type": "boolean",
                "description": "Disable truncation (useful for agent consumption). Default false.",
                "default": False,
            },
        },
    },
)
def tool_observe(params: dict[str, Any], ctx: fir_ext.Context) -> str:
    id_prefix = (params.get("id_prefix") or "").strip()
    cwd_flag = (params.get("cwd") or "").strip()
    lines = int(params.get("lines") or 50)
    raw_json = bool(params.get("raw_json"))
    full_text = bool(params.get("full_text"))
    if not id_prefix and not cwd_flag:
        return _snapshot_session_list()
    try:
        return _snapshot_transcript(id_prefix, cwd_flag, lines, raw_json, full_text)
    except ValueError as e:
        raise fir_ext.ToolError(str(e)) from e


@fir_ext.tool(
    name="send_session",
    description=(
        "Send a message to a different running fir session. The target "
        "session receives the message as a user-role input on its prompt "
        "queue (deliver_as='') by default; use deliver_as='steer' to "
        "interrupt the target's current turn, or 'followUp' to queue after "
        "it. Connects to the target's per-session Unix socket; the target "
        "must be live (not ended). Use to coordinate with sibling agents — "
        "e.g. 'review this branch when done', or to nudge a stuck session."
    ),
    parameters={
        "type": "object",
        "properties": {
            "id_prefix": {
                "type": "string",
                "description": "Prefix matching session id / name / basename(cwd). One of id_prefix or cwd is required.",
            },
            "cwd": {
                "type": "string",
                "description": "Resolve target by working directory. Optional alternative to id_prefix.",
            },
            "content": {
                "type": "string",
                "description": "Message text to deliver to the target session.",
            },
            "deliver_as": {
                "type": "string",
                "enum": ["", "steer", "followUp"],
                "description": "How to deliver: '' (default new turn), 'steer' (interrupt current turn), 'followUp' (queue post-turn).",
                "default": "",
            },
        },
        "required": ["content"],
    },
)
def tool_send(params: dict[str, Any], ctx: fir_ext.Context) -> dict[str, Any]:
    id_prefix = (params.get("id_prefix") or "").strip()
    cwd_flag = (params.get("cwd") or "").strip()
    content = params.get("content") or ""
    deliver_as = params.get("deliver_as") or ""
    if not id_prefix and not cwd_flag:
        raise fir_ext.ToolError("one of id_prefix or cwd is required")
    try:
        _send_one(id_prefix, cwd_flag, content, deliver_as)
    except (ValueError, OSError) as e:
        raise fir_ext.ToolError(str(e)) from e
    return {"ok": True, "deliver_as": deliver_as or "prompt"}


# ---------------------------------------------------------------------------
# CLI verb: `fir observe`
# ---------------------------------------------------------------------------

_OBSERVE_USAGE = """usage: fir observe [<id-prefix>] [--cwd <path>] [--json] [--full] [--interact]

  fir observe                  list live sessions across all running fir processes
  fir observe <id-prefix>      tail-and-format the matching session's transcript
  fir observe --cwd <path>     resolve session by working directory
  fir observe --cwd .          session in current directory (error if 0/many)
  fir observe <id> --json      raw JSONL transcript — no formatting, no truncation
  fir observe <id> --full      formatted transcript with no truncation
  fir observe <id> --interact  also pipe stdin to session as input (Enter to send)
"""

_SEND_USAGE = """usage: fir send <id-prefix> [--steer | --follow] [--cwd <path>]

  fir send <id-prefix>            interactive: Enter to send each line, Ctrl-\\ to disconnect
  fir send <id-prefix> --steer    all messages sent as steer (interrupt)
  fir send <id-prefix> --follow   all messages sent as followUp (queue)
  fir send --cwd .                resolve session by current directory
  echo "fix the bug" | fir send <id>   pipe a single message

First-line sigils (override per-message):
  !message     → steer (interrupt current turn)
  +message     → followUp (queue after current turn)
  \\!message    → literal '!' (escaped)
"""


def _age_string(started_at: str, now: float) -> str:
    if not started_at:
        return "?"
    s = started_at.replace("Z", "+00:00")
    try:
        dt = datetime.fromisoformat(s)
    except ValueError:
        return "?"
    delta = now - dt.astimezone(timezone.utc).timestamp()
    if delta < 60:
        return f"{int(delta)}s"
    if delta < 3600:
        return f"{int(delta // 60)}m{int(delta) % 60:02d}s"
    if delta < 86400:
        return f"{int(delta // 3600)}h{int((delta % 3600) // 60):02d}m"
    return f"{int(delta // 86400)}d"


def _verb_observe_list(host: fir_ext.Host) -> int:
    out = _snapshot_session_list()
    if out == _NO_SESSIONS_NOTICE:
        host.eprintln(out)
        return 0
    host.println(out)
    return 0


def _verb_observe_tail(
    host: fir_ext.Host,
    id_prefix: str,
    cwd_flag: str,
    json_out: bool,
    interact: bool,
    full_text: bool,
) -> int:
    try:
        s = _resolve_sidecar(id_prefix, cwd_flag)
    except ValueError as e:
        host.eprintln(str(e))
        return 1
    store_path = s.get("store_path", "") or ""
    if not store_path:
        sid8 = (s.get("session_id", "") or "")[:8]
        host.eprintln(f"session {sid8} has no transcript on disk (in-memory session)")
        return 1

    # --interact: connect to socket, forward host stdin lines.
    interact_stop = threading.Event()
    if interact:
        sock_path = s.get("socket_path", "") or ""
        if not sock_path:
            host.eprintln(
                "warning: --interact requested but session has no input socket (read-only)"
            )
        else:
            try:
                conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                conn.connect(sock_path)
            except OSError as e:
                host.eprintln(
                    f"warning: --interact: connect socket: {e} (continuing read-only)"
                )
                conn = None
            if conn is not None:
                # Daemon thread; we never join — the verb either exits via
                # the tail loop or via a signal handler, both of which tear
                # the process down regardless of this thread's state.
                threading.Thread(
                    target=_interact_send_loop,
                    args=(host, conn, interact_stop),
                    daemon=True,
                ).start()

    # Color: only when fir's stdout is a TTY and NO_COLOR is unset.
    color = host.stdout_is_tty and not os.environ.get("NO_COLOR")
    fmt = _Formatter(raw_json=json_out, full_text=full_text, color=color)

    # Tail loop. ~10 syscalls/sec when idle.
    try:
        f = open(store_path, "rb")  # noqa: SIM115 — closed in finally
    except OSError as e:
        host.eprintln(f"open transcript {store_path}: {e}")
        interact_stop.set()
        return 1

    sidecar_path = s.get("_sidecar_path", "") or ""
    pending = b""
    try:
        while True:
            chunk = f.readline()
            if chunk:
                pending += chunk
                if pending.endswith(b"\n"):
                    line = pending[:-1].decode("utf-8", errors="replace")
                    pending = b""
                    rendered = fmt.render(line)
                    if rendered is not None:
                        host.println(rendered)
                continue
            # EOF — poll for growth.
            time.sleep(0.1)
            try:
                cur_size = os.stat(store_path).st_size
            except FileNotFoundError:
                return 0
            if cur_size > f.tell():
                continue
            # Idle. Stop tailing if the session has ended.
            if sidecar_path:
                try:
                    with open(sidecar_path, encoding="utf-8") as sf:
                        fresh = json.load(sf)
                    if fresh.get("status") == "ended":
                        return 0
                except (OSError, ValueError):
                    pass
    finally:
        interact_stop.set()
        with contextlib.suppress(Exception):
            f.close()


def _interact_send_loop(
    host: fir_ext.Host, conn: socket.socket, stop: threading.Event
) -> None:
    """--interact stdin pump: each non-empty line of host.readline() is sent
    as one NDJSON message through `conn`. Ends on EOF or `stop`."""
    try:
        while not stop.is_set():
            line = host.readline(timeout=0.5)
            if line is None:
                # EOF or timeout. distinguish via stop event.
                if stop.is_set():
                    return
                continue
            line = line.rstrip("\n")
            if not line.strip():
                continue
            payload = _encode_send(line, "")
            if payload is None:
                continue
            try:
                conn.sendall(payload)
            except OSError as e:
                host.eprintln(f"warning: send: {e}")
                return
    finally:
        with contextlib.suppress(Exception):
            conn.close()


def _parse_observe_args(argv: list[str]) -> tuple[str, str, bool, bool, bool, str | None]:
    """Returns (id_prefix, cwd_flag, json_out, interact, full_text, error_message)."""
    id_prefix = ""
    cwd_flag = ""
    json_out = False
    interact = False
    full_text = False
    i = 0
    while i < len(argv):
        a = argv[i]
        if a == "--json":
            json_out = True
        elif a == "--full":
            full_text = True
        elif a == "--interact":
            interact = True
        elif a == "--cwd":
            if i + 1 >= len(argv):
                return ("", "", False, False, False,
                        "--cwd requires an argument (path or '.')")
            cwd_flag = argv[i + 1]
            i += 1
        elif a.startswith("--cwd="):
            cwd_flag = a[len("--cwd="):]
        elif a in ("--help", "-h"):
            return ("", "", False, False, False, "__HELP__")
        elif a.startswith("--"):
            return ("", "", False, False, False, f"unknown flag: {a}")
        else:
            if id_prefix:
                return ("", "", False, False, False,
                        f"unexpected extra argument: {a}")
            id_prefix = a
        i += 1
    return (id_prefix, cwd_flag, json_out, interact, full_text, None)


@fir_ext.cli_verb("observe")
def cli_observe(argv: list[str], host: fir_ext.Host) -> int:
    id_prefix, cwd_flag, json_out, interact, full_text, err = _parse_observe_args(argv)
    if err == "__HELP__":
        host.eprint(_OBSERVE_USAGE)
        return 0
    if err is not None:
        host.eprintln(err)
        host.eprint(_OBSERVE_USAGE)
        return 1
    if not id_prefix and not cwd_flag:
        return _verb_observe_list(host)
    return _verb_observe_tail(host, id_prefix, cwd_flag, json_out, interact, full_text)


# ---------------------------------------------------------------------------
# CLI verb: `fir send`
# ---------------------------------------------------------------------------


def _parse_send_args(argv: list[str]) -> tuple[str, str, str, str | None]:
    """Returns (id_prefix, cwd_flag, default_deliver_as, error_message)."""
    id_prefix = ""
    cwd_flag = ""
    steer = False
    follow = False
    i = 0
    while i < len(argv):
        a = argv[i]
        if a == "--steer":
            steer = True
        elif a == "--follow":
            follow = True
        elif a == "--cwd":
            if i + 1 >= len(argv):
                return ("", "", "", "--cwd requires an argument")
            cwd_flag = argv[i + 1]
            i += 1
        elif a.startswith("--cwd="):
            cwd_flag = a[len("--cwd="):]
        elif a in ("--help", "-h"):
            return ("", "", "", "__HELP__")
        elif a.startswith("--"):
            return ("", "", "", f"unknown flag: {a}")
        else:
            if id_prefix:
                return ("", "", "", f"unexpected extra argument: {a}")
            id_prefix = a
        i += 1
    if steer and follow:
        return ("", "", "", "--steer and --follow are mutually exclusive")
    if not id_prefix and not cwd_flag:
        return ("", "", "", "session id or --cwd required")
    default_deliver_as = "steer" if steer else ("followUp" if follow else "")
    return (id_prefix, cwd_flag, default_deliver_as, None)


@fir_ext.cli_verb("send")
def cli_send(argv: list[str], host: fir_ext.Host) -> int:
    id_prefix, cwd_flag, default_deliver_as, err = _parse_send_args(argv)
    if err == "__HELP__":
        host.eprint(_SEND_USAGE)
        return 0
    if err is not None:
        host.eprintln(err)
        host.eprint(_SEND_USAGE)
        return 1

    try:
        s = _resolve_sidecar(id_prefix, cwd_flag)
    except ValueError as e:
        host.eprintln(str(e))
        return 1
    sock_path = s.get("socket_path", "") or ""
    if not sock_path:
        sid8 = (s.get("session_id", "") or "")[:8]
        host.eprintln(f"session {sid8} has no input socket (ended or not started)")
        return 1
    try:
        conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        conn.connect(sock_path)
    except OSError as e:
        sid8 = (s.get("session_id", "") or "")[:8]
        host.eprintln(
            f"connect to session {sid8}: {e}\n(is the session still running?)"
        )
        return 1

    if host.stdin_is_tty:
        sid8 = (s.get("session_id", "") or "")[:8]
        name = s.get("session_name", "") or sid8
        suffix = f" ({sid8})" if s.get("session_name") else ""
        host.eprintln(
            f"Connected to session {name}{suffix}. Enter to send. Ctrl-\\ to disconnect."
        )
        host.eprintln("  ! prefix → steer (interrupt)   + prefix → followUp (queue)")

    try:
        while True:
            line = host.readline()
            if line is None:
                return 0
            line = line.rstrip("\n")
            if not line.strip():
                continue
            payload = _encode_send(line, default_deliver_as)
            if payload is None:
                continue
            try:
                conn.sendall(payload)
            except OSError as e:
                host.eprintln(f"send: {e}")
                return 1
    finally:
        with contextlib.suppress(Exception):
            conn.close()


# Ctrl-\ during `fir send` (interactive): clean detach.
@fir_ext.on_cli_signal
def _on_signal(name: str, host: fir_ext.Host) -> None:
    # SIGQUIT is the conventional clean-detach signal in send-style tools.
    if "quit" in name.lower() or name == "SIGQUIT":
        os._exit(0)


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

fir_ext.run(name="observe")
