# Pi-mono Extension Compat Layer — Remaining Work

_Updated 2025-03-24 after initial implementation and end-to-end testing_

## What Works Today

- **Pi-mono compat shim** (`pi_compat.js`) — maps `ExtensionAPI` to `fir_ext.js`
- **Generic runtime wrapper** (`run.sh`) — auto-detects runtime and pi-mono imports
- **Install post-hook** (`install.py`) — symlinks `main` → `run.sh` for JS/TS packages
- **SDK extraction** — `run.sh`, `pi_compat.js`, `fir_ext.js` all extracted to `~/.cache/fir/sdks/<hash>/node/`
- **Discovery** — reordered candidates in `discovery.go` (`.py` → `.sh` → `.ts` → `.js`)
- **Tested end-to-end** — pi-mono TypeScript extension with `pi.registerTool()` works in a live fir session

## Remaining Work

### P0 — Required for real-world use

1. **Module resolution for `@mariozechner/pi-coding-agent` imports**
   The current test extension uses `import type { ExtensionAPI }` which TypeScript strips at compile time. Real pi-mono extensions that `import { isToolCallEventType }` or other runtime values from `@mariozechner/pi-coding-agent` will fail with `MODULE_NOT_FOUND`. Need either:
   - A `--loader` hook (Node/Bun) that intercepts the import and redirects to `pi_compat.js`
   - A synthetic `node_modules/@mariozechner/pi-coding-agent/` with `package.json` pointing to our shim
   - A bundler step at install time
   
   **Recommendation:** Create a shim `package.json` + `index.js` in a synthetic `node_modules/` dir and set `NODE_PATH` to include it. Install.py can do this at install time.

2. **`@sinclair/typebox` dependency**
   Most pi-mono extensions use `import { Type } from "@sinclair/typebox"` for tool parameter schemas. This is a runtime dependency. Options:
   - Bundle a minimal typebox shim that returns passthrough JSON Schema objects
   - Run `npm install` in the extension directory at install time
   - Document that users need `npm install @sinclair/typebox` in their extension dir

3. **Frontmatter generation for discovered extensions**
   The `run.sh` symlink has no frontmatter, so fir warns about missing event declarations. The install post-hook should either:
   - Parse the extension source for `pi.on("event_name", ...)` calls and generate a frontmatter comment at the top of a wrapper script
   - Or generate a small `.sh` wrapper (not a symlink) with the correct frontmatter that execs `run.sh`

### P1 — Important for compatibility

4. **`ctx.ui.confirm()` / `ctx.ui.select()` / `ctx.ui.input()`**
   Currently stubbed (confirm→true, select/input→undefined). Many popular extensions use these for permission gates and interactive workflows. Needs bridge-side support in fir (`bridge.go` + `bridge_api.go`).

5. **`pi.appendEntry()` / `ctx.sessionManager.getBranch()`**
   Currently `appendEntry` maps to `setSessionData` (key/value) which is a lossy approximation. Stateful extensions (todos, cost trackers) need ordered entry append + branch query. Needs session bridge changes.

6. **`ctx.ui.setWidget()`**
   Mapped to no-op. Extensions that show persistent UI (usage bars, status widgets, progress) are silently broken. Needs TUI widget support in fir.

7. **Hook mapping completeness**
   These pi-mono hooks are mapped in `pi_compat.js` but may not exist in fir's bridge:
   - `hook/tool_result` — verify fir supports this
   - `hook/context` — message filtering before LLM call
   - `hook/input` — input interception
   - `hook/before_provider_request` — provider payload inspection
   - `hook/user_bash` — user shell command interception
   
   Need to audit `bridge.go` `handleInbound` and add missing hooks, or document which are unsupported.

8. **`pi.sendUserMessage()` delivery modes**
   Pi-mono supports `deliverAs: "steer" | "followUp" | "nextTurn"`. Fir's `send_user_message` may not support all three. Verify and document gaps.

### P2 — Nice to have

9. **`pi.registerProvider()` / `pi.unregisterProvider()`** — ✅ **Done.**
   `registerProvider` is mapped to fir's hosted-provider handshake (see
   `docs/pi-mono-compat-layer.md` § Hosted provider registration). Works for the
   `api`-passthrough case (e.g. pi-llama via `openai-completions`). Not covered:
   `oauth`, `streamSimple`, `headers`, literal `apiKey`, baseUrl-only overrides,
   and live `unregisterProvider()` — all warn + degrade since providers are
   fixed at the init handshake.

10. **`pi.registerShortcut()` / `pi.registerFlag()`**
    Keyboard shortcuts and CLI flags. Currently no-ops. TUI-only features.

11. **`pi.events` inter-extension bus**
    Currently in-process only (works within a single extension). Cross-extension communication would need bridge support.

12. **`pi.getThinkingLevel()` / `pi.setThinkingLevel()`**
    Currently stubs. Need bridge methods.

13. **`ctx.compact()` / `ctx.reload()` / `ctx.shutdown()`**
    Session control methods. `shutdown` calls `process.exit(0)` which is wrong (should signal fir). Compact and reload need bridge support.

14. **Custom message rendering (`renderCall` / `renderResult` / `registerMessageRenderer`)**
    TUI rendering customization. No-ops in fir's terminal mode.

15. **`pi.exec()` signal/timeout support**
    Currently passes to `ctx.exec()` but AbortSignal integration is untested.

### P3 — Polish

16. **Test suite for pi_compat.js**
    No unit tests yet. Should test:
    - Event mapping (pi-mono event names → fir events/hooks)
    - Hook result translation (block/allow, tool_result modification)
    - Tool registration and execution flow
    - Context method delegation

17. **Test suite for run.sh**
    No tests. Should test:
    - Entry point discovery priority
    - Pi-mono import detection
    - Runtime fallback chain (bun → node → npx tsx)
    - Non-pi-mono JS/TS passthrough

18. **Clean up old SDK cache dirs**
    `~/.cache/fir/sdks/` accumulates stale directories. Add a cleanup pass that removes dirs older than N days.

19. **Documentation**
    - Add to `docs/extensions.md` — how to install and use pi-mono extensions
    - Update `docs/extension-protocol.md` with JS/TS extension notes
    - README section on pi-mono compatibility

20. **`StringEnum` from `@mariozechner/pi-ai`**
    Some extensions import `StringEnum` for Google-compatible enum schemas. Need a shim or note in docs.
