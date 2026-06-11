# Brief: user-level `mcp.d` drop-ins + `reload_mcp` bridge method

You are on branch `feat/mcp-d-reload`, worktree `/Users/kfet/dev/ai/fir-wt-mcp-d-reload`, based on `main`.
When implementation is done and `go build ./...` + `go test ./pkg/mcp/... ./pkg/extension/...` pass,
run the **ship-it** skill to review-and-fix-loop then ff-merge to `main`. Do NOT push to origin.

## Goal (bare minimum — Phase 1 only)

Let AI sessions self-serve MCP servers race-free: each session writes its **own** drop-in JSON file
into a `mcp.d/` directory (atomic temp+rename), then calls a new `reload_mcp` bridge tool that re-reads
config from disk and applies a targeted reload. Collisions and per-server errors are returned **as the
tool result** so the agent can fix and re-reload.

This mirrors the already-merged `reload_extension` work (commit 45336a02) — study it as the template.

## Scope decisions (locked — do not expand)

- **User-level `mcp.d` ONLY**: `~/.config/fir/mcp.d/*.json`. Project-level `.fir/mcp.d` is explicitly
  DEFERRED to a later phase — do not implement it.
- Each `*.json` in `mcp.d/` is a standalone `ConfigFile` (`{"mcpServers": {...}}`), same schema as `mcp.json`.
- Collisions surface **in the `reload_mcp` tool result**, not as silent log lines.
- No streaming guard on the bridge path (called mid-tool by design).
- Reuse `ConfigFile` + `MergeConfigs` verbatim. No schema change.

## Part A — `mcp.d` loading (pkg/mcp/config.go)

Precedence, low → high:
1. `~/.config/fir/mcp.json`           (user base)
2. `~/.config/fir/mcp.d/*.json`       (user drop-ins, **lexically sorted**, last wins on name clash)
3. `<proj>/.fir/mcp.json`             (project base — unchanged, still highest today since proj .d deferred)

Anchors:
- `LoadConfigFile(path)` at config.go:36, `MergeConfigs(base, override)` at :54, `LoadDefaultConfigs(projectDir)`
  at :99, `DefaultConfigPaths` at :70, `defaultConfigDir()` at :82.
- Add `loadConfigDir(dir string) (*ConfigFile, []Collision, error)`: glob `*.json`, sort filenames, `LoadConfigFile`
  each, fold via `MergeConfigs`. Record a `Collision{Server, WonFile, ShadowedFiles}` whenever a server name
  already present is overwritten (within the dir, AND when a dir entry shadows the user `mcp.json` base).
  Missing dir → empty cfg + nil collisions + nil error (mirror `LoadConfigFile`'s missing-file behaviour).
- **Do NOT change the `LoadDefaultConfigs` signature** — it has many callers (factory.go:300, acp.go:444,
  app.go:130). Instead: have `LoadDefaultConfigs` internally call a new
  `LoadDefaultConfigsReport(projectDir) (*ConfigFile, []Collision, error)` and discard the collisions, so
  existing callers are untouched. The reload path (Part B) calls the `...Report` variant to obtain collisions.
- Define `type Collision struct { Server, WonFile string; ShadowedFiles []string }` (JSON-tagged).

## Part B — `reload_mcp` bridge method

Template: the `reload_extension` path. Read these first:
- bridge `case "reload_extension"`: pkg/extension/bridge.go:559
- `BridgeAPI.ReloadExtension`: pkg/extension/api.go:87
- `SessionBridge.ReloadExtension` + `reloadFn`/`SetReloadFn` seam: pkg/extension/session_bridge.go:457-478 (fields :36-37)
- manager wires `SetReloadFn` via type-assertion seam: pkg/extension/manager.go:199-209
- MCP reload engine: `session.ReloadMCP(...)` at pkg/session/factory.go:299; `Manager.Reload` at pkg/mcp/client.go:906
- the `mcpReloadFunc` closure that already owns mgrPtr/sess/cwd: cmd/fir/app.go:348; wired into interactive
  via `MCPReload` opt at app.go:981 and mode.go:166,216. ACP wires restart at acp.go:410.

Implement, mirroring the reload_extension seam exactly:
1. **api.go**: add `ReloadMCP() (ReloadMCPResult, error)` to `BridgeAPI`. Define `ReloadMCPResult` with at minimum
   `Collisions []mcp.Collision` and `Errors []ServerError` (server name + message). If cheap, also include
   `Started/Restarted/Stopped/Unchanged []string` diff arrays — but collisions+errors are the REQUIRED surface;
   diff arrays are optional. (Avoid importing pkg/mcp into pkg/extension if it creates a cycle — if so, define a
   local mirror struct in pkg/extension and convert at the app.go callback boundary.)
2. **session_bridge.go**: add a `reloadMCPFn func() (ReloadMCPResult, error)` field + `reloadMCPMu`,
   `SetReloadMCPFn(fn)`, and `ReloadMCP()` delegating to it (nil fn → error
   "MCP reload is not supported in this session"). Mirror reloadFn at :36-37,457-478.
3. **bridge.go**: add `case "reload_mcp"` → call `api.ReloadMCP()`, marshal the result struct as the RPC result
   (NOT okTrue — the agent needs the collisions/errors payload). No params required.
4. **Wire the callback** where mgrPtr/sess/cwd are in scope:
   - cmd/fir/app.go: build a `reloadMCPFn` that calls `session.ReloadMCP(...)` and constructs `ReloadMCPResult`
     (collisions from `LoadDefaultConfigsReport`, errors from per-server reload failures). Register it on the
     extension SessionBridge via the same `interface{ SetReloadMCPFn(...) }` type-assertion seam used for reload.
     Make sure it is wired in **ACP/print modes too**, not just interactive — acp.go currently stubs SetRestartFn
     at :410; add the SetReloadMCPFn wiring nearby so the forge works headless.
5. **SDK fir_ext.py**: add `def reload_mcp(self)` → `self._call("reload_mcp")` returning the result dict.
   Find the SDK file (likely under sdk/python or similar; grep for `def reload_extension`).

### Surfacing collisions/errors as the tool result
`Manager.Reload` returns `([]agent.AgentTool, error)` — it does NOT give a per-server diff. For Phase 1 you do
NOT need to refactor Reload to emit a diff. Minimum viable: the `reload_mcp` result carries `Collisions`
(from the config load report) and `Errors` (any error returned by `ReloadMCP`/`Reload`, plus any per-server
connect errors you can cheaply read back from the manager). If extracting per-server errors is hard, a single
top-level `error` string in the result is acceptable — but collisions MUST be populated from the `.d` load.

## Tests (required before ship-it)
- pkg/mcp: `loadConfigDir` — sorted merge, last-wins, collision recorded; missing dir → empty; dir shadows base → collision.
- pkg/mcp: `LoadDefaultConfigsReport` folds user mcp.d after user mcp.json; `LoadDefaultConfigs` still returns merged cfg.
- pkg/extension: `reload_mcp` bridge dispatch returns the result struct; nil fn → error.
- Keep existing pkg/mcp/reload_test.go green.

## Deliverables
- Code + tests above, `go build ./...` clean, target tests green.
- CHANGELOG entry (cmd/fir/CHANGELOG.md) describing user `mcp.d` + `reload_mcp`.
- Brief note in docs/extension-protocol.md for the `reload_mcp` method (mirror the reload_extension doc entry).
- Then run **ship-it**. Do not push to origin. Report the merged commit hash.
