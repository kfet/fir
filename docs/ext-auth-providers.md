# External Auth Providers

## Overview

Auth providers are a new **capability** within the existing extension system. Any
external process extension can declare one or more `auth_providers` in its init
handshake response. Fir owns credential storage; the extension owns the login
flow and token refresh logic. Fir exposes helpers (PKCE generation, local
callback server) so extensions don't need to reimplement OAuth plumbing.

This is **not** a new extension type — it extends the existing capability model
(tools, commands, events) with a fourth capability: auth providers.

## Design Principles

1. **Fir owns storage.** Credentials are persisted in `pkg/auth/authstorage.go`,
   same as built-in providers. Extensions never touch the filesystem for creds.
2. **Extension owns the flow.** The extension orchestrates login (builds URLs,
   exchanges codes, calls external APIs). Fir just provides helpers and UI.
3. **Fir proxies UI callbacks.** Browser opening, progress messages, and user
   prompts go through fir's TUI — the extension sends RPC requests/notifications
   and fir renders them.
4. **Fir provides OAuth helpers.** PKCE generation and the local HTTP callback
   server are offered as RPC methods the extension can call, so it doesn't need
   to reimplement them.
5. **Language-agnostic, Python-first.** The protocol is JSON-RPC over stdio. The
   Python SDK (`fir_ext.py`) gets first-class decorator support. Any language
   that speaks JSON-RPC works.

## Protocol

### Init Handshake

