#!/usr/bin/env python3
"""fleet-bridge: render a fleet agent's session/update stream into ANSI text.

Typical use — wire one pane per agent in a tmux window:

    tmux -S /tmp/fleet-demo/view.sock new-session -d -s fleet
    tmux -S /tmp/fleet-demo/view.sock new-window -n worker-1 \
        "python3 fleet_bridge.py worker-1"
    tmux -S /tmp/fleet-demo/view.sock attach

The bridge just prints nicely-formatted ANSI to stdout; tmux gives you
scrollback, copy-mode search, splits, detach/reattach, multi-viewer, SSH.

You can also pipe to `less +F`, `tee`, etc. — it's just a stream.
"""

from __future__ import annotations

import argparse
import json
import os
import socket
import sys
import time
from pathlib import Path


# Minimal ANSI palette.
RESET = "\x1b[0m"
DIM = "\x1b[2m"
BOLD = "\x1b[1m"
RED = "\x1b[31m"
GREEN = "\x1b[32m"
YELLOW = "\x1b[33m"
BLUE = "\x1b[34m"
MAGENTA = "\x1b[35m"
CYAN = "\x1b[36m"
GREY = "\x1b[90m"


def colorize(kind: str) -> str:
    return {
        "agent_message_chunk": GREEN,
        "agent_thought_chunk": MAGENTA,
        "tool_call": YELLOW,
        "tool_call_update": DIM + YELLOW,
        "plan": CYAN,
        "user_message_chunk": BOLD + BLUE,
    }.get(kind, "")


def render(rec: dict) -> str:
    ts = time.strftime("%H:%M:%S", time.localtime(rec.get("ts", 0)))
    upd = (rec.get("update") or {}).get("update") or {}
    kind = upd.get("sessionUpdate") or "?"
    col = colorize(kind)
    prefix = f"{GREY}[{ts}]{RESET} {col}{kind:<20}{RESET} "

    if kind in ("agent_message_chunk", "agent_thought_chunk", "user_message_chunk"):
        content = upd.get("content") or {}
        text = content.get("text", "")
        # Each chunk often has no trailing newline; keep streaming feel.
        return prefix + text

    if kind == "tool_call":
        title = upd.get("title") or upd.get("kind") or ""
        tid = (upd.get("toolCallId") or "")[:8]
        loc = upd.get("locations") or []
        loc_s = ""
        if loc:
            first = loc[0]
            loc_s = f" {GREY}{first.get('path','')}{RESET}"
            if first.get("line") is not None:
                loc_s += f":{first['line']}"
        return f"{prefix}{BOLD}{title}{RESET}  {GREY}({tid}){RESET}{loc_s}\n"

    if kind == "tool_call_update":
        tid = (upd.get("toolCallId") or "")[:8]
        status = upd.get("status") or ""
        col2 = GREEN if status == "completed" else (RED if status == "failed" else DIM)
        return f"{prefix}{col2}{status}{RESET} {GREY}{tid}{RESET}\n"

    if kind == "plan":
        lines = [prefix + "plan:"]
        for e in upd.get("entries", []):
            st = e.get("status", "?")
            sym = {"completed": "✓", "in_progress": "●", "pending": "·"}.get(st, "?")
            col2 = {"completed": GREEN, "in_progress": YELLOW, "pending": DIM}.get(st, "")
            lines.append(f"  {col2}{sym} {e.get('content','')}{RESET}")
        return "\n".join(lines) + "\n"

    # Fallback: compact JSON.
    return prefix + DIM + json.dumps(upd)[:200] + RESET + "\n"


def default_socket(fleet: str | None) -> Path:
    if "FLEET_SOCKET" in os.environ:
        return Path(os.environ["FLEET_SOCKET"])
    name = fleet or os.environ.get("FLEET_NAME") or "default"
    base = os.environ.get("XDG_RUNTIME_DIR") or "/tmp"
    return Path(base) / f"fleet-{name}" / "ctl.sock"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("name", help="agent name")
    ap.add_argument("--fleet", default=None)
    ap.add_argument("--from-log", action="store_true",
                    help="read the persisted jsonl log instead of live tail (tail -F style)")
    args = ap.parse_args()

    sock_path = default_socket(args.fleet)

    if args.from_log:
        log = sock_path.parent / f"{args.name}.jsonl"
        # tail -F style: sleep-poll.
        pos = 0
        while True:
            if log.exists():
                with log.open() as f:
                    f.seek(pos)
                    for line in f:
                        try:
                            sys.stdout.write(render(json.loads(line)))
                            sys.stdout.flush()
                        except json.JSONDecodeError:
                            pass
                    pos = f.tell()
            time.sleep(0.3)
        return  # unreachable, kept for clarity

    # Live tail via control socket.
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.connect(str(sock_path))
    s.sendall((json.dumps({"cmd": "tail", "name": args.name}) + "\n").encode())
    f = s.makefile("r")
    print(f"{BOLD}fleet-bridge: tailing {args.name} on {sock_path}{RESET}", flush=True)
    for line in f:
        try:
            rec = json.loads(line)
        except json.JSONDecodeError:
            continue
        if rec.get("tailing"):
            continue
        sys.stdout.write(render(rec))
        sys.stdout.flush()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
