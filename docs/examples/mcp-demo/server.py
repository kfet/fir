#!/usr/bin/env python3
"""
Minimal demo MCP server over stdio (JSON-RPC 2.0).

No external dependencies — just the Python standard library.

Tools:
  echo — returns the input message

Resources:
  demo://readme — a small static resource

Configure in .fir/mcp.json:

    {
      "mcpServers": {
        "demo": {
          "command": "python3",
          "args": [".fir/mcp-servers/demo/server.py"]
        }
      }
    }

See docs/examples/mcp-demo/README.md for setup instructions.
"""

import json
import sys

# ---------------------------------------------------------------------------
# Server metadata
# ---------------------------------------------------------------------------

SERVER_NAME = "demo"
SERVER_VERSION = "0.1.0"
PROTOCOL_VERSION = "2024-11-05"

# ---------------------------------------------------------------------------
# Tool definitions
# ---------------------------------------------------------------------------

TOOLS = [
    {
        "name": "echo",
        "description": "Echo the input message back.",
        "inputSchema": {
            "type": "object",
            "properties": {"message": {"type": "string", "description": "Text to echo"}},
            "required": ["message"],
        },
    },
]

# ---------------------------------------------------------------------------
# Resource definitions
# ---------------------------------------------------------------------------

RESOURCES = [
    {
        "uri": "demo://readme",
        "name": "Demo Readme",
        "description": "A small static resource for testing.",
        "mimeType": "text/plain",
    },
]

RESOURCE_CONTENTS = {
    "demo://readme": "This is the demo MCP server readme.\nIt exposes a few simple tools for testing /mcp.",
}

# ---------------------------------------------------------------------------
# Tool handlers
# ---------------------------------------------------------------------------


def handle_echo(args):
    return [{"type": "text", "text": f"echo: {args.get('message', '')}"}]


TOOL_HANDLERS = {
    "echo": handle_echo,
}

# ---------------------------------------------------------------------------
# JSON-RPC helpers
# ---------------------------------------------------------------------------


def send(obj):
    """Write a JSON-RPC message to stdout."""
    line = json.dumps(obj, separators=(",", ":"))
    sys.stdout.write(line + "\n")
    sys.stdout.flush()


def ok(id, result):
    send({"jsonrpc": "2.0", "id": id, "result": result})


def error(id, code, message):
    send({"jsonrpc": "2.0", "id": id, "error": {"code": code, "message": message}})


# ---------------------------------------------------------------------------
# Request dispatch
# ---------------------------------------------------------------------------


def handle(msg):
    method = msg.get("method")
    id = msg.get("id")
    params = msg.get("params", {})

    if method == "initialize":
        ok(
            id,
            {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {
                    "tools": {},
                    "resources": {},
                },
                "serverInfo": {"name": SERVER_NAME, "version": SERVER_VERSION},
            },
        )
    elif method == "notifications/initialized":
        pass  # no response for notifications
    elif method == "tools/list":
        ok(id, {"tools": TOOLS})
    elif method == "tools/call":
        name = params.get("name", "")
        args = params.get("arguments", {})
        handler = TOOL_HANDLERS.get(name)
        if handler is None:
            error(id, -32602, f"Unknown tool: {name}")
        else:
            try:
                content = handler(args)
                ok(id, {"content": content})
            except Exception as exc:
                ok(id, {"content": [{"type": "text", "text": str(exc)}], "isError": True})
    elif method == "resources/list":
        ok(id, {"resources": RESOURCES})
    elif method == "resources/read":
        uri = params.get("uri", "")
        text = RESOURCE_CONTENTS.get(uri)
        if text is None:
            error(id, -32602, f"Unknown resource: {uri}")
        else:
            ok(id, {"contents": [{"uri": uri, "mimeType": "text/plain", "text": text}]})
    elif method == "ping":
        ok(id, {})
    elif method is not None and method.startswith("notifications/"):
        pass  # ignore all notifications silently
    elif id is not None:
        error(id, -32601, f"Method not found: {method}")


# ---------------------------------------------------------------------------
# Main loop
# ---------------------------------------------------------------------------


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            continue
        handle(msg)


if __name__ == "__main__":
    main()
