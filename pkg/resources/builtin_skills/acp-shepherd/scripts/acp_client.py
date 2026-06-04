"""Minimal ACP (Agent Client Protocol) client over stdio.

Speaks JSON-RPC 2.0 line-delimited JSON to a child process launched with
something like `fir --mode acp`, `claude-code --acp`, `gemini --experimental-acp`,
etc. Handles:

  - initialize handshake
  - session/new (with cwd + mcpServers)
  - session/prompt (fire-and-forget; updates stream via session/update)
  - session/cancel
  - incoming session/update, session/request_permission notifications

The client is async (asyncio). One AcpAgent == one child subprocess ==
one stdio connection, potentially hosting multiple ACP sessions.

For shepherd usage we keep it simple: one session per agent. Nothing
stops you from running multiple though.

Reference: https://agentclientprotocol.com/
"""

from __future__ import annotations

import asyncio
import json
import os
import shlex
from dataclasses import dataclass, field
from typing import Any, Awaitable, Callable, Optional


UpdateHandler = Callable[[dict], Awaitable[None]]


@dataclass
class AcpAgent:
    name: str
    cmd: list[str]                 # argv for the agent subprocess
    cwd: str                       # working dir passed to session/new
    env: dict[str, str] = field(default_factory=dict)
    mcp_servers: list[dict] = field(default_factory=list)  # ACP mcpServers
    on_update: Optional[UpdateHandler] = None              # async fn(dict)

    # runtime state
    proc: Optional[asyncio.subprocess.Process] = None
    session_id: Optional[str] = None
    _next_id: int = 1
    _pending: dict[int, asyncio.Future] = field(default_factory=dict)
    _reader_task: Optional[asyncio.Task] = None
    _stderr_task: Optional[asyncio.Task] = None
    _write_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    _initialized: bool = False
    _closed: bool = False

    # --- lifecycle -------------------------------------------------------

    async def start(self) -> None:
        env = {**os.environ, **self.env}
        self.proc = await asyncio.create_subprocess_exec(
            *self.cmd,
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            cwd=self.cwd,
            env=env,
        )
        self._reader_task = asyncio.create_task(self._read_loop())
        self._stderr_task = asyncio.create_task(self._drain_stderr())
        await self._initialize()

    async def _initialize(self) -> None:
        # ACP initialize: advertise our client capabilities.
        # We ignore the agent's reply; if it requires authenticate, session/new
        # will fail loudly and the user should pre-auth.
        await self._request("initialize", {
            "protocolVersion": 1,
            "clientCapabilities": {
                "fs": {"readTextFile": False, "writeTextFile": False},
                "terminal": False,
            },
        })
        self._initialized = True

    async def new_session(self) -> str:
        if not self._initialized:
            raise RuntimeError("agent not initialized")
        params = {
            "cwd": self.cwd,
            "mcpServers": self.mcp_servers,
        }
        res = await self._request("session/new", params)
        self.session_id = res["sessionId"]
        return self.session_id

    async def prompt(self, text: str) -> str:
        """Send a prompt. Returns stopReason once the turn ends."""
        if not self.session_id:
            raise RuntimeError("no session")
        params = {
            "sessionId": self.session_id,
            "prompt": [{"type": "text", "text": text}],
        }
        # session/prompt returns once the turn is done (stopReason).
        # The agent streams session/update notifications meanwhile.
        res = await self._request("session/prompt", params, timeout=None)
        return res.get("stopReason", "end_turn")

    async def cancel(self) -> None:
        if not self.session_id:
            return
        await self._notify("session/cancel", {"sessionId": self.session_id})

    async def close(self) -> None:
        self._closed = True
        if self.proc and self.proc.returncode is None:
            try:
                self.proc.terminate()
                await asyncio.wait_for(self.proc.wait(), timeout=5)
            except asyncio.TimeoutError:
                self.proc.kill()
                try:
                    await asyncio.wait_for(self.proc.wait(), timeout=2)
                except asyncio.TimeoutError:
                    pass
            except ProcessLookupError:
                pass
        if self._reader_task:
            self._reader_task.cancel()
        if self._stderr_task:
            self._stderr_task.cancel()

    # --- JSON-RPC plumbing ----------------------------------------------

    async def _request(self, method: str, params: dict, timeout: Optional[float] = 60) -> Any:
        rid = self._next_id
        self._next_id += 1
        fut: asyncio.Future = asyncio.get_running_loop().create_future()
        self._pending[rid] = fut
        await self._send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
        if timeout is None:
            return await fut
        return await asyncio.wait_for(fut, timeout=timeout)

    async def _notify(self, method: str, params: dict) -> None:
        await self._send({"jsonrpc": "2.0", "method": method, "params": params})

    async def _send(self, msg: dict) -> None:
        line = (json.dumps(msg) + "\n").encode()
        async with self._write_lock:
            assert self.proc and self.proc.stdin
            self.proc.stdin.write(line)
            await self.proc.stdin.drain()

    async def _read_loop(self) -> None:
        assert self.proc and self.proc.stdout
        try:
            while True:
                line = await self.proc.stdout.readline()
                if not line:
                    break
                line = line.strip()
                if not line:
                    continue
                try:
                    msg = json.loads(line)
                except json.JSONDecodeError:
                    continue
                await self._dispatch(msg)
        except asyncio.CancelledError:
            pass
        finally:
            # Fail any pending requests.
            for fut in self._pending.values():
                if not fut.done():
                    fut.set_exception(RuntimeError("agent closed"))
            self._pending.clear()

    async def _dispatch(self, msg: dict) -> None:
        if "id" in msg and ("result" in msg or "error" in msg):
            fut = self._pending.pop(msg["id"], None)
            if fut and not fut.done():
                if "error" in msg:
                    fut.set_exception(RuntimeError(f"agent error: {msg['error']}"))
                else:
                    fut.set_result(msg.get("result"))
            return

        method = msg.get("method")
        params = msg.get("params", {}) or {}

        if method == "session/update":
            if self.on_update:
                await self.on_update(params)
            return

        if method == "session/request_permission":
            # Auto-allow for unattended fleets. Make this configurable later.
            rid = msg.get("id")
            if rid is not None:
                opts = params.get("options") or []
                chosen = None
                for o in opts:
                    if o.get("kind") == "allow_always" or o.get("kind") == "allow_once":
                        chosen = o.get("optionId")
                        break
                if chosen is None and opts:
                    chosen = opts[0].get("optionId")
                await self._send({
                    "jsonrpc": "2.0", "id": rid,
                    "result": {"outcome": {"outcome": "selected", "optionId": chosen}},
                })
            return

        # Unknown request — reply with method-not-found so the agent isn't stuck.
        if "id" in msg:
            await self._send({
                "jsonrpc": "2.0", "id": msg["id"],
                "error": {"code": -32601, "message": f"method not found: {method}"},
            })

    async def _drain_stderr(self) -> None:
        """Swallow stderr so the child doesn't block; caller can redirect elsewhere."""
        assert self.proc and self.proc.stderr
        try:
            while True:
                line = await self.proc.stderr.readline()
                if not line:
                    break
        except Exception:
            pass


def parse_cmd(cmd: str | list[str]) -> list[str]:
    if isinstance(cmd, list):
        return cmd
    return shlex.split(cmd)
