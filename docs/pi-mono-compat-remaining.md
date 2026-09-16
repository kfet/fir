# Pi-mono Extension Compat Layer — Remaining Work

_Updated 2026-09-16, after the package-discovery / literal-`apiKey` /
`auth/resolve_endpoint` work landed on top of v1.11.0._

## What Works Today

- **Pi-mono compat shim** (`pi_compat.js`) — maps `ExtensionAPI` to `fir_ext.js`
- **Generic runtime wrapper** (`run.sh`) — auto-detects runtime and pi-mono imports.
  Now also: augments `PATH` with common runtime install locations (`~/.bun/bin`,
  `~/.local/bin`, nvm/fnm/volta/pnpm/deno, `/usr/local/bin`, `/opt/homebrew/bin`)
  so bun/node are found even when fir's spawn `PATH` is minimal; accepts the
  extension directory as `$1`; and detects **both** the `@mariozechner/` and the
  `@earendil-works/` `pi-coding-agent` import scopes as pi-mono.
- **Core install wrapper generation** (`pkg/pkg/jswrapper.go`) — on every install
  path (`fir install` CLI verb **and** the `/install` slash-command), fir scans the
  package for JS/TS entry points and creates a `main → run.sh` symlink next to each
  one. The `main` symlink is an extensionless executable entry point, so package
  discovery picks it up via the same convention used for `.fir/extensions/<name>/main`
  (see "Package discovery honours the `main`/binary convention" below). The install
  extension (`install.py`) is now a thin shell-out to `fir install`, so generation has
  a single source of truth in core.
- **Package discovery honours the `main`/binary convention** — package
  auto-discovery (`pkg/pkg` `autoDiscover`) now collects an extensionless executable
  entry point (`main`, or a file named after its directory) in any package directory,
  and `extension.ConfigsFromFiles` names such a frontmatter-free executable after its
  **parent directory** — exactly like a `.fir/extensions/<name>/main` entry. This is
  what lets installed packages ship **compiled binary** extensions (which cannot carry
  a comment-frontmatter block) as well as the runtime-wrapped JS/TS case. Loose
  `.py`/`.sh` scripts still require frontmatter and are named by filename. A
  `main → run.sh` symlink is kept in step with the SDK at discovery time: one left
  **dangling** (SDK cache dir pruned) *and* one that is merely **stale** (the old cache
  dir survives, so it silently keeps running a superseded `run.sh`/`fir_ext.js`) are
  both re-pointed at the current SDK's `run.sh`. Only symlinks targeting a `run.sh`
  inside fir's SDK cache are touched — a link to a user's own `run.sh` is left alone.
