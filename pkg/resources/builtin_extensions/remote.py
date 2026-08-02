#!/usr/bin/env python3
# ---
# name: remote
# description: ssh remote execution and remote tmux driving — rexec, rjob,
#   rput/rget, rtmux, rhosts. Ships scripts over ssh stdin so the model never
#   writes a nested quote.
# builtin: true
# ---
"""remote.py — first-class tools for working on other hosts over ssh.

Replaces hand-written ``ssh host "bash -lc '...'"`` one-liners inside Bash.
Measured across real fir sessions, 59% of all Bash calls contained
ssh/scp/rsync, and each one paid a per-call correctness tax: escaped quotes,
``timeout N``, ``BatchMode=yes``, ``ConnectTimeout=``, heredocs. This
extension converts that recurring tax into a one-time implementation.

Transport rules (the whole point — see docs in each builder):

1. **Never** build ``ssh host "bash -lc '...'"``. ssh joins its argv with
   spaces and the remote shell re-splits the result; that is where the
   nested-quoting failures live. Scripts are shipped over **ssh stdin** to
   ``bash -l -s``: zero local shell interpretation, arbitrary script size,
   and ``bash -l`` fixes both the missing-``~/.local/bin``-on-PATH gotcha and
   the remote-zsh gotcha (several hosts default to zsh, where an unquoted
   glob or a bare ``echo ===`` aborts the chain).
2. The ssh flags are owned here, not by the model — BatchMode, ConnectTimeout,
   ServerAliveInterval, and a ControlMaster mux socket so repeated calls skip
   the 150-500ms handshake. The mux degrades gracefully when the socket dies.
3. Host configuration lives in ``~/.ssh/config``. There is no fir-side host
   registry; the tools accept whatever ``ssh`` accepts.

Every tool returns the same discriminated envelope (see ``_envelope``), never
a bare string and never empty-on-failure: an empty tool result serialises to
``""`` on the wire and the model reads silence as "no signal".
"""

from __future__ import annotations

import base64
import concurrent.futures
import hashlib
import json
import os
import re
import secrets
import shlex
import subprocess
import threading
import time
from pathlib import Path
from typing import Any

import fir_ext

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

#: Output cap per stream. Beyond this we keep head + tail and set the flag.
_MAX_STREAM_BYTES = 40 * 1024

#: How much of the kept text comes from the head of the stream.
_HEAD_FRACTION = 0.6

#: Remote directory holding detached-job state. The remote filesystem *is*
#: the job registry — we never keep a local copy that can drift.
_RJOBS_DIR = "$HOME/.cache/fir/rjobs"

#: Grace period between TERM and KILL for the remote ``timeout`` wrapper.
_TIMEOUT_KILL_GRACE = 5

#: Extra seconds the local subprocess timeout gets over the remote one, so the
#: remote ``timeout`` normally wins and we can report a clean exit code 124.
_LOCAL_TIMEOUT_SLACK = 15

#: Sentinel exit codes emitted by our own remote preamble scripts.
_RC_NO_TMUX = 97
_RC_NO_JOB = 96

#: GNU ``timeout`` reports this when it had to kill the command.
_RC_TIMEOUT = 124

_OUTCOME_OK = "ok"
_OUTCOME_NONZERO = "nonzero_exit"
_OUTCOME_TIMEOUT = "timeout"
_OUTCOME_UNREACHABLE = "unreachable"
_OUTCOME_AUTH_FAILED = "auth_failed"
_OUTCOME_NO_TMUX = "no_tmux"
_OUTCOME_NO_TARGET = "no_target"

#: Outcomes reported to fir as tool errors (is_error). A remote command merely
#: exiting nonzero is NOT one of these — that is ordinary signal.
_ERROR_OUTCOMES = frozenset(
    {
        _OUTCOME_TIMEOUT,
        _OUTCOME_UNREACHABLE,
        _OUTCOME_AUTH_FAILED,
        _OUTCOME_NO_TMUX,
        _OUTCOME_NO_TARGET,
    }
)


# ---------------------------------------------------------------------------
# ssh argv construction
# ---------------------------------------------------------------------------


def _control_path() -> str:
    """ControlMaster socket template. ``%C`` is ssh's hash of the connection."""
    return "~/.ssh/fir-cm-%C"


def _ssh_flags() -> list[str]:
    """The ssh options this extension owns so the model never writes them.

    BatchMode keeps a password prompt from hanging a tool call forever;
    ConnectTimeout bounds an unreachable host; ServerAliveInterval kills a
    silently dropped Tailscale link; the ControlMaster trio reuses one TCP +
    crypto handshake across the many small calls this extension encourages.
    """
    return [
        "-o",
        "BatchMode=yes",
        "-o",
        "ConnectTimeout=8",
        "-o",
        "ServerAliveInterval=15",
        "-o",
        "ControlMaster=auto",
        "-o",
        f"ControlPath={_control_path()}",
        "-o",
        "ControlPersist=120",
    ]


def _check_host(host: str) -> str:
    """Reject an option-like host before ssh silently reinterprets it."""
    if host.startswith("-"):
        raise fir_ext.ToolError(f"invalid host {host!r}: must not start with '-'")
    return host


def _ssh_argv(host: str, remote_argv: list[str]) -> list[str]:
    """Full local argv for running *remote_argv* on *host*.

    *remote_argv* is passed after ``--`` so a host name that looks like a flag
    cannot be misparsed. Note the caller never embeds a shell command here —
    the script travels on stdin.
    """
    return ["ssh", *_ssh_flags(), host, "--", *remote_argv]


def _scp_argv(sources: list[str], dest: str, recursive: bool = True) -> list[str]:
    """scp argv sharing the same connection flags (and hence the same mux)."""
    argv = ["scp", *_ssh_flags(), "-p"]
    if recursive:
        argv.append("-r")
    argv.extend(sources)
    argv.append(dest)
    return argv


# Cache of host -> resolved ControlPath, so reuse detection costs one cheap
# local `ssh -G` per host per extension process instead of one per call.
_ctl_path_cache: dict[str, str] = {}
_ctl_lock = threading.Lock()


def _resolved_control_path(host: str) -> str:
    """Ask ssh to expand the ControlPath tokens (``%C``) for *host*.

    ``ssh -G`` performs full config + token expansion without touching the
    network, so this is a purely local resolution.
    """
    with _ctl_lock:
        cached = _ctl_path_cache.get(host)
    if cached is not None:
        return cached
    path = ""
    try:
        proc = subprocess.run(
            ["ssh", "-o", f"ControlPath={_control_path()}", "-G", host],
            capture_output=True,
            text=True,
            timeout=10,
        )
        for line in proc.stdout.splitlines():
            if line.lower().startswith("controlpath "):
                path = line.split(None, 1)[1].strip()
                break
    except (OSError, subprocess.SubprocessError):
        path = ""
    with _ctl_lock:
        _ctl_path_cache[host] = path
    return path


