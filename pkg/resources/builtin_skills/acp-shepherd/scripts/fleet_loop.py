#!/usr/bin/env python3
"""fleet-loop: poll a fleet and print a compact shepherd dashboard.

Each tick:
  - Lists agents with alive/idle/last-kind.
  - Computes per-agent activity (new updates since last tick, last tool call).
  - Detects "idle" (no updates for --idle-threshold seconds) and "stuck"
    (no `tool_call_update` progress for --stuck-threshold seconds while
    updates still flowing — analysis-paralysis shape).
  - Optionally runs a test command in --worktree and prints pass/fail.
  - Optionally writes FLEET.md alongside the fleet state dir.

Not autonomous — it just prints facts. The shepherd agent reads the
output and decides what to do (assign tasks, cancel, /new equivalent).

Usage:
    fleet-loop                        # one tick
    fleet-loop --watch                # loop forever, 10s cadence
    fleet-loop --watch --interval 5
    fleet-loop --test "make test" --worktree /path/to/wt
    fleet-loop --write-fleet-md
"""

from __future__ import annotations

import argparse
import json
import os
import socket
import subprocess
import sys
import time
from pathlib import Path


def sock_and_state(fleet: str | None) -> tuple[Path, Path]:
    if "FLEET_SOCKET" in os.environ:
        s = Path(os.environ["FLEET_SOCKET"])
        return s, s.parent
    name = fleet or os.environ.get("FLEET_NAME") or "default"
    base = Path(os.environ.get("XDG_RUNTIME_DIR") or "/tmp") / f"fleet-{name}"
    return base / "ctl.sock", base


def rpc(sock_path: Path, req: dict) -> dict:
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.connect(str(sock_path))
    s.sendall((json.dumps(req) + "\n").encode())
    line = s.makefile("r").readline()
    s.close()
    return json.loads(line)


def tail_log(path: Path, last_n: int) -> list[dict]:
    if not path.exists():
        return []
    out = []
    for line in path.read_text().splitlines()[-last_n:]:
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return out


def summarize_agent(recs: list[dict]) -> dict:
    """Extract shepherd-relevant facts from an update tail."""
    last_tool = None
    last_tool_status = None
    last_msg = None
    last_plan = None
    kinds: dict[str, int] = {}
    for r in recs:
        upd = (r.get("update") or {}).get("update") or {}
        kind = upd.get("sessionUpdate")
        if not kind:
            continue
        kinds[kind] = kinds.get(kind, 0) + 1
        if kind == "tool_call":
            last_tool = upd.get("title") or upd.get("kind")
        elif kind == "tool_call_update":
            last_tool_status = upd.get("status")
        elif kind == "agent_message_chunk":
            text = (upd.get("content") or {}).get("text", "")
            if text.strip():
                last_msg = text.strip()[-200:]
        elif kind == "plan":
            entries = upd.get("entries") or []
            done = sum(1 for e in entries if e.get("status") == "completed")
            last_plan = f"{done}/{len(entries)}"
    return {
        "last_tool": last_tool,
        "last_tool_status": last_tool_status,
        "last_msg": last_msg,
        "last_plan": last_plan,
        "kinds": kinds,
    }


def render_dashboard(sock: Path, state: Path, args) -> str:
    now = time.time()
    try:
        res = rpc(sock, {"cmd": "status"})
    except (FileNotFoundError, ConnectionRefusedError):
        return f"fleet daemon not running at {sock}\n"
    agents = res.get("agents", [])
    if not agents:
        return "(no agents)\n"

    lines = []
    lines.append(time.strftime("=== fleet-loop %Y-%m-%d %H:%M:%S ==="))
    for a in agents:
        name = a["name"]
        log = state / f"{name}.jsonl"
        recs = tail_log(log, 200)
        summ = summarize_agent(recs)

        flags = []
        if not a["alive"]:
            flags.append("DEAD")
        if a["idle_s"] >= args.idle_threshold:
            flags.append(f"IDLE>{int(args.idle_threshold)}s")
        # Stuck = updates still happening but no tool progress for a while.
        tool_updates = summ["kinds"].get("tool_call_update", 0)
        if a["updates"] > 5 and tool_updates == 0 and a["idle_s"] < 30:
            flags.append("NO_TOOLS")
        flag_s = " ".join(flags) if flags else "ok"

        lines.append(f"  [{name}] {flag_s}  "
                     f"updates={a['updates']} idle={a['idle_s']}s "
                     f"plan={summ['last_plan'] or '-'} "
                     f"last_tool={summ['last_tool'] or '-'} ({summ['last_tool_status'] or '-'})")
        if summ["last_msg"]:
            snippet = summ["last_msg"].replace("\n", " ")[:140]
            lines.append(f"      msg: {snippet}")

    if args.test:
        t0 = time.time()
        try:
            r = subprocess.run(
                args.test, shell=True, cwd=args.worktree or None,
                capture_output=True, text=True, timeout=args.test_timeout,
            )
            ok = r.returncode == 0
            lines.append(f"  test: {'PASS' if ok else 'FAIL'} "
                         f"(rc={r.returncode} in {time.time()-t0:.1f}s)")
            if not ok:
                tail = (r.stderr or r.stdout).splitlines()[-5:]
                for ln in tail:
                    lines.append(f"    | {ln}")
        except subprocess.TimeoutExpired:
            lines.append(f"  test: TIMEOUT after {args.test_timeout}s")

    out = "\n".join(lines) + "\n"

    if args.write_fleet_md:
        md_path = Path(args.write_fleet_md) if args.write_fleet_md != "-" \
                  else state / "FLEET.md"
        fleet_md(agents, args, md_path)

    return out


def fleet_md(agents: list[dict], args, path: Path) -> None:
    lines = [
        "# FLEET",
        "",
        f"- Updated: {time.strftime('%Y-%m-%d %H:%M:%S')}",
        f"- Worktree: {args.worktree or '(not set)'}",
        "",
        "| Agent | Alive | Updates | Idle (s) | Last activity |",
        "| --- | --- | --- | --- | --- |",
    ]
    for a in agents:
        lines.append(f"| {a['name']} | {a['alive']} | {a['updates']} | "
                     f"{a['idle_s']} | {a.get('last_kind') or '-'} |")
    path.write_text("\n".join(lines) + "\n")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fleet", default=None)
    ap.add_argument("--watch", action="store_true")
    ap.add_argument("--interval", type=float, default=10.0)
    ap.add_argument("--idle-threshold", type=float, default=60.0,
                    help="flag agents idle longer than this (seconds)")
    ap.add_argument("--test", default=None,
                    help="shell command; runs each tick in --worktree")
    ap.add_argument("--test-timeout", type=float, default=120.0)
    ap.add_argument("--worktree", default=None)
    ap.add_argument("--write-fleet-md", nargs="?", const="-", default=None,
                    help="write FLEET.md (default path: <state>/FLEET.md)")
    args = ap.parse_args()

    sock, state = sock_and_state(args.fleet)

    if not args.watch:
        sys.stdout.write(render_dashboard(sock, state, args))
        return

    try:
        while True:
            sys.stdout.write(render_dashboard(sock, state, args))
            sys.stdout.flush()
            time.sleep(args.interval)
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
