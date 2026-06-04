#!/usr/bin/env python3
"""fleet: CLI for the ACP fleet daemon.

Usage:
    fleet init [--name NAME]                     # start a daemon for this fleet
    fleet spawn NAME [--provider P] [--model M] [--cwd DIR]
                     [--cmd "argv..."] [--mcp-config FILE] [--env K=V]...
    fleet prompt NAME TEXT...                    # send a prompt, wait for turn end
    fleet cancel NAME
    fleet kill   NAME
    fleet status [--json]                        # one-line-per-agent table
    fleet tail   NAME [--pretty|--raw]           # stream structured updates
    fleet capture NAME [--last N] [--pretty]     # last N updates from the jsonl
    fleet log-path NAME                          # print path to JSONL log
    fleet shutdown

The daemon socket is:
    $FLEET_SOCKET  (if set)
    else  ${XDG_RUNTIME_DIR:-/tmp}/fleet-<name>/ctl.sock

State (JSONL logs) live alongside the socket.
"""

from __future__ import annotations

import argparse
import json
import os
import shlex
import socket
import sys
import time
from pathlib import Path


# ---------- socket discovery ----------

def default_base(name: str) -> Path:
    base = os.environ.get("XDG_RUNTIME_DIR") or "/tmp"
    return Path(base) / f"fleet-{name}"


def resolve_paths(name: str | None = None) -> tuple[Path, Path]:
    """Return (socket_path, state_dir)."""
    if "FLEET_SOCKET" in os.environ:
        sock = Path(os.environ["FLEET_SOCKET"])
        state = sock.parent
        return sock, state
    n = name or os.environ.get("FLEET_NAME") or "default"
    base = default_base(n)
    return base / "ctl.sock", base


# ---------- client ----------

def send(sock_path: Path, req: dict, stream: bool = False):
    """One-shot request/response, or generator of responses if stream=True."""
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.connect(str(sock_path))
    s.sendall((json.dumps(req) + "\n").encode())
    f = s.makefile("r")
    if not stream:
        line = f.readline()
        s.close()
        return json.loads(line)
    def gen():
        try:
            for line in f:
                yield json.loads(line)
        finally:
            s.close()
    return gen()


def die(msg, code=1):
    print(f"fleet: {msg}", file=sys.stderr)
    sys.exit(code)


# ---------- commands ----------

def cmd_init(args):
    sock, state = resolve_paths(args.fleet or args.name)
    if sock.exists():
        try:
            send(sock, {"cmd": "status"})
            print(f"fleet already running at {sock}")
            return
        except Exception:
            sock.unlink(missing_ok=True)
    state.mkdir(parents=True, exist_ok=True)
    here = Path(__file__).resolve().parent
    fleetd = here / "fleetd.py"
    logf = state / "fleetd.log"
    # Detach.
    pid = os.fork()
    if pid > 0:
        # Wait for socket.
        for _ in range(100):
            if sock.exists():
                print(f"fleet daemon started: {sock}")
                print(f"export FLEET_SOCKET={sock}")
                return
            time.sleep(0.05)
        die("daemon did not start in time; see " + str(logf))
    os.setsid()
    with open(os.devnull, "rb") as devnull:
        os.dup2(devnull.fileno(), 0)
    with open(logf, "ab") as lf:
        os.dup2(lf.fileno(), 1)
        os.dup2(lf.fileno(), 2)
    os.execvp(sys.executable, [sys.executable, str(fleetd),
                               "--socket", str(sock),
                               "--state-dir", str(state)])


def _parse_envs(pairs: list[str]) -> dict:
    out = {}
    for p in pairs or []:
        if "=" not in p:
            die(f"bad --env {p!r}; expected K=V")
        k, v = p.split("=", 1)
        out[k] = v
    return out


def _load_mcp(path: str | None) -> list:
    if not path:
        return []
    data = json.loads(Path(path).read_text())
    # Accept either the ACP wire shape (list of {name,command,args,env})
    # or the common Claude-Desktop shape: {"mcpServers": {name: {command, args, env}}}
    if isinstance(data, list):
        return data
    servers = data.get("mcpServers") or {}
    out = []
    for name, spec in servers.items():
        out.append({
            "name": name,
            "command": spec.get("command"),
            "args": spec.get("args", []),
            "env": [{"name": k, "value": v} for k, v in (spec.get("env") or {}).items()],
        })
    return out


def cmd_spawn(args):
    sock, _ = resolve_paths(args.fleet)
    if args.cmd:
        cmd = shlex.split(args.cmd)
    else:
        # Default: fir --mode acp
        cmd = ["fir", "--mode", "acp"]
        if args.provider:
            cmd += ["--provider", args.provider]
        if args.model:
            cmd += ["--model", args.model]
        cmd += ["-d", "auto-namer", "-d", "notify",
                "-d", "provider-usage", "-d", "tmuxspinner"]
    req = {
        "cmd": "spawn",
        "name": args.name,
        "argv": cmd,
        "cwd": args.cwd or os.getcwd(),
        "env": _parse_envs(args.env),
        "mcp": _load_mcp(args.mcp_config),
    }
    res = send(sock, req)
    if not res.get("ok"):
        die(res.get("error", "spawn failed"))
    print(f"spawned {args.name} session={res['session_id']}")


def cmd_prompt(args):
    sock, _ = resolve_paths(args.fleet)
    text = " ".join(args.text)
    if not text and not sys.stdin.isatty():
        text = sys.stdin.read()
    if not text:
        die("empty prompt")
    res = send(sock, {"cmd": "prompt", "name": args.name, "text": text})
    if not res.get("ok"):
        die(res.get("error", "prompt failed"))
    print(f"stop_reason={res.get('stop_reason')}")