def _connection_reused(host: str) -> bool:
    """True when a live mux socket for *host* existed before this call."""
    path = _resolved_control_path(host)
    if not path:
        return False
    try:
        return os.path.exists(os.path.expanduser(path))
    except OSError:
        return False


# ---------------------------------------------------------------------------
# Remote script construction
# ---------------------------------------------------------------------------


def _script_preamble(cwd: str | None) -> str:
    """Leading lines every shipped script gets."""
    lines = ["# fir remote.py — shipped over ssh stdin, never re-split by a shell"]
    if cwd:
        lines.append(f"cd -- {shlex.quote(cwd)} || exit 127")
    return "\n".join(lines) + "\n"


def _build_script(command: str, cwd: str | None) -> str:
    """The exact bytes written to ``bash -l -s`` on the remote host."""
    return _script_preamble(cwd) + command + "\n"


def _b64(text: str) -> str:
    """Base64 of *text* — the only safe way to carry arbitrary bytes through
    a shell without inventing a heredoc delimiter that might collide."""
    return base64.b64encode(text.encode("utf-8")).decode("ascii")


def _write_file_cmd(path_expr: str, content: str) -> str:
    """Shell fragment writing *content* to *path_expr* with zero quoting risk."""
    return f"printf %s {_b64(content)} | base64 -d > {path_expr}"


def _new_job_id() -> str:
    return f"fir-{int(time.time())}-{secrets.token_hex(3)}"


def _is_safe_job_id(job_id: str) -> bool:
    """Job ids index into a remote directory — keep them boring."""
    if not job_id or len(job_id) > 64:
        return False
    return all(c.isalnum() or c in "-_" for c in job_id)


def _detach_scripts(job_id: str, command: str, cwd: str | None, host: str) -> str:
    """Outer script that stages and launches a detached remote job.

    The launched job must survive the ssh channel closing. Two things matter:

    * ``systemd-run --user --collect`` is the good path — the job gets its own
      transient unit, reaped on exit.
    * The fallback must *double-fork*: a plain ``setsid nohup ... &`` launched
      from a tool call still shares the foreground process group's fate on
      some hosts. ``( setsid ... & ) &`` detaches session *and* parentage.

    Either way stdout/stderr go to a file, not to the ssh channel — otherwise
    ssh would block waiting for EOF on an inherited pipe and ``detach`` would
    not return until the job finished.
    """
    runner = (
        "#!/bin/bash\n"
        f'D="{_RJOBS_DIR}"\n'
        f'echo $$ > "$D/{job_id}.pid"\n'
        + (
            # A failed cd must still record an rc, or the job would sit in
            # state=unknown forever with an empty log — the one state the
            # model cannot act on.
            f"cd -- {shlex.quote(cwd)} || {{ "
            f'echo "fir: cd failed: {cwd}" >> "$D/{job_id}.log"; '
            f'echo 127 > "$D/{job_id}.rc"; exit 127; }}\n'
            if cwd
            else ""
        )
        + f'bash -l "$D/{job_id}.cmd" >> "$D/{job_id}.log" 2>&1\n'
        f'echo $? > "$D/{job_id}.rc"\n'
    )
    meta = json.dumps(
        {
            "job_id": job_id,
            "host": host,
            "cwd": cwd or "",
            "command": command[:4000],
            "started_at": int(time.time()),
        },
        separators=(",", ":"),
    )
    return (
        f'D="{_RJOBS_DIR}"\n'
        'mkdir -p "$D" || exit 127\n'
        + _write_file_cmd(f'"$D/{job_id}.cmd"', command + "\n")
        + "\n"
        + _write_file_cmd(f'"$D/{job_id}.runner"', runner)
        + "\n"
        + _write_file_cmd(f'"$D/{job_id}.meta"', meta + "\n")
        + "\n"
        f': > "$D/{job_id}.log"\n'
        "if command -v systemd-run >/dev/null 2>&1 && "
        f"systemd-run --user --collect --quiet --unit={job_id} "
        f'-- /bin/bash "$D/{job_id}.runner" >/dev/null 2>&1; then\n'
        f'  echo systemd > "$D/{job_id}.launcher"\n'
        "else\n"
        f'  ( setsid /bin/bash "$D/{job_id}.runner" </dev/null >/dev/null 2>&1 & ) &\n'
        f'  echo fork > "$D/{job_id}.launcher"\n'
        "fi\n"
        f'cat "$D/{job_id}.launcher"\n'
    )


# ---------------------------------------------------------------------------
# Outcome classification
# ---------------------------------------------------------------------------

_AUTH_PATTERNS = (
    "permission denied",
    "too many authentication failures",
    "no supported authentication methods",
    "host key verification failed",
    "remote host identification has changed",
)

_UNREACHABLE_PATTERNS = (
    "could not resolve hostname",
    "connection refused",
    "connection timed out",
    "connection closed by remote host",
    "no route to host",
    "network is unreachable",
    "operation timed out",
    "name or service not known",
    "port 22: ",
    "kex_exchange_identification",
    "broken pipe",
)


def _classify(exit_code: int, stderr: str) -> str:
    """Map an ssh exit code + stderr onto an outcome.

    ssh reserves 255 for its *own* failures, but a remote command may also
    exit 255, so we only reclassify when the stderr actually looks like ssh
    complaining. Anything else is honest remote signal.
    """
    low = (stderr or "").lower()
    if exit_code == 255:
        if any(p in low for p in _AUTH_PATTERNS):
            return _OUTCOME_AUTH_FAILED
        if any(p in low for p in _UNREACHABLE_PATTERNS):
            return _OUTCOME_UNREACHABLE
        return _OUTCOME_UNREACHABLE
    if exit_code == 0:
        return _OUTCOME_OK
    return _OUTCOME_NONZERO


# ---------------------------------------------------------------------------
# Envelope
# ---------------------------------------------------------------------------


def _truncate(text: str) -> tuple[str, bool]:
    """Cap *text*, keeping head + tail so both the setup and the failure
    at the end of a long log survive. Returns ``(text, truncated)``."""
    raw = text or ""
    encoded = raw.encode("utf-8", "replace")
    if len(encoded) <= _MAX_STREAM_BYTES:
        return raw, False
    # Slice on bytes, not characters, so multibyte output cannot blow past the
    # budget; decode with "ignore" to drop a code point split at the seam.
    head_len = int(_MAX_STREAM_BYTES * _HEAD_FRACTION)
    tail_len = _MAX_STREAM_BYTES - head_len
    head = encoded[:head_len].decode("utf-8", "ignore")
    tail = encoded[-tail_len:].decode("utf-8", "ignore")
    dropped = len(encoded) - head_len - tail_len
    return f"{head}\n... [{dropped} bytes elided by fir remote] ...\n{tail}", True


