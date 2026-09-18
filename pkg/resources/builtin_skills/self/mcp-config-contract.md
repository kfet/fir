# MCP configuration reference — `.fir/mcp.json`

This is the **authoritative** field reference for MCP server configuration,
embedded verbatim from fir's own source (`pkg/mcp/config_contract.go`). It
cannot go stale: adding a field to the Go struct changes this page.

Every exported field below maps to the JSON key in its `json:"…"` tag. Fields
marked `omitempty` are optional; the doc comment on each one is the
specification for it.

```go
{{FIR_MCP_CONFIG_CONTRACT}}
```
