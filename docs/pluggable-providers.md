# Pluggable AI Providers

Design + audit notes from the provider→extension migration (landed in commit 5c45f663).

---

# Pluggable AI Providers — Locked Design (v3)

Supersedes RECAP.md and the design parts of PLAN.md. The audit findings in PLAN.md §1 still stand.

## Goal
A new provider — including the entire Cloud Code Assist family (gemini-cli, antigravity, future variants) and arbitrary third-party gateways — can be added by an extension shipping **data only**. No provider-specific Go code, no provider name as a string literal in core code paths outside the provider's own self-registration site (which, for ext-shipped providers, is the extension itself).

## Mechanism: declarative provider configs interpreted by generic Api adapters

Each Api adapter (one per *wire family*) is a generic interpreter parameterised by a JSON config. The config covers everything that varies per provider on that wire family: URL templates, headers, request envelope, response unwrap, retry policy, auth source binding, reasoning header prefix, system-instruction prefix, and so on. Per-family configs are deliberately distinct; we do not invent a single mega-config covering every wire family.

A `RegisteredProvider` record (Provider-keyed registry, alongside the existing `Api`-keyed `ApiProvider`) bundles per-provider identity (id, displayName, shortName, priority, defaultModelID, keyLink, envKeys, oauthProviderID, claimsModelID, models or live-lister, source `"builtin"` or `"ext:<name>"`) with a reference to the wire-family Api adapter id and the per-family config blob to feed it.

## Substitution grammar (shared across wire families)

Configs are JSON. String values support a tiny substitution language; the interpreter is shared across Api adapters so providers expressing themselves over different wire families use the same vocabulary.

**Variables** — `${path.to.value}` looks up in a per-request context map populated by the adapter:
- `model.id`, `model.base_url`, `model.max_tokens`, `model.reasoning` (booleans serialise to `true`/`false`)
- `creds.access_token`, `creds.api_key`, `creds.project_id`, `creds.<extra>` — derived from the auth source. If the credential delivered to the adapter via `StreamOptions.ApiKey` is a plain string it populates `creds.api_key` and `creds.access_token` (alias). If it is a JSON object (existing convention for OAuth providers that need extras like `projectId`) the keys populate `creds.<key>` snake-cased.
- `env.<NAME>` — process env, only those declared in the provider's `EnvKeys` allowlist (no arbitrary env exfiltration).
- `os` (`darwin`/`linux`/`windows`), `arch` (`amd64`/`arm64`), `version` (fir version).