def _envelope(
    outcome: str,
    host: str,
    *,
    exit_code: int = 0,
    stdout: str = "",
    stderr: str = "",
    duration_ms: int = 0,
    connect_reused: bool = False,
    job_id: str | None = None,
    **extra: Any,
) -> dict[str, Any]:
    """Build the discriminated result envelope shared by every tool.

    ``stdout_bytes`` is always present — including as an explicit ``0`` — so
    the model cannot misread silence on success as absence of signal.
    """
    out, out_trunc = _truncate(stdout)
    err, _ = _truncate(stderr)
    env: dict[str, Any] = {
        "outcome": outcome,
        "host": host,
        "exit_code": exit_code,
        "stdout": out,
        "stdout_bytes": len((stdout or "").encode("utf-8", "replace")),
        "stdout_truncated": out_trunc,
        "stderr": err,
        "duration_ms": duration_ms,
        "connect_reused": connect_reused,
        "job_id": job_id,
    }
    env.update(extra)
    return env


def _result(env: dict[str, Any]) -> dict[str, Any]:
    """Wrap an envelope as a fir tool result, flagging error outcomes."""
    return {
        "content": [{"type": "text", "text": json.dumps(env, indent=2)}],
        "is_error": env.get("outcome") in _ERROR_OUTCOMES,
    }


# ---------------------------------------------------------------------------
# Execution core
# ---------------------------------------------------------------------------


def _run_local(argv: list[str], stdin_data: str | None, timeout_s: float):
    """Run *argv* locally, feeding *stdin_data*. Returns (rc, out, err, timed_out).

    On timeout we still recover whatever partial output arrived, because a
    hung remote command's first 200 lines are usually the diagnosis.
    """
    # argv is always built here from a list — never a shell string.
    proc = subprocess.Popen(
        argv,
        stdin=subprocess.PIPE if stdin_data is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        # A remote command may emit non-UTF-8 (cat a binary, a mixed-encoding
        # log). Strict decoding would raise inside communicate() and cost the
        # whole envelope — the exact unstructured failure this tool exists to
        # avoid. Replace instead; stdout_bytes is counted the same way.
        errors="replace",
        start_new_session=True,
    )
    try:
        out, err = proc.communicate(input=stdin_data, timeout=timeout_s)
        return proc.returncode, out or "", err or "", False
    except subprocess.TimeoutExpired:
        proc.kill()
        try:
            out, err = proc.communicate(timeout=10)
        except subprocess.SubprocessError:
            out, err = "", ""
        return _RC_TIMEOUT, out or "", err or "", True


def _ssh_exec(
    host: str,
    script: str,
    timeout_s: float,
    *,
    remote_timeout: bool = True,
    job_id: str | None = None,
) -> dict[str, Any]:
    """Ship *script* to *host* over ssh stdin and return a full envelope.

    When *remote_timeout* is set the remote side runs under ``timeout -k``,
    which puts the command in its own process group and signals the *group* —
    so a timed-out command does not leave orphans behind. The local timeout is
    deliberately looser so the remote one normally wins and we can report the
    clean 124.
    """
    reused = _connection_reused(host)
    if remote_timeout:
        remote_argv = [
            "timeout",
            "-k",
            str(_TIMEOUT_KILL_GRACE),
            str(int(timeout_s)),
            "bash",
            "-l",
            "-s",
        ]
    else:
        remote_argv = ["bash", "-l", "-s"]
    argv = _ssh_argv(host, remote_argv)
    started = time.time()
    rc, out, err, local_timed_out = _run_local(argv, script, timeout_s + _LOCAL_TIMEOUT_SLACK)
    duration_ms = int((time.time() - started) * 1000)

    if local_timed_out or rc == _RC_TIMEOUT:
        return _envelope(
            _OUTCOME_TIMEOUT,
            host,
            exit_code=_RC_TIMEOUT,
            stdout=out,
            stderr=err
            or (
                f"remote command exceeded timeout_s={int(timeout_s)}; "
                "its process group was signalled"
            ),
            duration_ms=duration_ms,
            connect_reused=reused,
            job_id=job_id,
            timeout_s=int(timeout_s),
        )
    outcome = _classify(rc, err)
    return _envelope(
        outcome,
        host,
        exit_code=rc,
        stdout=out,
        stderr=err,
        duration_ms=duration_ms,
        connect_reused=reused,
        job_id=job_id,
    )


# ---------------------------------------------------------------------------
# ~/.ssh/config parsing
# ---------------------------------------------------------------------------


def _ssh_config_path() -> Path:
    return Path.home() / ".ssh" / "config"


def _parse_ssh_config(text: str) -> list[dict[str, Any]]:
    """Extract ``Host`` stanzas with the handful of fields worth showing.

    Wildcard aliases (``Host *``) are kept but flagged as patterns — they are
    config defaults, not connectable hosts, and must not be probed.
    """
    hosts: list[dict[str, Any]] = []
    # Entries created by the most recent Host line — one Host line can declare
    # several aliases, and the following keywords apply to all of them.
    current: list[dict[str, Any]] = []
    fields = {"hostname": "hostname", "user": "user", "port": "port"}
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.replace("=", " ", 1).split(None, 1)
        if len(parts) != 2:
            continue
        key, value = parts[0].lower(), parts[1].strip()
        if key == "host":
            current = [
                {"host": alias, "pattern": any(c in alias for c in "*?!")}
                for alias in value.split()
            ]
            hosts.extend(current)
        elif key in fields:
            for entry in current:
                entry[fields[key]] = value
    return hosts


def _read_ssh_config(path: Path, depth: int = 0) -> str:
    """Read an ssh config, inlining ``Include`` directives (bounded depth)."""
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return ""
    if depth >= 3:
        return text
    out: list[str] = []
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.lower().startswith("include "):
            pattern = stripped.split(None, 1)[1].strip()
            base = Path.home() / ".ssh"
            expanded = os.path.expanduser(pattern)
            candidates = (
                sorted(Path("/").glob(expanded.lstrip("/")))
                if os.path.isabs(expanded)
                else sorted(base.glob(pattern))
            )
            out.extend(_read_ssh_config(inc, depth + 1) for inc in candidates if inc.is_file())
            continue
        out.append(line)
    return "\n".join(out)


# ---------------------------------------------------------------------------
# tmux helpers
# ---------------------------------------------------------------------------

_ANSI_RE = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)")

_TMUX_GUARD = (
    "command -v tmux >/dev/null 2>&1 || "
    f"{{ echo 'tmux not found on remote host' >&2; exit {_RC_NO_TMUX}; }}\n"
)

_NO_TARGET_PATTERNS = (
    "can't find session",
    "can't find window",
    "can't find pane",
    "no such session",
    "session not found",
    "no server running",
)


def _strip_ansi(text: str) -> str:
    return _ANSI_RE.sub("", text)


