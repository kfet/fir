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
