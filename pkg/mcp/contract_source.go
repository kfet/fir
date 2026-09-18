package mcp

import _ "embed"

// ConfigContractSource is the verbatim source of config_contract.go — the
// user-facing MCP configuration contract, with its doc comments intact.
//
// It is embedded rather than mirrored in prose: documentation that restates
// the struct fields goes stale silently (it did — allow_private_network was
// added in v1.14.0 and the self skill lost it until v1.14.1). Serving the
// source makes that class of drift impossible. The self skill exposes this as
// an on-demand resource file; see expandSkillPlaceholders in pkg/resources.
//
//go:embed config_contract.go
var ConfigContractSource string