def _tmux_cmd(args: list[str]) -> str:
    """A single quoted ``tmux ...`` shell line.

    Quoting happens *here*, from a list the tool built — the model never has
    to write a quote, which is the entire point of this extension.
    """
    return " ".join(shlex.quote(a) for a in ["tmux", *args])


def _new_session_name() -> str:
    return "fir-" + secrets.token_hex(3)


def _parse_tmux_ls(stdout: str) -> list[dict[str, Any]]:
    """Parse the two marker-delimited tmux listings into the ls result shape."""
    sessions: dict[str, dict[str, Any]] = {}
    order: list[str] = []
    mode = ""
    for line in stdout.splitlines():
        if line.startswith("---SESSIONS---"):
            mode = "s"
            continue
        if line.startswith("---PANES---"):
            mode = "p"
            continue
        if not line.strip():
            continue
        fields = line.split("\t")
        if mode == "s" and len(fields) >= 3:
            name = fields[0]
            sessions[name] = {
                "name": name,
                "attached": fields[1] not in ("0", ""),
                "activity_ts": int(fields[2]) if fields[2].isdigit() else 0,
                "windows": [],
            }
            order.append(name)
        elif mode == "p" and len(fields) >= 5:
            sess = sessions.get(fields[0])
            if sess is None:
                continue
            idx = int(fields[1]) if fields[1].lstrip("-").isdigit() else fields[1]
            if any(w.get("idx") == idx for w in sess["windows"]):
                continue
            sess["windows"].append(
                {
                    "idx": idx,
                    "name": fields[2],
                    "pane_cmd": fields[3],
                    "pid": int(fields[4]) if fields[4].isdigit() else 0,
                }
            )
    return [sessions[n] for n in order]


# Hash of the last capture per (host, target), so a polling agent that keeps
# re-capturing an idle pane pays nothing after the first look.
_capture_hashes: dict[tuple[str, str], str] = {}
_capture_lock = threading.Lock()


def _capture_unchanged(host: str, target: str, capture: str) -> tuple[bool, str]:
    """Record the capture hash; return (unchanged, hash)."""
    digest = hashlib.sha256(capture.encode("utf-8", "replace")).hexdigest()[:16]
    key = (host, target)
    with _capture_lock:
        previous = _capture_hashes.get(key)
        _capture_hashes[key] = digest
    return previous == digest, digest


def _tmux_outcome(env: dict[str, Any]) -> dict[str, Any]:
    """Reclassify a tmux exec envelope into no_tmux / no_target where apt."""
    if env["outcome"] != _OUTCOME_NONZERO:
        return env
    if env.get("exit_code") == _RC_NO_TMUX:
        env["outcome"] = _OUTCOME_NO_TMUX
        env["hint"] = "install tmux or use rexec detach=True"
        return env
    low = (env.get("stderr") or "").lower()
    if any(p in low for p in _NO_TARGET_PATTERNS):
        env["outcome"] = _OUTCOME_NO_TARGET
        env["hint"] = "run rtmux action=ls to see what exists on this host"
    return env


# ---------------------------------------------------------------------------
# Tool: rexec
# ---------------------------------------------------------------------------

_BOUNDARY_NOTE = (
    "SCOPE: these tools make teleoperation cheap, which is a trap. Use them "
    "for short remote commands, inspection, and file transfer. For "
    "substantial work on a host — a build, a refactor, a debugging session — "
    "do NOT drive it 400 calls at a time from here: rput a brief and spawn an "
    "agent over there (see rtmux new)."
)

_REXEC_DESCRIPTION = (
    "Run a shell command on a remote host over ssh. The script is shipped on "
    "ssh STDIN to `bash -l -s`, so you can pass arbitrary multi-line shell "
    "with quotes, heredocs, globs and $vars EXACTLY as you would type it "
    "locally — nothing is re-split by an intermediate shell. Do not write "
    "ssh flags, `bash -lc`, `timeout N`, or escaped quotes; this tool owns "
    "all of that. Login shell semantics are guaranteed (~/.local/bin on PATH) "
    "even on hosts that default to zsh.\n\n"
    "Returns a structured envelope: outcome (ok | nonzero_exit | timeout | "
    "unreachable | auth_failed), exit_code, stdout/stderr, stdout_bytes "
    "(explicitly 0 when empty — success with no output is signal, not "
    "silence), duration_ms, connect_reused.\n\n"
    "detach=True returns a job_id immediately and runs the command under a "
    "transient systemd unit (or a double-forked setsid) on the remote box, "
    "with output and exit code landing in files there. Poll it with `rjob`. "
    "Use detach for anything longer than a couple of minutes.\n\n" + _BOUNDARY_NOTE
)

_REXEC_PARAMETERS: dict[str, Any] = {
    "type": "object",
    "properties": {
        "host": {
            "type": "string",
            "description": (
                "Target host — anything ssh accepts (a ~/.ssh/config alias, "
                "user@host, an IP). Use `rhosts` to discover configured ones."
            ),
        },
        "command": {
            "type": "string",
            "description": (
                "Shell script to run remotely. Multi-line is fine and "
                "preferred over chained &&. Write it plainly: no escaping, "
                "no outer quotes, no bash -lc wrapper."
            ),
        },
        "timeout_s": {
            "type": "number",
            "description": (
                "Seconds before the remote process GROUP is killed and "
                "outcome=timeout is returned with partial output. Default 120."
            ),
        },
        "cwd": {
            "type": "string",
            "description": "Remote directory to cd into first. Optional.",
        },
        "detach": {
            "type": "boolean",
            "description": (
                "Run in the background on the remote host and return a "
                "job_id immediately. Poll with `rjob`."
            ),
        },
    },
    "required": ["host", "command"],
}


@fir_ext.tool(
    name="rexec",
    description=_REXEC_DESCRIPTION,
    parameters=_REXEC_PARAMETERS,
    display_hint={
        "title_args": [
            {"name": "host", "style": "accent"},
            {"name": "command", "style": ""},
        ],
        "use_box": True,
    },
    # Bounded by our own timeout_s (plus slack), which can legitimately exceed
    # fir's 30s default. The inner subprocess timeout is the real bound.
    timeout=-1,
)
def rexec(params: dict, ctx: fir_ext.Context) -> dict[str, Any]:
    host = (params.get("host") or "").strip()
    command = params.get("command") or ""
    if not host:
        raise fir_ext.ToolError("rexec: 'host' is required")
    _check_host(host)
    if not command.strip():
        raise fir_ext.ToolError("rexec: 'command' is required")
    cwd = params.get("cwd") or None
    timeout_s = float(params.get("timeout_s") or 120)
    if timeout_s <= 0:
        timeout_s = 120.0

    if params.get("detach"):
        job_id = _new_job_id()
        script = _build_script(_detach_scripts(job_id, command, cwd, host), None)
        env = _ssh_exec(host, script, min(timeout_s, 60), job_id=job_id)
        if env["outcome"] == _OUTCOME_OK:
            env["launcher"] = (env.get("stdout") or "").strip()
            env["log_path"] = f"~/.cache/fir/rjobs/{job_id}.log"
            env["hint"] = f"poll with rjob(host={host!r}, id={job_id!r})"
        return _result(env)

    env = _ssh_exec(host, _build_script(command, cwd), timeout_s)
    return _result(env)