def cmd_cancel(args):
    sock, _ = resolve_paths(args.fleet)
    res = send(sock, {"cmd": "cancel", "name": args.name})
    if not res.get("ok"):
        die(res.get("error"))


def cmd_kill(args):
    sock, _ = resolve_paths(args.fleet)
    res = send(sock, {"cmd": "kill", "name": args.name})
    if not res.get("ok"):
        die(res.get("error"))


def cmd_status(args):
    sock, _ = resolve_paths(args.fleet)
    res = send(sock, {"cmd": "status"})
    if args.json:
        print(json.dumps(res, indent=2))
        return
    agents = res.get("agents", [])
    if not agents:
        print("(no agents)")
        return
    print(f"{'NAME':<16} {'ALIVE':<5} {'UPDATES':>7} {'IDLE_S':>7}  LAST                CMD")
    for a in agents:
        cmd = " ".join(a.get("cmd") or [])[:40]
        print(f"{a['name']:<16} {str(a['alive']):<5} {a['updates']:>7} "
              f"{a['idle_s']:>7}  {str(a.get('last_kind') or ''):<19} {cmd}")


def _pretty_update(rec: dict) -> str:
    upd = rec.get("update", {}).get("update") or {}
    kind = upd.get("sessionUpdate") or "?"
    ts = time.strftime("%H:%M:%S", time.localtime(rec.get("ts", 0)))
    if kind == "agent_message_chunk":
        content = upd.get("content") or {}
        text = content.get("text", "")
        return f"[{ts}] msg: {text}"
    if kind == "agent_thought_chunk":
        content = upd.get("content") or {}
        return f"[{ts}] think: {content.get('text','')[:120]}"
    if kind == "tool_call":
        return f"[{ts}] tool: {upd.get('title') or upd.get('kind')}  ({upd.get('toolCallId','')[:8]})"
    if kind == "tool_call_update":
        status = upd.get("status")
        return f"[{ts}] tool_update: {upd.get('toolCallId','')[:8]} {status}"
    if kind == "plan":
        entries = upd.get("entries", [])
        return f"[{ts}] plan: {len(entries)} entries"
    if kind == "user_message_chunk":
        content = upd.get("content") or {}
        return f"[{ts}] user: {content.get('text','')[:80]}"
    return f"[{ts}] {kind}: {json.dumps(upd)[:120]}"


def cmd_tail(args):
    sock, _ = resolve_paths(args.fleet)
    for rec in send(sock, {"cmd": "tail", "name": args.name}, stream=True):
        if rec.get("tailing"):
            continue
        if args.raw:
            print(json.dumps(rec))
        else:
            print(_pretty_update(rec))
        sys.stdout.flush()


def cmd_capture(args):
    _, state = resolve_paths(args.fleet)
    path = state / f"{args.name}.jsonl"
    if not path.exists():
        die(f"no log at {path}")
    lines = path.read_text().splitlines()[-args.last:]
    for line in lines:
        try:
            rec = json.loads(line)
        except json.JSONDecodeError:
            continue
        print(_pretty_update(rec) if args.pretty else json.dumps(rec))


def cmd_log_path(args):
    _, state = resolve_paths(args.fleet)
    print(state / f"{args.name}.jsonl")


def cmd_shutdown(args):
    sock, _ = resolve_paths(args.fleet)
    if not sock.exists():
        die(f"no daemon at {sock}")
    try:
        send(sock, {"cmd": "shutdown"})
    except (ConnectionRefusedError, FileNotFoundError):
        # Socket went away as we were connecting — treat as already stopped.
        pass


# ---------- argparse ----------

def build_parser():
    ap = argparse.ArgumentParser(prog="fleet")
    ap.add_argument("--fleet", default=None,
                    help="fleet name (also FLEET_NAME env); ignored if FLEET_SOCKET set")
    sub = ap.add_subparsers(dest="command", required=True)

    p = sub.add_parser("init"); p.add_argument("--name", default=None); p.set_defaults(func=cmd_init)

    p = sub.add_parser("spawn")
    p.add_argument("name")
    p.add_argument("--provider")
    p.add_argument("--model")
    p.add_argument("--cwd")
    p.add_argument("--cmd", help="full argv for the agent (overrides --provider/--model); default: fir --mode acp ...")
    p.add_argument("--mcp-config", help="path to an MCP config JSON (Claude-Desktop shape accepted)")
    p.add_argument("--env", action="append", default=[], help="K=V; repeatable")
    p.set_defaults(func=cmd_spawn)

    p = sub.add_parser("prompt"); p.add_argument("name"); p.add_argument("text", nargs="*"); p.set_defaults(func=cmd_prompt)
    p = sub.add_parser("cancel"); p.add_argument("name"); p.set_defaults(func=cmd_cancel)
    p = sub.add_parser("kill");   p.add_argument("name"); p.set_defaults(func=cmd_kill)

    p = sub.add_parser("status"); p.add_argument("--json", action="store_true"); p.set_defaults(func=cmd_status)

    p = sub.add_parser("tail")
    p.add_argument("name")
    p.add_argument("--raw", action="store_true")
    p.add_argument("--pretty", action="store_true", default=True)
    p.set_defaults(func=cmd_tail)

    p = sub.add_parser("capture")
    p.add_argument("name")
    p.add_argument("--last", type=int, default=50)
    p.add_argument("--pretty", action="store_true", default=True)
    p.set_defaults(func=cmd_capture)

    p = sub.add_parser("log-path"); p.add_argument("name"); p.set_defaults(func=cmd_log_path)
    p = sub.add_parser("shutdown"); p.set_defaults(func=cmd_shutdown)
    return ap


def main():
    ap = build_parser()
    args = ap.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
