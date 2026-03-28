# Demo MCP Server

A minimal MCP server in pure Python (no dependencies) for testing and
demonstrating fir's MCP integration.

## Tools

| Tool   | Description             |
|--------|-------------------------|
| `echo` | Returns the input message |

## Resources

| URI             | Description              |
|-----------------|--------------------------|
| `demo://readme` | A small static text blob |

## Setup

Copy the server into your project and configure it in `.fir/mcp.json`:

```bash
mkdir -p .fir/mcp-servers/demo
cp docs/examples/mcp-demo/server.py .fir/mcp-servers/demo/
```

```json
{
  "mcpServers": {
    "demo": {
      "command": "python3",
      "args": [".fir/mcp-servers/demo/server.py"]
    }
  }
}
```

Then start fir and run `/mcp` to see the server, or `/mcp demo` for full
tool details.

## Standalone test

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}' \
  | python3 docs/examples/mcp-demo/server.py
```