# ---------------------------------------------------------------------------
# Tool: rjob
# ---------------------------------------------------------------------------


def _rjob_script(job_id: str, action: str, lines: int) -> str:
    """Remote script for one rjob action. The remote FS is the registry."""
    d = _RJOBS_DIR
    head = (
        f'D="{d}"\n'
        f'[ -e "$D/{job_id}.meta" ] || '
        f"{{ echo 'no such job: {job_id}' >&2; exit {_RC_NO_JOB}; }}\n"
    )
    if action == "kill":
        return (
            head + f'L=$(cat "$D/{job_id}.launcher" 2>/dev/null)\n'
            f'P=$(cat "$D/{job_id}.pid" 2>/dev/null)\n'
            'if [ "$L" = systemd ]; then\n'
            f"  systemctl --user stop {job_id} 2>/dev/null && echo 'stopped unit'\n"
            "fi\n"
            'if [ -n "$P" ]; then\n'
            '  kill -TERM -"$P" 2>/dev/null || kill -TERM "$P" 2>/dev/null\n'
            '  echo "signalled pid $P"\n'
            "fi\n"
            f'[ -f "$D/{job_id}.rc" ] || echo 143 > "$D/{job_id}.rc"\n'
            "echo killed\n"
        )
    tail_expr = {
        "log": f'cat "$D/{job_id}.log" 2>/dev/null',
        "tail": f'tail -n {lines} "$D/{job_id}.log" 2>/dev/null',
        "status": f'tail -n {min(lines, 20)} "$D/{job_id}.log" 2>/dev/null',
    }.get(action, f'tail -n {lines} "$D/{job_id}.log" 2>/dev/null')
    return (
        head + f'echo "META:$(cat "$D/{job_id}.meta" 2>/dev/null | tr -d "\\n")"\n'
        f'P=$(cat "$D/{job_id}.pid" 2>/dev/null)\n'
        'echo "PID:$P"\n'
        f'if [ -f "$D/{job_id}.rc" ]; then\n'
        f'  echo "STATE:done"; echo "RC:$(cat "$D/{job_id}.rc")"\n'
        'elif [ -n "$P" ] && kill -0 "$P" 2>/dev/null; then\n'
        '  echo "STATE:running"; echo "RC:"\n'
        "else\n"
        '  echo "STATE:unknown"; echo "RC:"\n'
        "fi\n"
        f'echo "LOGBYTES:$(wc -c < "$D/{job_id}.log" 2>/dev/null | tr -d " ")"\n'
        'echo "---LOG---"\n' + tail_expr + "\n"
    )


def _parse_rjob_stdout(stdout: str) -> dict[str, Any]:
    """Split the rjob probe output into structured fields plus the log body."""
    info: dict[str, Any] = {"state": "unknown", "job_exit_code": None, "log": ""}
    log_lines: list[str] = []
    in_log = False
    for line in stdout.splitlines():
        if in_log:
            log_lines.append(line)
            continue
        if line == "---LOG---":
            in_log = True
        elif line.startswith("META:"):
            with_json = line[5:].strip()
            try:
                info["meta"] = json.loads(with_json) if with_json else {}
            except ValueError:
                info["meta"] = {"raw": with_json}
        elif line.startswith("PID:"):
            pid = line[4:].strip()
            info["pid"] = int(pid) if pid.isdigit() else None
        elif line.startswith("STATE:"):
            info["state"] = line[6:].strip()
        elif line.startswith("RC:"):
            rc = line[3:].strip()
            info["job_exit_code"] = int(rc) if rc.lstrip("-").isdigit() else None
        elif line.startswith("LOGBYTES:"):
            n = line[9:].strip()
            info["log_bytes"] = int(n) if n.isdigit() else 0
    info["log"] = "\n".join(log_lines)
    return info


_RJOB_DESCRIPTION = (
    "Inspect or stop a detached remote job started by `rexec detach=True`. "
    "State lives entirely on the remote filesystem "
    "(~/.cache/fir/rjobs/<id>.{log,rc,pid}) — there is no local registry to "
    "drift, so this works across fir sessions and even after a restart.\n\n"
    "actions: status (state + exit code + last 20 log lines), log (whole "
    "log), tail (last `lines`), kill (stop the job's process group)."
)

_RJOB_PARAMETERS: dict[str, Any] = {
    "type": "object",
    "properties": {
        "host": {"type": "string", "description": "Host the job runs on."},
        "id": {"type": "string", "description": "Job id returned by rexec."},
        "action": {
            "type": "string",
            "enum": ["status", "log", "tail", "kill"],
            "description": "What to do. Default status.",
        },
        "lines": {
            "type": "integer",
            "description": "Lines for tail (default 40).",
        },
    },
    "required": ["host", "id"],
}


@fir_ext.tool(
    name="rjob",
    description=_RJOB_DESCRIPTION,
    parameters=_RJOB_PARAMETERS,
    display_hint={
        "title_args": [
            {"name": "host", "style": "accent"},
            {"name": "id", "style": ""},
            {"name": "action", "style": "accent"},
        ]
    },
    timeout=-1,
)
def rjob(params: dict, ctx: fir_ext.Context) -> dict[str, Any]:
    host = (params.get("host") or "").strip()
    job_id = (params.get("id") or "").strip()
    action = (params.get("action") or "status").strip()
    lines = int(params.get("lines") or 40)
    if not host:
        raise fir_ext.ToolError("rjob: 'host' is required")
    _check_host(host)
    if not _is_safe_job_id(job_id):
        raise fir_ext.ToolError(f"rjob: invalid job id {job_id!r}")
    if action not in ("status", "log", "tail", "kill"):
        raise fir_ext.ToolError(f"rjob: unknown action {action!r}")

    script = _build_script(_rjob_script(job_id, action, max(1, lines)), None)
    env = _ssh_exec(host, script, 60, job_id=job_id)
    if env["outcome"] == _OUTCOME_NONZERO and env.get("exit_code") == _RC_NO_JOB:
        env["outcome"] = _OUTCOME_NO_TARGET
        env["hint"] = "no such job on this host — check the id and the host"
        return _result(env)
    if env["outcome"] == _OUTCOME_OK and action != "kill":
        info = _parse_rjob_stdout(env.get("stdout") or "")
        env["state"] = info["state"]
        env["job_exit_code"] = info["job_exit_code"]
        env["pid"] = info.get("pid")
        env["meta"] = info.get("meta", {})
        env["log_bytes"] = info.get("log_bytes", 0)
        log, truncated = _truncate(info["log"])
        env["stdout"] = log
        env["stdout_bytes"] = len(info["log"].encode("utf-8", "replace"))
        env["stdout_truncated"] = truncated
    return _result(env)


