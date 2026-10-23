#!/usr/bin/env python3
# ---
# name: agent-introspect
# description: Expose the agent_introspect tool so the agent can inspect its own runtime state
# builtin: true
# ---
"""Single-tool extension that returns a structured snapshot of the current
fir runtime/session. Wraps the host ``agent.info`` RPC."""

import json

import fir_ext


@fir_ext.tool(
    name="agent_introspect",
    description=(
        "Return a JSON snapshot of the current agent runtime: version, mode, "
        "session, model, context usage, tools (via counts), thinking level, "
        "message counts, token totals, cost, and mcp.servers[] — every "
        "configured MCP server with state (ok/connecting/needs_auth/"
        "token_expired/error/disabled), config source, error, tool count "
        "and the fir command that fixes it."
    ),
    parameters={"type": "object", "properties": {}},
)
def agent_introspect(_params, ctx):
    info = ctx.agent_info()
    return json.dumps(info, indent=2)


fir_ext.run(name="agent-introspect")