**Functions** — `${fn.name(arg1,arg2)}` — small, fixed set:
- `fn.rand_id(prefix)` — `prefix-<unix-ms>-<9 lowercase alphanumerics>` (matches today's antigravity/gemini-cli request-id format).
- `fn.unix_millis()` — current Unix milliseconds.

**`"$inner"` sentinel** — the literal string `"$inner"` appearing as a JSON value in `envelope` is replaced by the inner Gemini/OpenAI/Anthropic request *as a JSON value* (not stringified). Used by Cloud Code Assist's wrapper. Outside `envelope` it is plain text.

The substitution function set is deliberately small. New functions are forever-public extension API; we add them only when a concrete provider proves the need.

## Per-family configs

### `google-generative-ai` (covers `google`, `google-vertex`, `google-gemini-cli`, `google-antigravity`, and Cloud Code Assist clones)

```jsonc
{
  "endpoint_urls": ["url-template", ...],     // tried in order
  "headers":       { "Name": "value-template", ... },
  "envelope":      "$inner" | { ...template... },   // identity or wrap object
  "system_instruction_prefix": [{ "role": "...", "text": "..." }, ...],
  "sse_chunk_unwrap": "/json/pointer" | "",   // default identity
  "retry": {
    "base_ms": 1000, "max_attempts": 3, "max_ms": 60000,
    "retry_on_status": [429, 500, 502, 503, 504],
    "retry_on_empty":  { "max_attempts": 2, "base_ms": 500 }
  },
  "reasoning_header_prefix": "x-google-thinking-" | "x-gemini-thinking-",
  "auth": { "oauth_provider_id": "..." | null, "env_key": "GEMINI_API_KEY" | null }
}
```

The three current variants (`google`, `gemini-cli`, `antigravity`) become three config records. `google_gemini_cli.go` is deleted; the unified declarative adapter in `pkg/ai/providers/declgoogle/` (~250 LOC) replaces it. Vertex stays a distinct adapter only because of ADC token plumbing; or it can collapse into the same family with `auth.kind = "adc"` once we choose. Phase 1e ports vertex too only if cheap; otherwise it stays as today.

### Other wire families — same shape, different fields

The OpenAI and Anthropic Api adapters follow the identical pattern: a per-family JSON config with `endpoint_urls`, `headers`, optionally `envelope` (Codex-Responses wraps differently than direct OpenAI; Bedrock-Converse is its own envelope), per-family streaming-decode hints (SSE vs WebSocket), and retry/reasoning knobs. **We do not refactor those families in this PR.** They become straightforward follow-ups once the Gemini family proves the design. The substitution grammar above is intentionally written to be sufficient for them.

## OAuth-extension applicability (general, not Gemini-specific)

The point of putting `auth.oauth_provider_id` in the config rather than wiring auth in core code is that *any* OAuth-backed provider can ship as data:
- **GitHub Copilot** — already has `copilot_auth.py` builtin extension; today its provider record (model list, endpoints, headers, env-keys) is in core. After Phase 2, `copilot_auth.py` grows a `providers=[…]` field referencing its own `auth_providers` entry, and the core record is removed.
- **Anthropic OAuth (Claude.ai login)** — `anthropic_auth.py` similarly.
- **OpenAI Codex OAuth** — `codex_auth.py` similarly.
- **Poe**, **Cerebras Pro**, **Mistral OAuth**, etc. — any future OAuth provider ships as one extension carrying both the OAuth flow (existing mechanism) and the provider config (new mechanism). Zero core changes.

The config design is deliberately not specialised to Gemini — `auth.oauth_provider_id` references the existing oauth registry by id, and `creds.<extra>` lifts any provider-specific OAuth extras (Copilot's GitHub session id, etc.) into the substitution namespace generically. The same `${creds.access_token}` works for every Bearer-token OAuth provider; gemini-cli's `${creds.project_id}` is just one extra among many.

## End-state acceptance criteria

After Phase 1e + Phase 2 + the PoC payoff:

1. `rg "gemini-cli|antigravity|google-gemini-cli|google-antigravity"` returns **zero hits** in core Go code paths (excluding `pkg/ai/providers/declgoogle/` interpreter, generated files, tests, `cmd/generate-models/` data files, and the extensions themselves).
2. `pkg/ai/providers/google_gemini_cli.go` is deleted; the ~12 antigravity-specific switch sites are gone with it.
3. `gemini_cli_auth.py` and `antigravity_auth.py` declare full provider records (config + models + auth) at handshake; removing either extension removes the corresponding provider from the model picker.
4. A new third-party Cloud Code Assist clone can be added by dropping a single Python file in `.fir/extensions/`.
5. The literal-hit baseline of 170 across cross-cutting core sites (PLAN.md §1b) is driven to zero in Phase 1a–1d. Phase 1e + Phase 2 + PoC payoff drive the gemini-cli/antigravity-specific count to zero.

## Plan ordering

Phase 1e (declarative Gemini adapter) lands **first** as a self-contained, fully-tested commit — `google_gemini_cli.go` deleted, three config records still in core, `make all` green. It's the riskiest design call and the most decisive proof-point. Phase 1a–1d (audit-driven, mechanical de-literalisation) follow. Phase 2 wires the extension JSON-RPC payload. PoC payoff (move records out of core into extensions) is the final commit.

---

# Pluggable AI Providers — Audit & Plan

Branch: `work/pluggable-providers`. Scope: an extension alone must be able to add a provider; no provider name should appear in any code path outside its own self-registration site.

## 1. Audit — every hardcoded-provider site

(Scanned the whole repo; results sorted by where they live and what they encode.)

### 1a. Legitimate self-registration sites (keep, but treat as "the provider's own home")
- `pkg/ai/providers/*` — built-in stream/transform/headers code, one file per provider family. Owns its own constants. Good.
- `pkg/ai/oauth/openai_codex.go`, builtin extensions `*_auth.py` — provider-specific OAuth flows. The oauth registry (`pkg/ai/oauth/registry.go`) is already pluggable.
- `cmd/generate-models/main.go` — offline generator. Its `applyOverridesAndAdditions` (incl. `google-antigravity` synthesis) is a build-time data table, not a runtime code path. Acceptable, but extension-backed providers must be able to *replace or supplement* its output at runtime via their own model list.

### 1b. Cross-cutting sites that violate the invariant (must be data-driven)

| File | Symbol | What it encodes | Fix |
|---|---|---|---|
| `pkg/ai/types.go` | `Provider*`/`Api*` constants | Just IDs; no switches. | Keep as docs of well-known IDs. Built-ins move them to providers/ subpkg; nothing else may grow this list. |
| `pkg/ai/envkeys/envkeys.go` | switch on provider→env-var | API-key env var name per provider | Replace with a registry: each provider self-registers an `EnvKeySpec` (primary + fallbacks + special "<authenticated>" providers like bedrock/vertex). |
| `pkg/ai/oauth/registry.go` `builtInProviders` | sync.Map seeded by `init()` of each oauth provider file | Already pluggable — fine. | Keep. Document. |
| `pkg/extension/capability.go` `builtinAuthProviderIDs` | hardcoded set of "reserved" auth provider IDs | Prevents extensions overriding built-ins by accident | Replace with `oauth.IsBuiltInProviderID(id)` derived from the registry's "built-in snapshot" set. |
| `pkg/models/modelresolver.go` `defaultModels` | provider→default model id map | Default model per provider | Move to `Provider.DefaultModelID()` on the registered provider record (in pkg/ai). Resolver iterates the registry. |
| `pkg/models/modelresolver.go` `knownProviderOrder` | display/preference order | UX preference order | Move to `Provider.Priority` (int) in the registered provider record. Sort by priority then ID. |
| `pkg/models/modelresolver.go` bedrock-pass-through and poe special cases | `if provider == "amazon-bedrock"` etc. | ID-shape claiming | Add `Provider.ClaimsModelID(id) bool` and `Provider.ResolveCustomID(id) *Model` hooks; resolver calls these instead of switching. Used by `cmd/fir/app.go` ARN auto-detect too. |
| `pkg/models/modellister.go` `GetModelLister` switch | provider→remote-listing strategy | Live `/v1/models` calls | Each built-in lister registers itself in a `modellister.Register(id, lister)` map. `openRouterVendor` etc. moves into the openrouter lister. |
| `pkg/modes/acp/auth.go` `providerKeyLinks`, `displayNames` | maps for UI | Per-provider UX strings | Move to `Provider.KeyLink()`, `Provider.DisplayName()` on the registered record. ACP iterates the registry. |
| `pkg/modes/acp/modelstate.go` short-name map | abbreviation for status bar | UX | Move to `Provider.ShortName()`; default = first 4 chars of ID. |
| `cmd/fir/app.go` Bedrock ARN auto-detect | `args.Provider = "amazon-bedrock"` based on ID prefix | Provider claim from ID | Use the new `Registry.ClaimProviderForModelID(id)` (delegates to per-provider `ClaimsModelID`). |
| `cmd/fir/app.go` Bedrock late-registration | clones a bedrock model | Convenience for ARN models | Becomes the bedrock provider's `ResolveCustomID` hook. |

### 1c. The "Provider record" in core (new)

A single `ai.RegisteredProvider` struct will carry everything cross-cutting code currently switches on:

```go
type RegisteredProvider struct {
    ID               Provider
    DisplayName      string
    ShortName        string         // status-bar abbreviation
    Priority         int            // smaller = preferred in defaulting
    DefaultModelID   string
    KeyLink          string         // URL where users get an API key
    EnvKeys          EnvKeySpec     // {Primary, Fallbacks, Authenticated bool}
    OAuthProviderID  string         // empty if pure API-key
    ClaimsModelID    func(id string) bool                 // optional
    ResolveCustomID  func(id string) *Model               // optional
    Lister           ModelLister                          // optional
    Stream           StreamFunction                       // built-ins; nil for ext-backed
    StreamSimple     SimpleStreamFunction                 // optional
    Source           string         // "builtin" or "ext:<extName>"
}
```

Existing `ApiProvider`/`Api` indirection stays — `Api` is the wire protocol, `Provider` is the hosted service. The new record adds a `Provider`-keyed registry alongside the existing `Api`-keyed one, and the `Provider` record references which `Api` it speaks.

## 2. Extension contract for ext-backed providers

### 2a. Init payload
Adds `providers` to `InitResult`:
```python
ProviderSpec {
    id: str
    api: str                  # one of the wire protocols, OR "ext-stream" (see 2c)
    display_name: str
    short_name: str | None
    priority: int = 1000
    default_model_id: str | None
    key_link: str | None
    env_keys: {primary: str, fallbacks: [str], authenticated: bool}
    oauth_provider_id: str | None    # cross-references InitResult.auth_providers
    models: [Model]                  # static catalogue
    claims_model_id_globs: [str]     # optional ID-shape claim patterns (e.g. "arn:aws:bedrock:*")
    supports_live_listing: bool
}
```
Built-in providers will be re-expressed as a Go-side equivalent of this (`RegisteredProvider`); no behavioural change at runtime, but the *shape* of the data is now identical for ext and built-in.

### 2b. Methods called *to* the extension
- `provider/listModels` `{providerId, credentials?}` → `Model[]`
- `provider/resolveCustomId` `{providerId, modelId}` → `Model | null` (only if `claims_model_id_globs` matched)
- `provider/stream/start` `{providerId, streamId, model, request}` → ack `{}`
- `provider/stream/toolResult` `{streamId, toolUseId, content}` → ack
- `provider/stream/cancel` `{streamId}` → ack

### 2c. Events from extension to host (existing notif channel)
- `provider.stream.event` `{streamId, kind, ...}` where kind ∈ `text` / `thinking` / `tool_call` / `usage` / `provider_response` / `done` / `error`

For built-in providers nothing changes — they keep their in-process `StreamFunction`. Ext-backed providers get an in-core `extStreamAdapter` that turns these JSON-RPC events back into the same `StreamEvent` type built-ins emit, so the rest of the agent layer is unaware.

### 2d. Documentation & SDK
- Typed Go structs in `pkg/extension/types.go` (capability) and the bridge.
- Python SDK shapes in `pkg/extension/sdk/python/fir_ext.py`: `ProviderSpec`, `Model`, decorators `@provider_list_models`, `@provider_stream`, `@provider_resolve_custom_id`. Writes the streaming yield pattern (sync generator → events) and exposes a `tool_result(stream_id, tool_use_id, content)` callback.
- `docs/extension-protocol.md` updated with the new methods + payload schemas.

## 3. Refactor plan (phases — landed as separate commits in this branch)

**Phase A — De-literalise core. No new wire protocol.**
- Add `RegisteredProvider` + `pkg/ai.ProviderRegistry` (Provider-keyed) alongside today's `Api`-keyed registry.
- Each built-in provider self-registers a `RegisteredProvider` from its existing `register_*.go` file (already the right home).
- Replace every map/switch in 1b with iteration over the registry.
- Replace `pkg/ai/envkeys` switch with registry lookup.
- Replace `pkg/extension/capability.go` reserved-IDs set with `oauth.IsBuiltInProviderID`.
- Tests: assert that `rg "anthropic|openai|google|..."` returns zero hits outside `pkg/ai/providers/`, `pkg/ai/oauth/`, `pkg/resources/builtin_extensions/`, `cmd/generate-models/`, generated files, and `*_test.go`. Check this in CI via a `make audit-providers` target.

**Phase B — Wire the extension contract.**
- Add `provider/*` methods to `pkg/extension/bridge.go`, and `ProviderSpec` to `InitResult`.
- Add the `extStreamAdapter` in `pkg/ai/providers/extprovider/` (its own subpackage; it's an in-core adapter, not a built-in provider) that satisfies `StreamFunction` by talking to the extension manager.
- Register ext-backed providers into `RegisteredProvider` registry from the extension manager after handshake; unregister on shutdown.

**Phase C — Python SDK + docs + demo.**
- `fir_ext.py` decorators and helpers; types as `dataclasses` (3.9-compatible, no `from __future__ import annotations` tricks needed).
- Update `docs/extension-protocol.md`.
- Update `demo.py` to register a tiny "echo" provider (no real network, just emits a hello world response token-by-token) and add a passing test in `pkg/extension/sdk/python/demo_ext_test.py`.
- Update `pkg/resources/builtin_extensions/` if any built-in extension wants to migrate (none required for landing).

**Phase D — PoC: third-party provider.**
- New sample extension `pkg/extension/sdk/python/examples/sample_provider.py` (or a dedicated repo example under `docs/examples/`) that adds a fictional provider via OpenRouter-compatible HTTP. Uses real env-key handling.
- Integration test: `pkg/extension/integration/provider_test.go` boots fir with the sample extension, lists models, runs a stub streamed completion against an `httptest.Server`, asserts events flow.

**Phase E — Polish.**
- Update `.fir/skills/self/SKILL.md` to document the new "providers via extensions" capability.
- Update `CHANGELOG.md` `[Unreleased] / Added`.
- Confirm `make all` clean. Advisor review. Ff-merge.

## 4. Scope honesty

This is a multi-thousand-LOC refactor touching ~12 packages. I will not try to land it in one shot. Phase A is the prerequisite — it's the part that makes core actually data-driven; without it Phases B–E can't be clean. I'll land Phase A as a self-contained, fully-tested commit first, then reassess. If Phase A reveals nasty surprises (the most likely is bedrock's ARN-claim entanglement with `cmd/fir/app.go` model registration timing), the plan adjusts.

## 5. Risks
- **Streaming over JSON-RPC**: latency and head-of-line blocking. Mitigation: events are notifs (fire-and-forget); each stream gets its own ID; tool-result ingress is a separate request.
- **`models_generated.go`**: 14k lines, regenerated by `cmd/generate-models`. Ext-backed providers must contribute their models *at runtime* and never round-trip through this generator.
- **OAuth refresh races**: keep oauth/registry as today; ext-backed auth providers already use the existing extension `auth_providers` mechanism.
- **Backwards compat**: every existing CLI flag, model ID, env var must keep working. The Phase A tests pin this.