# ---------------------------------------------------------------------------
# Tools: rput / rget
# ---------------------------------------------------------------------------


def _copy(
    host: str, sources: list[str], dest: str, direction: str, timeout_s: float
) -> dict[str, Any]:
    reused = _connection_reused(host)
    argv = _scp_argv(sources, dest)
    started = time.time()
    rc, out, err, timed_out = _run_local(argv, None, timeout_s)
    duration_ms = int((time.time() - started) * 1000)
    if timed_out:
        return _envelope(
            _OUTCOME_TIMEOUT,
            host,
            exit_code=_RC_TIMEOUT,
            stdout=out,
            stderr=err or f"scp exceeded timeout_s={int(timeout_s)}",
            duration_ms=duration_ms,
            connect_reused=reused,
            direction=direction,
        )
    outcome = _classify(rc, err) if rc != 1 else _OUTCOME_NONZERO
    if rc == 1 and any(p in (err or "").lower() for p in _AUTH_PATTERNS):
        outcome = _OUTCOME_AUTH_FAILED
    elif rc == 1 and any(p in (err or "").lower() for p in _UNREACHABLE_PATTERNS):
        outcome = _OUTCOME_UNREACHABLE
    return _envelope(
        outcome,
        host,
        exit_code=rc,
        stdout=out,
        stderr=err,
        duration_ms=duration_ms,
        connect_reused=reused,
        direction=direction,
        sources=sources,
        dest=dest,
    )


_RPUT_DESCRIPTION = (
    "Copy a local file or directory to a remote host (scp, same connection "
    "flags and mux as rexec, recursive, preserves mode/mtime). Structured "
    "envelope like rexec.\n\n"
    "This is half of the delegation primitive: when a remote task is "
    "substantial, `rput` a written brief onto the host, `rtmux new` a fir "
    "agent there with that brief, and steer it with rtmux send/cap — instead "
    "of teleoperating the work one rexec at a time."
)

_RGET_DESCRIPTION = (
    "Copy a file or directory from a remote host to the local filesystem "
    "(scp, same connection flags and mux as rexec, recursive). Structured "
    "envelope like rexec. Use it to pull back logs, artefacts, or a diff for "
    "local Read/Edit — remote paths are never readable by Read/Grep/Edit."
)

_RPUT_PARAMETERS: dict[str, Any] = {
    "type": "object",
    "properties": {
        "host": {"type": "string", "description": "Destination host."},
        "local": {"type": "string", "description": "Local path (file or dir)."},
        "remote": {
            "type": "string",
            "description": "Remote destination path. ~ is expanded remotely.",
        },
        "timeout_s": {"type": "number", "description": "Default 300."},
    },
    "required": ["host", "local", "remote"],
}

_RGET_PARAMETERS: dict[str, Any] = {
    "type": "object",
    "properties": {
        "host": {"type": "string", "description": "Source host."},
        "remote": {"type": "string", "description": "Remote path (file or dir)."},
        "local": {"type": "string", "description": "Local destination path."},
        "timeout_s": {"type": "number", "description": "Default 300."},
    },
    "required": ["host", "remote", "local"],
}


@fir_ext.tool(
    name="rput",
    description=_RPUT_DESCRIPTION,
    parameters=_RPUT_PARAMETERS,
    display_hint={
        "title_args": [
            {"name": "host", "style": "accent"},
            {"name": "local", "style": "path"},
            {"name": "remote", "style": "path"},
        ]
    },
    timeout=-1,
)
def rput(params: dict, ctx: fir_ext.Context) -> dict[str, Any]:
    host = (params.get("host") or "").strip()
    local = params.get("local") or ""
    remote = params.get("remote") or ""
    if not (host and local and remote):
        raise fir_ext.ToolError("rput: 'host', 'local' and 'remote' are required")
    _check_host(host)
    timeout_s = float(params.get("timeout_s") or 300)
    local_path = os.path.expanduser(local)
    if not os.path.exists(local_path):
        raise fir_ext.ToolError(f"rput: local path does not exist: {local_path}")
    env = _copy(host, [local_path], f"{host}:{remote}", "put", timeout_s)
    if env["outcome"] == _OUTCOME_OK:
        env["local_bytes"] = _path_size(local_path)
    return _result(env)


@fir_ext.tool(
    name="rget",
    description=_RGET_DESCRIPTION,
    parameters=_RGET_PARAMETERS,
    display_hint={
        "title_args": [
            {"name": "host", "style": "accent"},
            {"name": "remote", "style": "path"},
            {"name": "local", "style": "path"},
        ]
    },
    timeout=-1,
)
def rget(params: dict, ctx: fir_ext.Context) -> dict[str, Any]:
    host = (params.get("host") or "").strip()
    remote = params.get("remote") or ""
    local = params.get("local") or ""
    if not (host and local and remote):
        raise fir_ext.ToolError("rget: 'host', 'remote' and 'local' are required")
    _check_host(host)
    timeout_s = float(params.get("timeout_s") or 300)
    local_path = os.path.expanduser(local)
    env = _copy(host, [f"{host}:{remote}"], local_path, "get", timeout_s)
    if env["outcome"] == _OUTCOME_OK:
        env["local_bytes"] = _path_size(local_path)
        env["local_path"] = os.path.abspath(local_path)
    return _result(env)


def _path_size(path: str) -> int:
    """Total bytes at *path* (recursing into directories)."""
    try:
        if os.path.isfile(path):
            return os.path.getsize(path)
        total = 0
        for root, _dirs, files in os.walk(path):
            for name in files:
                with_path = os.path.join(root, name)
                if os.path.isfile(with_path):
                    total += os.path.getsize(with_path)
        return total
    except OSError:
        return 0


# ---------------------------------------------------------------------------
# Tool: rtmux
# ---------------------------------------------------------------------------

_RTMUX_DESCRIPTION = (
    "Drive tmux on a remote host: the PTY lives over there, inside tmux, and "
    "every action here is a stateless short ssh exec — no held ssh -tt "
    "channel, so nothing hangs on a stuck read.\n\n"
    "actions:\n"
    "  ls   — live inventory of every tmux session/window on the host "
    "(name, pane command, pid). This is how you discover what is running on "
    "a box, including other fir agents.\n"
    "  new  — create a detached session (auto-named fir-xxxxxx) optionally "
    "running `command` in `cwd`. Pass command and cwd as plain arguments; "
    "this tool builds the argv, so you never write a quote or an escape.\n"
    "  send — type into a session: `text` (literal, escaping handled here) "
    'and/or `keys` (e.g. ["Enter", "C-c"]). Returns a short tail so a '
    "blind send is still visible.\n"
    "  cap  — capture the pane (tmux does the terminal emulation). Returns "
    "`unchanged: true` instead of re-emitting an identical pane, so polling "
    "is cheap.\n"
    "  kill — tear the session down.\n\n"
    "DELEGATION: `rtmux new` is the launch primitive for real remote work — "
    "rput a brief onto the host, `rtmux new` a fir agent there running it, "
    "then check in with cap. Prefer that over teleoperating a long job."
)