- **Literal `apiKey` on a pi provider** — pi's `config.apiKey` is a literal key
  value, and it now authenticates. `pi_compat` passes it to fir as the provider's
  `api_key` (a new `ProviderSpec` field), which fir resolves **last** — after a
  stored credential, the environment variable and any `models.json` stanza — so a
  keyless-in-practice server (llama.cpp's conventional `"no-key"`) works out of
  the box while remaining overridable. fir previously read `apiKey` as the NAME
  of an environment variable; that reading is preserved by *also* setting
  `env_keys.primary` when the value matches `^[A-Z][A-Z0-9_]+$`. Precedence then
  decides without guessing: an env var of that name wins if set, otherwise the
  string itself is the key. No author is broken either way.
- **`auth/resolve_endpoint` in the Node SDK** — fir asks every extension to
  confirm or correct a selected model's endpoint. The Node SDK had no handler at
  all, so every pi-registered provider produced
  `WARN hook call failed … auth/resolve_endpoint: Unknown auth method`. There is
  now a `fir_ext.authResolveEndpoint(providerId, handler)` registration (mirroring
  the Python SDK's `auth_resolve_endpoint`); an extension with no handler answers
  `null` ("no correction") instead of erroring, and `pi_compat` installs a
  resolver reporting the provider's configured `baseUrl` for any provider that
  has one.
- **Extension naming follows the package directory** — `pi_compat` derives the
  handshake name with `deriveExtensionName()`: a generic entry-point stem
  (`index`/`main`) defers to the containing directory, a specific one (`todos.ts`)
  is used as-is. Without this every conventional `index.ts` pi package announced
  itself as `index`, so fir discovered `pi-llama` and then addressed it as `index`.
- **SDK extraction** — `run.sh`, `pi_compat.js`, `fir_ext.js` all extracted to `~/.cache/fir/sdks/<hash>/node/`
- **Discovery** — extensionless `main`/binary entries and frontmatter-bearing
  `.py`/`.sh` scripts both flow through `ScanPackageResources` →
  `GetPackageExtensionPaths` → `ConfigsFromFiles` into the extension manager.
- **Tested end-to-end** — `fir install git:github.com/huggingface/pi-llama` now
  reports `Discovered: … 1 extension(s)`, `fir packages` shows `EXTENSIONS 1`, and a
  live session spawns the `pi-llama` extension (named after its package directory) and
  completes its handshake (its `llama-cpp` provider registers only once a
  `llama-server` is reachable — with no server it cleanly registers nothing). typebox
  (P0 #2) is auto-installed by bun, so pi-llama runs to completion under the bun
  runtime.


## Remaining Work

### P0 — Required for real-world use

1. **Module resolution for `@mariozechner/pi-coding-agent` / `@earendil-works/pi-coding-agent` runtime imports**
   Type-only imports (`import type { ExtensionAPI }`) are stripped at compile time
   and need no resolution — this is the common case (pi-llama uses it). Extensions
   that import **runtime values** (`import { isToolCallEventType } from "…/pi-coding-agent"`)
   still fail with `MODULE_NOT_FOUND`. The synthetic `node_modules/<scope>/pi-coding-agent`
   + `NODE_PATH` shim recommended below is **not yet implemented** (deferred: marginal
   ROI given bun is the documented runtime and the common case is type-only, and a
   stray `node_modules` risks perturbing bun's resolution of other deps). Revisit if a
   real extension needs it.
   - **Recommendation (unchanged):** create a shim `package.json` + `index.js` re-exporting
     `pi_compat.js`'s exports in a synthetic `node_modules/` and set `NODE_PATH`.

2. **`@sinclair/typebox` / `typebox` dependency** — ✅ **Effectively handled under bun.**
   bun auto-installs bare imports (e.g. `typebox`, `typebox/compile`) into its global
   cache (`~/.bun/install/cache/`) at run time, so pi-llama's schema definitions work
   with no extra step. Under plain `node` (no auto-install) this still requires the
   user to `npm install` the dep or a bundle step — document that bun is the
   recommended runtime.

3. **Frontmatter generation for discovered extensions** — ✅ **Done (via the `main` convention, no synthetic frontmatter).**
   Rather than generate a synthetic frontmatter wrapper, package discovery was taught
   to honour the same extensionless entry-point convention as `.fir/extensions/<name>/`:
   a `main` (or `<dirname>`) executable is an extension named after its directory, with
   frontmatter optional (`pkg/pkg` `autoDiscover` + `extension.ConfigsFromFiles`). The
   install hook (`pkg/pkg/jswrapper.go`, called from `Manager.Install`) creates a plain
   `main → run.sh` symlink, so generation lives in core and every install path produces
   a loadable package. This also unblocks **compiled binary** package extensions, which
   cannot carry a frontmatter block at all. Events are still collected at the handshake,
   not from frontmatter, so no `pi.on(...)` parsing is needed.



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
   `api`-passthrough case (e.g. pi-llama via `openai-completions`), including a
   **literal `apiKey`** (see *Literal `apiKey` on a pi provider*, above). Not covered:
   `oauth`, `streamSimple`,
   `headers`, baseUrl-only overrides (a `baseUrl` *with* models is applied per
   model), and live `unregisterProvider()` — all warn + degrade since providers
   are fixed at the init handshake.

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

16. **Test suite for pi_compat.js** — ✅ **Partially done.**
    `pkg/extension/sdk/node/fir_ext_test.js` (run by `make test-node-sdk`) covers the
    provider-registration surface including both `apiKey` shapes,
    `auth/resolve_endpoint` present/absent/unknown-method, `wrapContext` UI mapping,
    `mapHookResult` block/allow/no-opinion translation, acknowledged-event ordering,
    and `deriveExtensionName`. Still uncovered: **tool registration and execution
    flow**, and the full pi-event-name → fir-event/hook mapping table.

17. **Test suite for run.sh**
    Still none. `run.sh` is exercised only indirectly (as a symlink target in
    `pkg/pkg/jswrapper_test.go` and `pkg/extension/jspackage_discovery_test.go`);
    nothing tests its own logic. Should test:
    - Entry point discovery priority
    - Pi-mono import detection (both `@mariozechner/` and `@earendil-works/` scopes)
    - Runtime fallback chain (bun → node → npx tsx) and the `PATH` augmentation
    - Non-pi-mono JS/TS passthrough

18. ~~**Clean up old SDK cache dirs**~~ — done. `pkg/cache.SweepAged` ages out
    unclaimed `<cache>/fir/sdks/<hash>/` trees (14 days), alongside the same
    pass for builtin skills and extensions.

18b. **Make discovery read-only again — stable `sdks/current` symlink.**
    `autoDiscover` currently *writes* to package directories: it re-points stale
    `main → run.sh` wrapper symlinks (`healRunShSymlink`) so an extension follows
    SDK upgrades. Correct, atomic (temp-link + rename) and guarded to fir's own
    SDK cache, but a scan with a filesystem side effect is a surprising design,
    and it silently does nothing on a read-only package dir. The clean fix is to
    have `sdk.EnsureExtracted()` maintain a stable
    `<cache>/fir/sdks/current → <hash>` symlink and have wrappers point at
    `…/sdks/current/node/run.sh`. Nothing ever goes stale, healing disappears,
    and discovery goes back to being a pure read.

19. **Documentation** — partially done.
    - ✅ `docs/extensions.md` — documents the package `main`/binary convention and
      provider API keys (literal vs env-var-name).
    - ✅ `docs/extension-protocol.md` — documents the `api_key` `ProviderSpec` field
      and its resolution precedence.
    - ❌ Still missing: an end-user "how to install and use a pi-mono extension"
      walkthrough, and a README section on pi-mono compatibility.

20. **`StringEnum` from `@mariozechner/pi-ai`**
    Some extensions import `StringEnum` for Google-compatible enum schemas. Need a shim or note in docs.
