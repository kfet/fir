"""Fleet daemon: holds N ACP agents alive, exposes a control socket.

One daemon per fleet. Agents are spawned as subprocesses via AcpAgent.
Control clients (the `fleet` CLI) connect over a Unix socket and send
line-delimited JSON commands. Agent updates are logged to a per-agent
JSONL file and also broadcast to any `fleet tail` subscribers.

Wire protocol (control socket, one JSON object per line):

    -> {"cmd": "spawn",  "name": "...", "argv": [...] or "string",
        "cwd": "...", "env": {...}, "mcp": [...]}
    <- {"ok": true, "session_id": "..."}

    -> {"cmd": "prompt", "name": "...", "text": "..."}
    <- {"ok": true, "stop_reason": "end_turn"}      (when turn ends)

    -> {"cmd": "cancel", "name": "..."}
    -> {"cmd": "status"}                -> {"agents": [...]}
    -> {"cmd": "kill",   "name": "..."}
    -> {"cmd": "shutdown"}
    -> {"cmd": "tail",   "name": "..."}  # streams updates until client disconnects

Each agent's updates are also appended to:
    <state_dir>/<name>.jsonl
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import signal
import sys
import time
from pathlib import Path

# Allow running from anywhere.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from acp_client import AcpAgent, parse_cmd  # noqa: E402


class Fleet:
    def __init__(self, state_dir: Path):
        self.state_dir = state_dir
        state_dir.mkdir(parents=True, exist_ok=True)
        self.agents: dict[str, AcpAgent] = {}
        # name -> list of asyncio.Queue for tail subscribers
        self.subs: dict[str, list[asyncio.Queue]] = {}
        # summary counters per agent
        self.stats: dict[str, dict] = {}

    def _log_path(self, name: str) -> Path:
        return self.state_dir / f"{name}.jsonl"

    async def _handle_update(self, name: str, params: dict) -> None:
        rec = {"ts": time.time(), "agent": name, "update": params}
        # Append to file.
        try:
            with self._log_path(name).open("a") as f:
                f.write(json.dumps(rec) + "\n")
        except Exception:
            pass
        # Fan out to subscribers.
        for q in list(self.subs.get(name, [])):
            try:
                q.put_nowait(rec)
            except asyncio.QueueFull:
                pass
        # Bump counters.
        s = self.stats.setdefault(name, {"updates": 0, "last_ts": 0.0, "last_kind": None})
        s["updates"] += 1
        s["last_ts"] = rec["ts"]
        upd = params.get("update") or {}
        s["last_kind"] = upd.get("sessionUpdate") or upd.get("type")

    async def spawn(self, name: str, cmd, cwd: str, env: dict, mcp: list) -> dict:
        if name in self.agents:
            return {"ok": False, "error": f"agent {name!r} already exists"}
        argv = parse_cmd(cmd)
        agent = AcpAgent(
            name=name,
            cmd=argv,
            cwd=cwd or os.getcwd(),
            env=env or {},
            mcp_servers=mcp or [],
            on_update=lambda p, _n=name: self._handle_update(_n, p),
        )
        try:
            await agent.start()
            sid = await agent.new_session()
        except Exception as e:
            await agent.close()
            return {"ok": False, "error": str(e)}
        self.agents[name] = agent
        self.stats[name] = {"updates": 0, "last_ts": time.time(), "last_kind": "spawned",
                            "cmd": argv, "cwd": agent.cwd, "session_id": sid}
        return {"ok": True, "session_id": sid}

    async def prompt(self, name: str, text: str) -> dict:
        agent = self.agents.get(name)
        if not agent:
            return {"ok": False, "error": f"no agent {name!r}"}
        try:
            stop = await agent.prompt(text)
        except Exception as e:
            return {"ok": False, "error": str(e)}
        return {"ok": True, "stop_reason": stop}

    async def cancel(self, name: str) -> dict:
        agent = self.agents.get(name)
        if not agent:
            return {"ok": False, "error": "unknown agent"}
        await agent.cancel()
        return {"ok": True}

    async def kill(self, name: str) -> dict:
        agent = self.agents.pop(name, None)
        if not agent:
            return {"ok": False, "error": "unknown agent"}
        await agent.close()
        return {"ok": True}

    def status(self) -> dict:
        now = time.time()
        out = []
        for name, agent in self.agents.items():
            s = self.stats.get(name, {})
            out.append({
                "name": name,
                "session_id": agent.session_id,
                "cwd": agent.cwd,
                "cmd": s.get("cmd"),
                "updates": s.get("updates", 0),
                "last_kind": s.get("last_kind"),
                "idle_s": round(now - s.get("last_ts", now), 1),
                "alive": agent.proc is not None and agent.proc.returncode is None,
            })
        return {"agents": out}

    async def shutdown(self) -> None:
        for name in list(self.agents):
            await self.kill(name)


async def _serve_client(fleet: Fleet, stop: asyncio.Future,
                        reader: asyncio.StreamReader, writer: asyncio.StreamWriter):
    async def send(obj):
        writer.write((json.dumps(obj) + "\n").encode())
        try:
            await writer.drain()
        except Exception:
            pass

    try:
        line = await reader.readline()
        if not line:
            return
        req = json.loads(line.decode())
        cmd = req.get("cmd")

        if cmd == "spawn":
            res = await fleet.spawn(
                req["name"], req["argv"],
                req.get("cwd") or os.getcwd(),
                req.get("env") or {},
                req.get("mcp") or [],
            )
            await send(res)

        elif cmd == "prompt":
            # Each client connection is its own coroutine, so concurrent
            # `fleet prompt` invocations naturally run in parallel.
            res = await fleet.prompt(req["name"], req["text"])
            await send(res)

        elif cmd == "cancel":
            await send(await fleet.cancel(req["name"]))

        elif cmd == "kill":
            await send(await fleet.kill(req["name"]))

        elif cmd == "status":
            await send(fleet.status())

        elif cmd == "tail":
            name = req["name"]
            q: asyncio.Queue = asyncio.Queue(maxsize=1000)
            fleet.subs.setdefault(name, []).append(q)
            await send({"ok": True, "tailing": name})
            try:
                while True:
                    rec = await q.get()
                    await send(rec)
            except (ConnectionResetError, BrokenPipeError):
                pass
            finally:
                subs = fleet.subs.get(name)
                if subs and q in subs:
                    subs.remove(q)

        elif cmd == "shutdown":
            await send({"ok": True})
            # Signal the main loop to unwind; it awaits fleet.shutdown() in its
            # finally block. Doing it here would race with in-flight handlers.
            if not stop.done():
                stop.set_result(None)

        else:
            await send({"ok": False, "error": f"unknown cmd {cmd!r}"})
    except Exception as e:
        await send({"ok": False, "error": f"server exception: {e}"})
    finally:
        try:
            writer.close()
            await writer.wait_closed()
        except Exception:
            pass


async def main_async(sock_path: str, state_dir: str):
    # Clean stale socket.
    try:
        os.unlink(sock_path)
    except FileNotFoundError:
        pass
    os.makedirs(os.path.dirname(sock_path) or ".", exist_ok=True)
    fleet = Fleet(Path(state_dir))

    loop = asyncio.get_running_loop()
    stop = loop.create_future()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, lambda: stop.done() or stop.set_result(None))

    server = await asyncio.start_unix_server(
        lambda r, w: _serve_client(fleet, stop, r, w),
        path=sock_path,
    )
    os.chmod(sock_path, 0o600)

    print(f"fleetd: listening on {sock_path}", flush=True)
    try:
        await stop
    finally:
        await fleet.shutdown()
        server.close()
        try:
            os.unlink(sock_path)
        except FileNotFoundError:
            pass


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--socket", required=True)
    ap.add_argument("--state-dir", required=True)
    args = ap.parse_args()
    asyncio.run(main_async(args.socket, args.state_dir))


if __name__ == "__main__":
    main()