_RTMUX_PARAMETERS: dict[str, Any] = {
    "type": "object",
    "properties": {
        "host": {"type": "string", "description": "Host running tmux."},
        "action": {
            "type": "string",
            "enum": ["ls", "new", "send", "cap", "kill"],
            "description": "Which action to perform.",
        },
        "target": {
            "type": "string",
            "description": (
                "tmux target for send/cap/kill — a session name, or session:window.pane. From `ls`."
            ),
        },
        "text": {
            "type": "string",
            "description": (
                "Literal text to type (send). Escaping is handled here — "
                "write it exactly as a human would type it."
            ),
        },
        "keys": {
            "type": "array",
            "items": {"type": "string"},
            "description": 'Key names to send after `text`, e.g. ["Enter"], ["C-c"].',
        },
        "command": {
            "type": "string",
            "description": "Command for a new session to run (action=new).",
        },
        "cwd": {
            "type": "string",
            "description": "Working directory for a new session (action=new).",
        },
        "name": {
            "type": "string",
            "description": "Session name for action=new. Auto-generated if omitted.",
        },
        "lines": {
            "type": "integer",
            "description": "History lines to capture (action=cap). Default 120.",
        },
    },
    "required": ["host", "action"],
}


def _tmux_ls_script() -> str:
    return (
        _TMUX_GUARD
        + "echo '---SESSIONS---'\n"
        + _tmux_cmd(
            [
                "list-sessions",
                "-F",
                "#{session_name}\t#{session_attached}\t#{session_activity}",
            ]
        )
        + " 2>/dev/null\n"
        "echo '---PANES---'\n"
        + _tmux_cmd(
            [
                "list-panes",
                "-a",
                "-F",
                "#{session_name}\t#{window_index}\t#{window_name}\t"
                "#{pane_current_command}\t#{pane_pid}",
            ]
        )
        + " 2>/dev/null\n"
        "exit 0\n"
    )


def _pane_settle_fragment(name: str) -> str:
    """Wait until a freshly created pane's shell is actually reading input.

    This exists because of a real failure: text sent into a session whose
    shell is still initialising is echoed by the tty line discipline and then
    *discarded* when the shell flushes typeahead at startup — so the keys
    appear in the pane, look sent, and never run. We poll until the pane is
    non-empty (a prompt has been drawn) and byte-stable across two samples,
    capped so a slow host costs seconds, not the whole call.
    """
    cap = _tmux_cmd(["capture-pane", "-p", "-t", name])
    return (
        "prev=\n"
        "for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do\n"
        f"  cur=$({cap} 2>/dev/null | cksum)\n"
        f'  body=$({cap} 2>/dev/null | tr -d "[:space:]")\n'
        '  if [ -n "$body" ] && [ "$cur" = "$prev" ]; then break; fi\n'
        '  prev="$cur"\n'
        "  sleep 0.25\n"
        "done\n"
    )


def _tmux_new_script(name: str, command: str | None, cwd: str | None) -> str:
    args = ["new-session", "-d", "-s", name]
    if cwd:
        args.extend(["-c", cwd])
    if command:
        args.append(command)
    script = _TMUX_GUARD + _tmux_cmd(args) + " || exit $?\n"
    if not command:
        # Only an interactive shell needs the settle — a session launched with
        # a command is not going to be typed into before it prints anything.
        script += _pane_settle_fragment(name)
    return script + _tmux_cmd(["has-session", "-t", name]) + "\n"


def _tmux_send_script(target: str, text: str | None, keys: list[str], lines: int) -> str:
    parts = [
        _TMUX_GUARD,
        _tmux_cmd(["has-session", "-t", target.split(":")[0]]) + " || exit $?",
    ]
    if text:
        parts.append(_tmux_cmd(["send-keys", "-t", target, "-l", "--", text]) + " || exit $?")
    parts.extend(_tmux_cmd(["send-keys", "-t", target, key]) + " || exit $?" for key in keys)
    parts.append("sleep 0.4")
    parts.append("echo '---TAIL---'")
    parts.append(_tmux_cmd(["capture-pane", "-p", "-t", target, "-S", f"-{lines}"]))
    return "\n".join(parts) + "\n"


def _tmux_cap_script(target: str, lines: int) -> str:
    return (
        _TMUX_GUARD
        + _tmux_cmd(["capture-pane", "-p", "-t", target, "-S", f"-{lines}"])
        + " || exit $?\n"
        "echo '---CURSOR---'\n"
        + _tmux_cmd(["display-message", "-p", "-t", target, "#{cursor_flag}"])
        + "\n"
    )


def _tmux_kill_script(target: str) -> str:
    return _TMUX_GUARD + _tmux_cmd(["kill-session", "-t", target]) + " || exit $?\n"


def _split_marker(stdout: str, marker: str) -> tuple[str, str]:
    """Split *stdout* at a whole-line *marker*. Returns (before, after)."""
    lines = stdout.splitlines()
    for i, line in enumerate(lines):
        if line.strip() == marker:
            return "\n".join(lines[:i]), "\n".join(lines[i + 1 :])
    return stdout, ""