Extensions declare auth providers in the `init` response:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "name": "my-corp-sso",
    "auth_providers": [
      {
        "id": "my-corp",
        "name": "My Corp SSO",
        "uses_callback_server": true
      }
    ],
    "tools": [],
    "commands": [],
    "events": []
  }
}
```

The `auth_providers` field is optional. Each entry has:

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique provider ID (e.g., `"my-corp"`). Must not collide with built-in IDs. |
| `name` | string | Human-readable name shown in the OAuth selector UI. |
| `uses_callback_server` | bool | Whether login uses a local HTTP callback and supports manual code input fallback. |

### Auth RPC Methods (fir → extension)

These are JSON-RPC requests sent from fir to the extension process:

#### `auth/login`

Start the login flow. The extension should use helper RPCs (below) to interact
with the user and OAuth endpoints, then return credentials.

**Params:**
```json
{
  "provider_id": "my-corp"
}
```

**Result:**
```json
{
  "credentials": {
    "access": "sk-...",
    "refresh": "rt-...",
    "expires": 1717000000000,
    "extra": {}
  }
}
```

The extension may also return an error (`jsonrpc error`) if login fails or is
cancelled.

During the login flow, the extension calls helper RPCs and sends notifications
(see below).

#### `auth/refresh`

Refresh expired credentials.

**Params:**
```json
{
  "provider_id": "my-corp",
  "credentials": {
    "access": "...",
    "refresh": "...",
    "expires": 1717000000000,
    "extra": {}
  }
}
```

**Result:**
```json
{
  "credentials": {
    "access": "new-access-token",
    "refresh": "new-refresh-token",
    "expires": 1717100000000,
    "extra": {}
  }
}
```

##### When fir calls it

Three paths reach a provider's refresh, and all three funnel through the same
locked read-decide-rotate-write section in `pkg/auth` — never a second writer:

1. **Mid-turn**, when the stored access token has expired and a request needs a
   key.
2. **`fir auth refresh [provider | provider#account]`**, the zero-inference
   keepalive intended for cron on machines that sit idle. Given a bare provider
   id it walks every stored account slot of the provider; given an account slot
   key (the form it prints itself, and the one `fir login list` / `fir logout`
   speak) it refreshes only that account. Either way it refreshes only
   credentials at or near expiry (`--within`, default 1h; `--force` overrides),
   and exits non-zero if any slot failed.
3. **Manually**, via a re-login, which replaces the credential outright.

##### Rotation is destructive — design for it

Assume the refresh token **rotates on every grant**, and assume the previous
access token is **revoked the moment it does**. Anthropic behaves exactly this
way: the old access token starts returning
`401 OAuth access token has been revoked` immediately, long before its stated
`expires`. Two consequences for provider authors:

- The new `refresh` value from `auth/refresh` **must** be returned so fir can
  persist it. Losing it strands the account at the next refresh. (If your
  provider omits `refresh_token` on a refresh response because nothing changed
  — Google does this — fir carries the previous one forward for you.)
- Never write `auth.json` yourself, and never refresh a credential outside
  fir. An out-of-tree writer racing fir over a rotating credential can replay
  an already-consumed refresh token and get the whole token family revoked.

> **Testing note: copying the agent dir does not isolate a `--force` refresh.**
> `FIR_AGENT_DIR=/tmp/copy fir auth refresh --force` duplicates the *file*, but
> the refresh token inside it is the same credential upstream — the grant rotates
> the real credential at the provider and revokes the access token every live fir
> session holds. Use a copied agent dir only for paths that spend no grant; a real
> `--force` test needs a **separate login**.

fir's side of that contract: any process resolving a key re-reads `auth.json`
when it has changed on disk, and the request path re-reads unconditionally
before reporting an auth failure, so a session that was live across someone
else's rotation picks up the new token instead of wedging on the revoked one.

#### `auth/api_key`

Extract the API key string from credentials. Most providers just return
`credentials.access`, but some may derive the key differently.

**Params:**
```json
{
  "provider_id": "my-corp",
  "credentials": { "access": "...", "refresh": "...", "expires": 0 }
}
```

**Result:**
```json
{
  "api_key": "sk-..."
}
```

#### `auth/modify_models`

Optionally adjust model definitions (e.g., set a custom base URL). Return `null`
or omit `models` to indicate no changes.

**Params:**
```json
{
  "provider_id": "my-corp",
  "credentials": { "...": "..." },
  "models": [ { "id": "claude-sonnet-4-20250514", "...": "..." } ]
}
```

**Result:**
```json
{
  "models": null
}
```

### Helper RPCs (extension → fir)

These are JSON-RPC requests the extension sends **to fir** during the login flow.
Fir handles them and returns results.

#### `auth/generate_pkce`

Generate a PKCE code verifier and challenge.

**Params:** `{}` (empty)

**Result:**
```json
{
  "verifier": "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk",
  "challenge": "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
}
```

#### `auth/start_callback_server`

Start a local HTTP server to receive the OAuth callback.

**Params:**
```json
{
  "addr": "127.0.0.1:53692",
  "path": "/callback"
}
```

`addr` may use port `0` to let the OS pick a free port.

**Result:**
```json
{
  "addr": "127.0.0.1:53692",
  "redirect_uri": "http://localhost:53692/callback"
}
```

Returns an error if the port is unavailable.

#### `auth/await_callback`

Block until the callback server receives a request, or until the login is
cancelled. Must be called after `auth/start_callback_server`.

**Params:** `{}`

**Result:**
```json
{
  "code": "auth-code-from-provider",
  "state": "state-value"
}
```

Returns an error if the server was not started or if cancelled.

#### `auth/stop_callback_server`

Stop the callback server. Called after the code is received or on error. Also
called automatically by fir if the extension process exits.

**Params:** `{}`
**Result:** `{}`

### UI Notifications / Requests (extension → fir)

These let the extension drive user interaction through fir's TUI.

#### `auth/open_url` (notification)

Ask fir to open a URL in the user's browser and/or display it.

```json
{
  "jsonrpc": "2.0",
  "method": "auth/open_url",
  "params": {
    "url": "https://my-corp.com/oauth/authorize?...",
    "short_url": "https://tinyurl.com/example?state=...",
    "instructions": "Complete login in your browser."
  }
}
```

`short_url` is optional — a pre-shortened form of `url` (e.g. via a public
URL shortener that forwards click-time query params). When present, fir
typically shows it prominently with `url` as a fallback line, and opens
`short_url` in the browser. Omit or pass `""` when no short form exists.

#### `auth/progress` (notification)

Display a progress/status message in the UI.

```json
{
  "jsonrpc": "2.0",
  "method": "auth/progress",
  "params": {
    "message": "Exchanging authorization code for tokens..."
  }
}
```

#### `auth/prompt` (request — expects response)

Ask the user for text input. Fir renders a prompt in the TUI and returns the
user's response.

```json
{
  "jsonrpc": "2.0",
  "id": 42,
  "method": "auth/prompt",
  "params": {
    "message": "Paste the authorization code:",
    "placeholder": "https://my-corp.com/callback?code=...",
    "allow_empty": false
  }
}
```

**Response from fir:**
```json
{
  "jsonrpc": "2.0",
  "id": 42,
  "result": {
    "value": "user-typed-value"
  }
}
```

## Go Implementation

### Data Structures

In `pkg/extension/capability.go`:

```go
type AuthProviderSpec struct {
    ID                 string `json:"id"`
    Name               string `json:"name"`
    UsesCallbackServer bool   `json:"uses_callback_server"`
}

type InitResult struct {
    Name          string             `json:"name"`
    Tools         []ToolSpec         `json:"tools,omitempty"`
    Commands      []CommandSpec      `json:"commands,omitempty"`
    Events        []string           `json:"events,omitempty"`
    AuthProviders []AuthProviderSpec `json:"auth_providers,omitempty"`
}
```

### Adapter: Bridge → oauth.Provider

In `pkg/extension/bridge_auth.go` (new file), implement an adapter that wraps
a Bridge + AuthProviderSpec into an `oauth.Provider`:

```go
type extAuthProvider struct {
    spec   AuthProviderSpec
    bridge *Bridge
}

func (p *extAuthProvider) ID() string   { return p.spec.ID }
func (p *extAuthProvider) Name() string { return p.spec.Name }
func (p *extAuthProvider) UsesCallbackServer() bool { return p.spec.UsesCallbackServer }

func (p *extAuthProvider) Login(callbacks oauth.LoginCallbacks) (*oauth.Credentials, error) {
    // Send auth/login RPC to extension.
    // While waiting for the response, handle incoming auth/open_url,
    // auth/progress, and auth/prompt requests from the extension by
    // dispatching to the LoginCallbacks.
}

func (p *extAuthProvider) RefreshToken(creds *oauth.Credentials) (*oauth.Credentials, error) {
    // Send auth/refresh RPC, return result.
}

func (p *extAuthProvider) GetAPIKey(creds *oauth.Credentials) string {
    // Send auth/api_key RPC, return result.
}

func (p *extAuthProvider) ModifyModels(models []*ai.Model, creds *oauth.Credentials) []*ai.Model {
    // Send auth/modify_models RPC, return result or nil.
}
```

### Registration Lifecycle

When an extension completes its init handshake and declares `auth_providers`:

1. For each `AuthProviderSpec`, create an `extAuthProvider` adapter.
2. Call `ai.RegisterOAuthProvider(adapter)` to register it.
3. The provider now appears in `ai.GetOAuthProviders()` and the OAuth selector UI.

When the extension is unloaded (process exit, `/reload`, shutdown):

1. Call `ai.UnregisterOAuthProvider(spec.ID)` for each provider.
2. The registry entry is removed. There are no Go-side built-in OAuth
   providers, so unregistration cleanly removes the provider.

### Helper RPC Handlers

In the bridge's message loop, handle incoming requests from the extension:

| Method | Handler |
|---|---|
| `auth/generate_pkce` | Call `oauth.GeneratePKCE()`, return verifier+challenge |
| `auth/start_callback_server` | Call `oauth.startOAuthCallbackServer()`, stash server reference |
| `auth/await_callback` | Read from the callback channel, return code+state |
| `auth/stop_callback_server` | Close the stashed server |
| `auth/open_url` | Dispatch to `LoginCallbacks.OnAuth` |
| `auth/progress` | Dispatch to `LoginCallbacks.OnProgress` |
| `auth/prompt` | Dispatch to `LoginCallbacks.OnPrompt`, return value |

The tricky part is that helper RPCs arrive **while fir is waiting** for the
`auth/login` response. The bridge message loop must handle bidirectional traffic
during login: fir sends `auth/login`, then processes incoming requests/notifications
from the extension until the `auth/login` response arrives.

## Python SDK

Add to `fir_ext.py`:

```python
def auth_provider(id: str, name: str, uses_callback_server: bool = True):
    """Decorator to register an auth provider login handler."""
    def decorator(fn):
        _auth_providers.append({
            "id": id, "name": name,
            "uses_callback_server": uses_callback_server,
            "_login_handler": fn,
        })
        return fn
    return decorator

def auth_refresh(provider: str):
    """Decorator to register a token refresh handler for a provider."""
    def decorator(fn):
        _auth_refresh_handlers[provider] = fn
        return fn
    return decorator

def auth_api_key(provider: str):
    """Decorator to register an api_key extractor for a provider."""
    def decorator(fn):
        _auth_api_key_handlers[provider] = fn
        return fn
    return decorator
```

The `ctx` object passed to handlers gets new methods:

```python
class AuthContext:
    def generate_pkce(self) -> dict:
        """Returns {"verifier": "...", "challenge": "..."}"""

    def start_callback_server(self, addr="127.0.0.1:0", path="/callback") -> dict:
        """Returns {"addr": "...", "redirect_uri": "..."}"""

    def await_callback(self) -> dict:
        """Returns {"code": "...", "state": "..."}"""

    def stop_callback_server(self) -> None: ...

    def open_url(self, url: str, short_url: str = "", instructions: str = "") -> None:
        """Ask fir to open a URL / show it to the user.

        short_url is an optional pre-shortened form of url; fir shows it
        prominently with url as a fallback line."""

    def progress(self, message: str) -> None:
        """Show a progress message in the UI."""

    def prompt(self, message: str, placeholder: str = "", allow_empty: bool = False) -> str:
        """Ask the user for text input. Returns the entered value."""
```

### Example: Custom OAuth Provider

```python
#!/usr/bin/env python3
"""My Corp SSO auth provider for fir."""
import fir_ext
import urllib.parse
import requests  # or urllib

CLIENT_ID = "my-client-id"
AUTHORIZE_URL = "https://sso.my-corp.com/oauth/authorize"
TOKEN_URL = "https://sso.my-corp.com/oauth/token"

@fir_ext.auth_provider(id="my-corp", name="My Corp SSO")
def login(params, ctx):
    pkce = ctx.generate_pkce()
    server = ctx.start_callback_server(addr="127.0.0.1:0", path="/callback")

    auth_url = AUTHORIZE_URL + "?" + urllib.parse.urlencode({
        "client_id": CLIENT_ID,
        "response_type": "code",
        "redirect_uri": server["redirect_uri"],
        "scope": "openid profile",
        "code_challenge": pkce["challenge"],
        "code_challenge_method": "S256",
        "state": pkce["verifier"],
    })

    ctx.open_url(auth_url, "", "Complete login in your browser.")
    result = ctx.await_callback()
    ctx.stop_callback_server()

    if result["state"] != pkce["verifier"]:
        raise ValueError("OAuth state mismatch")

    ctx.progress("Exchanging authorization code...")
    resp = requests.post(TOKEN_URL, json={
        "grant_type": "authorization_code",
        "client_id": CLIENT_ID,
        "code": result["code"],
        "redirect_uri": server["redirect_uri"],
        "code_verifier": pkce["verifier"],
    })
    resp.raise_for_status()
    tokens = resp.json()

    import time
    return {
        "access": tokens["access_token"],
        "refresh": tokens["refresh_token"],
        "expires": int(time.time() * 1000) + tokens["expires_in"] * 1000 - 300000,
    }

@fir_ext.auth_refresh(provider="my-corp")
def refresh(params, ctx):
    resp = requests.post(TOKEN_URL, json={
        "grant_type": "refresh_token",
        "client_id": CLIENT_ID,
        "refresh_token": params["credentials"]["refresh"],
    })
    resp.raise_for_status()
    tokens = resp.json()

    import time
    return {
        "access": tokens["access_token"],
        "refresh": tokens["refresh_token"],
        "expires": int(time.time() * 1000) + tokens["expires_in"] * 1000 - 300000,
    }

fir_ext.run()
```

## Frontmatter

Extensions can declare auth providers in comment frontmatter for discovery
without spawning the extension process:

```python
#!/usr/bin/env python3
# fir:name my-corp-sso
# fir:auth_provider my-corp
```

This allows the auth-only manager (used during fir startup before the full
extension manager runs) to know which extensions register auth providers and
spawn only those.

## Validation

- Auth provider IDs must match `[a-z][a-z0-9-]*`.
- Auth provider IDs must not collide with built-in provider IDs (`anthropic`,
  `github-copilot`, `gemini-cli`, `google-antigravity`, `openai-codex`, `poe`,
  `openrouter`) unless the extension is explicitly trusted to override them.
- Frontmatter auth provider declarations are checked against init handshake
  results (same as events/commands).

## Security

- Extensions must be trusted (existing trust model applies).
- Credentials returned by extensions are stored in fir's secure credential
  storage, never exposed to other extensions.
- The callback server binds to `127.0.0.1` only.
- Extensions cannot read credentials for other providers.

## Implementation Plan

### Phase 1: Core Protocol
1. Add `AuthProviderSpec` to `InitResult` in `pkg/extension/capability.go`
2. Create `pkg/extension/bridge_auth.go` with the `extAuthProvider` adapter
3. Add helper RPC handlers to the bridge message loop
4. Wire up registration/unregistration in the extension lifecycle

### Phase 2: Python SDK
5. Add auth decorators and `AuthContext` to `fir_ext.py`
6. Handle `auth/*` RPC dispatch in the SDK's main loop

### Phase 3: UI Integration
7. Ensure external auth providers appear in the OAuth selector
8. Wire up `auth/open_url`, `auth/progress`, `auth/prompt` to TUI components

### Phase 4: Testing
9. Unit tests for the adapter and helper RPCs
10. Integration test with a mock auth provider extension
11. Frontmatter validation tests
