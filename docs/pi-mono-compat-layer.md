# Pi-mono Extension Compatibility Layer — Design & Implementation

## Overview

Fir can now run pi-mono (JS/TS) extensions without changes to the core extension framework. The solution has three parts:

### 1. `pkg/extension/sdk/node/pi_compat.js` — Compatibility Shim

Maps pi-mono's `ExtensionAPI` surface to fir's `fir_ext.js` SDK:

| Pi-mono API | Mapped to |
|---|---|
| `pi.on("session_start", handler)` | `fir.on("session_start", ...)` |
| `pi.on("tool_call", handler)` | `fir.on("hook/tool_call", ...)` |
| `pi.registerTool({ name, execute })` | `fir.tool({ name }, handler)` |
| `pi.registerCommand("name", { handler })` | `fir.command("name", desc, handler)` |
| `pi.sendMessage(msg, opts)` | `ctx.sendMessage(...)` |
| `pi.sendUserMessage(content, opts)` | `ctx.sendUserMessage(...)` |
| `pi.getActiveTools()` / `setActiveTools()` | `ctx.getActiveTools()` / `setActiveTools()` |
| `pi.setModel(model)` | `ctx.setModel(provider, id)` |
| `pi.exec(cmd, args)` | `ctx.exec(cmd, args)` |
| `ctx.ui.notify(msg, level)` | `ctx.notify(msg, level)` |
| `ctx.ui.setStatus(id, text)` | `ctx.setStatus(text)` |
| `ctx.ui.confirm/select/input` | Stubs (confirm→true, select/input→undefined) |
| `ctx.ui.setWidget/custom` | No-ops |
| `pi.events.on/emit` | Local in-process event bus |
| `pi.registerProvider(name, config)` | `fir.registerProvider(spec)` — declared at the init handshake |
| `pi.unregisterProvider(name)` | No-op (providers are fixed at handshake) |
| `pi.registerShortcut/registerFlag` | No-ops |

Run standalone: `node pi_compat.js <extension-path>`

### 2. `pkg/extension/sdk/node/run.sh` — Generic Runtime Wrapper

Shipped alongside the SDK (embedded, extracted to `~/.cache/fir/sdks/<hash>/node/`). Symlinked into extension directories as `main` (extensionless).

**Entry point discovery:** `index.ts` → `index.js` → `main.ts` → `main.js` → `<dirname>.ts` → `<dirname>.js` → first `*.ts` → first `*.js`

**Runtime detection:**
- `.ts`: bun → npx tsx → node --experimental-strip-types
- `.js`: bun → node
- `.py`: python3 → python

**Pi-mono auto-detection:** Greps for `@mariozechner/pi-coding-agent` imports and routes through `pi_compat.js` if found.

### 3. `install.py` — Post-Install Hook

After `fir install` clones a package, scans for JS/TS files without shebangs and creates `main` symlinks pointing to `run.sh`. The symlink is extensionless so it takes first priority in `findSubdirEntryPoint()`.

### 4. `discovery.go` — Candidate Reorder (already applied)

```
main, main.py, main.sh, main.ts, main.js
<dirname>, <dirname>.py, <dirname>.sh, <dirname>.ts, <dirname>.js
```

Prefers directly-executable formats (`.py`, `.sh`) over runtime-dependent ones (`.ts`, `.js`). TypeScript before JavaScript (typed > untyped).

## File Inventory

| File | Status |
|---|---|
| `pkg/extension/sdk/node/pi_compat.js` | **New** — pi-mono compat shim |
| `pkg/extension/sdk/node/run.sh` | **New** — generic runtime wrapper |
| `pkg/resources/builtin_extensions/install.py` | **Modified** — post-install hook |
| `pkg/extension/discovery.go` | **Modified** — candidate reorder |

## Unsupported Pi-mono APIs

These require fir-side changes and are out of scope for this layer:

- `ctx.ui.setWidget()` — needs TUI widget support
- `ctx.ui.confirm/select/input()` — needs interactive dialog support  
- `ctx.sessionManager.getBranch()` — needs session state access over RPC
- `pi.appendEntry()` — mapped to session data (key/value) as best-effort
- `ctx.ui.custom()` — needs custom TUI component support
- `pi.registerShortcut()` — needs keybinding support

## Hosted provider registration

`pi.registerProvider(name, config)` is supported via fir's hosted-provider
handshake. The shim maps the pi-mono `ProviderConfig` to a `fir.registerProvider`
spec (`name`→`display_name`, `api`, `apiKey`→`env_keys.primary`, `baseUrl`→each
model's `base_url`, and `models`). fir declares providers at the init handshake;
because the shim awaits the extension factory before `fir.run()`, factory-time
registration — including after async model discovery (as pi-llama does) — is
captured. This makes a llama.cpp-style provider work end-to-end through
`api: "openai-completions"` passthrough (fir streams natively).

Constraints (warned + dropped): `oauth` (use fir's auth-provider API),
`streamSimple` (custom JS streaming isn't bridged — use `api` passthrough),
`headers`/`authHeader` (no provider-wire equivalent), and baseUrl-only overrides
of built-in providers. `apiKey` must be an environment-variable *name* (fir's
wire can't carry a literal secret, and the extension is a separate process from
fir). `unregisterProvider()` and post-init `registerProvider()` calls are no-ops,
since providers are fixed at the handshake.