@fir_ext.tool(
    name="rtmux",
    description=_RTMUX_DESCRIPTION,
    parameters=_RTMUX_PARAMETERS,
    display_hint={
        "title_args": [
            {"name": "host", "style": "accent"},
            {"name": "action", "style": "accent"},
            {"name": "target", "style": ""},
        ],
        "use_box": True,
    },
    timeout=-1,
)
def rtmux(params: dict, ctx: fir_ext.Context) -> dict[str, Any]:
    host = (params.get("host") or "").strip()
    action = (params.get("action") or "").strip()
    if not host:
        raise fir_ext.ToolError("rtmux: 'host' is required")
    _check_host(host)
    if action not in ("ls", "new", "send", "cap", "kill"):
        raise fir_ext.ToolError(f"rtmux: unknown action {action!r}")
    target = (params.get("target") or "").strip()
    lines = max(1, int(params.get("lines") or 120))

    if action == "ls":
        env = _tmux_outcome(_ssh_exec(host, _build_script(_tmux_ls_script(), None), 45))
        if env["outcome"] == _OUTCOME_OK:
            env["sessions"] = _parse_tmux_ls(env.get("stdout") or "")
            env["stdout"] = ""
            env["stdout_bytes"] = 0
            env["session_count"] = len(env["sessions"])
        return _result(env)

    if action == "new":
        name = (params.get("name") or "").strip() or _new_session_name()
        script = _tmux_new_script(name, params.get("command") or None, params.get("cwd"))
        env = _tmux_outcome(_ssh_exec(host, _build_script(script, None), 45))
        if env["outcome"] == _OUTCOME_OK:
            env["name"] = name
            env["hint"] = (
                f"steer with rtmux(action='send', target='{name}', ...) and "
                f"rtmux(action='cap', target='{name}')"
            )
        return _result(env)

    if not target:
        raise fir_ext.ToolError(f"rtmux: 'target' is required for action={action!r}")

    if action == "kill":
        env = _tmux_outcome(_ssh_exec(host, _build_script(_tmux_kill_script(target), None), 30))
        if env["outcome"] == _OUTCOME_OK:
            env["killed"] = target
        with _capture_lock:
            _capture_hashes.pop((host, target), None)
        return _result(env)

    if action == "send":
        text = params.get("text")
        keys = params.get("keys") or []
        if not isinstance(keys, list):
            raise fir_ext.ToolError("rtmux: 'keys' must be a list of key names")
        if not text and not keys:
            raise fir_ext.ToolError("rtmux: action=send needs 'text' and/or 'keys'")
        script = _tmux_send_script(target, text, [str(k) for k in keys], min(lines, 40))
        env = _tmux_outcome(_ssh_exec(host, _build_script(script, None), 45))
        if env["outcome"] == _OUTCOME_OK:
            _, tail = _split_marker(env.get("stdout") or "", "---TAIL---")
            tail = _strip_ansi(tail).rstrip()
            env["stdout"] = tail
            env["stdout_bytes"] = len(tail.encode("utf-8", "replace"))
            env["sent_text"] = text or ""
            env["sent_keys"] = keys
            # A send always invalidates our capture memo — the pane just moved.
            with _capture_lock:
                _capture_hashes.pop((host, target), None)
        return _result(env)

    # action == "cap"
    env = _tmux_outcome(_ssh_exec(host, _build_script(_tmux_cap_script(target, lines), None), 45))
    if env["outcome"] == _OUTCOME_OK:
        body, cursor = _split_marker(env.get("stdout") or "", "---CURSOR---")
        capture = _strip_ansi(body).rstrip()
        unchanged, digest = _capture_unchanged(host, target, capture)
        capture_bytes = len(capture.encode("utf-8", "replace"))
        shown, truncated = _truncate(capture)
        env["capture"] = "" if unchanged else shown
        env["capture_bytes"] = capture_bytes
        env["truncated"] = truncated
        env["unchanged"] = unchanged
        env["capture_hash"] = digest
        env["cursor_visible"] = cursor.strip() in ("1", "on")
        env["stdout"] = ""
        env["stdout_bytes"] = 0
        if unchanged:
            env["hint"] = "pane identical to the last capture — nothing re-emitted"
    return _result(env)


# ---------------------------------------------------------------------------
# Tool: rhosts
# ---------------------------------------------------------------------------

_RHOSTS_DESCRIPTION = (
    "List the hosts configured in ~/.ssh/config (Include directives followed) "
    "with their HostName/User/Port. This is the only host source — there is "
    "no fir-side registry, and rexec/rtmux accept anything ssh accepts, "
    "including hosts not listed here.\n\n"
    "probe=True additionally runs a parallel `ssh <host> true` sweep and "
    "classifies each as reachable / unreachable / auth_failed. Wildcard "
    "stanzas (Host *) are listed but never probed."
)

_RHOSTS_PARAMETERS: dict[str, Any] = {
    "type": "object",
    "properties": {
        "probe": {
            "type": "boolean",
            "description": (
                "Run a parallel reachability sweep. Costs one short ssh per "
                "host; warms the connection mux as a side effect."
            ),
        },
        "timeout_s": {
            "type": "number",
            "description": "Per-host probe timeout in seconds. Default 15.",
        },
    },
}


def _probe_host(host: str, timeout_s: float) -> dict[str, Any]:
    started = time.time()
    rc, _out, err, timed_out = _run_local(_ssh_argv(host, ["true"]), None, timeout_s)
    duration_ms = int((time.time() - started) * 1000)
    if timed_out:
        status = _OUTCOME_UNREACHABLE
    elif rc == 0:
        status = "reachable"
    else:
        status = _classify(rc, err)
        if status == _OUTCOME_NONZERO:
            status = _OUTCOME_UNREACHABLE
    entry: dict[str, Any] = {"host": host, "status": status, "duration_ms": duration_ms}
    if status != "reachable":
        entry["stderr"] = (err or "").strip()[:400]
    return entry


@fir_ext.tool(
    name="rhosts",
    description=_RHOSTS_DESCRIPTION,
    parameters=_RHOSTS_PARAMETERS,
    display_hint={"title_args": [{"name": "probe", "style": "accent"}]},
    timeout=-1,
)
def rhosts(params: dict, ctx: fir_ext.Context) -> dict[str, Any]:
    path = _ssh_config_path()
    text = _read_ssh_config(path)
    hosts = _parse_ssh_config(text)
    env: dict[str, Any] = {
        "outcome": _OUTCOME_OK,
        "host": "",
        "exit_code": 0,
        "stdout": "",
        "stdout_bytes": 0,
        "stdout_truncated": False,
        "stderr": "" if text else f"no readable ssh config at {path}",
        "duration_ms": 0,
        "connect_reused": False,
        "job_id": None,
        "config_path": str(path),
        "hosts": hosts,
        "host_count": len(hosts),
    }
    if not params.get("probe"):
        return _result(env)

    timeout_s = float(params.get("timeout_s") or 15)
    targets = [h["host"] for h in hosts if not h.get("pattern") and not h["host"].startswith("-")]
    started = time.time()
    results: list[dict[str, Any]] = []
    if targets:
        workers = min(8, len(targets))
        with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
            results = list(pool.map(lambda h: _probe_host(h, timeout_s), targets))
    by_host = {r["host"]: r for r in results}
    for entry in hosts:
        probe = by_host.get(entry["host"])
        if probe:
            entry["status"] = probe["status"]
            entry["probe_ms"] = probe["duration_ms"]
            if probe.get("stderr"):
                entry["probe_stderr"] = probe["stderr"]
    env["duration_ms"] = int((time.time() - started) * 1000)
    env["reachable"] = [r["host"] for r in results if r["status"] == "reachable"]
    env["unreachable"] = [r["host"] for r in results if r["status"] == _OUTCOME_UNREACHABLE]
    env["auth_failed"] = [r["host"] for r in results if r["status"] == _OUTCOME_AUTH_FAILED]
    return _result(env)


fir_ext.run(name="remote")
