# ACP Auth Methods RFD Support

## Summary

Implement support for the [ACP Auth Methods RFD](https://agentclientprotocol.com/rfds/auth-methods),
which extends `AuthMethod` with typed auth methods so clients can present better auth UIs.

## RFD Spec (extracted)

The RFD adds a `type` field to `AuthMethod` with three variants:

### 1. `"agent"` (default, backward-compatible)
Agent handles auth itself (current behavior). No extra fields.
```json
{"id": "123", "name": "Agent", "description": "...", "type": "agent"}
```

### 2. `"env_var"`
Client collects a key/secret and passes it as an env var (may restart agent process).
```json
{"id": "123", "name": "OpenAI api key", "type": "env_var", "varName": "OPEN_AI_KEY", "link": "https://..."}
```

### 3. `"terminal"`
Client runs the agent binary in an interactive terminal with extra args/env for TUI login.
```json
{"id": "123", "name": "Run in terminal", "type": "terminal", "args": ["--setup"], "env": {"VAR1": "value1"}}
```

### Auth Errors
`AUTH_REQUIRED` JSON-RPC error (code -32000) can include `authMethods` array to narrow options per model.

## Current State

- SDK v0.6.3 `AuthMethod` has: `Id`, `Name`, `Description`, `Meta` — no `type` field.
- fir's `Initialize()` returns empty `AuthMethods` slice.
- fir's `Authenticate()` is a no-op stub.
- fir already has OAuth login (`/login` command) but doesn't advertise it via ACP auth methods.

## Implementation Plan

### Phase 1: Extended AuthMethod types (local, since SDK doesn't have them yet)

**File: `pkg/modes/acp/types.go`** — Add extended auth method types:

```go
// AuthMethodType is the type discriminator for auth methods.
type AuthMethodType string

const (
    AuthMethodTypeAgent   AuthMethodType = "agent"
    AuthMethodTypeEnvVar  AuthMethodType = "env_var"
    AuthMethodTypeTerminal AuthMethodType = "terminal"
)

// ExtendedAuthMethod wraps acpsdk.AuthMethod with RFD fields.
// Uses Meta for type-specific data until the SDK adds native support.
type ExtendedAuthMethod struct {
    acpsdk.AuthMethod
    Type    AuthMethodType    `json:"type,omitempty"`
    VarName string            `json:"varName,omitempty"`  // env_var only
    Link    string            `json:"link,omitempty"`      // env_var only
    Args    []string          `json:"args,omitempty"`      // terminal only
    Env     map[string]string `json:"env,omitempty"`       // terminal only
}
```

### Phase 2: Populate auth methods in Initialize()

In `firAgent.Initialize()`, build the auth methods list based on configured providers:

1. For each provider with OAuth support → `type: "agent"` auth method
2. For each provider accepting API keys → `type: "env_var"` auth method with the correct env var name
3. Optionally a `type: "terminal"` method for interactive `fir --setup` flow

This requires a small mapping of provider → env var name (e.g., `anthropic` → `ANTHROPIC_API_KEY`).

### Phase 3: Handle Authenticate with method type awareness

Update `Authenticate()` to:
- For `agent` type: trigger OAuth flow (existing `/login` logic)
- For `env_var` type: check if the env var is set and configure the provider
- For `terminal` type: no-op (client handles this externally)

### Phase 4: Auth error responses

Add helper to return AUTH_REQUIRED error (code -32000) with narrowed `authMethods` list when a session fails due to missing credentials.

### Phase 5: Tests

- Unit tests for auth method construction from provider config
- Test Initialize returns correct auth methods
- Test Authenticate dispatches correctly per type
- Test auth error includes narrowed methods
- E2E test with mock connection

## SDK Migration Note

When the acp-go-sdk adds native `Type` field to `AuthMethod`, we should:
1. Remove local `ExtendedAuthMethod` type
2. Use SDK types directly
3. The JSON wire format should be identical, so no protocol changes needed

## Risks

- RFD is not yet finalized — spec may change
- SDK doesn't support these types yet — we use local types that embed SDK ones
- `env_var` type requires client to restart agent process with env var, which is client-side behavior we can't control

## Files to Change

| File | Change |
|------|--------|
| `pkg/modes/acp/types.go` | Add extended auth method types |
| `pkg/modes/acp/auth.go` | New file: auth method builder + authenticate handler logic |
| `pkg/modes/acp/acp.go` | Update Initialize() and Authenticate() to use new logic |
| `pkg/modes/acp/conn.go` | Handle auth error responses |
| `pkg/modes/acp/auth_test.go` | New file: tests for auth methods |
| `pkg/modes/acp/acp_test.go` | Update existing tests for new Initialize response |
