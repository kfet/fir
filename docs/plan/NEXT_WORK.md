# Available Work Items — Pick One and Go

**Updated:** 2026-02-08 21:46 PST
**Instructions:** Read `08-agent-resume.md` for full workflow. Pick ONE item below, verify it's still unclaimed, port it.

---

## ✅ Completed Phases (DO NOT TOUCH)

- Phase 0: Scaffolding — DONE
- Phase 1: AI Layer — DONE (all 16 items)
- Phase 2: Agent Loop — DONE (all 3 items)
- Phase 3: Tools — DONE (all 9 items)

---

## 🟢 Available — Ready to Claim NOW

### Phase 4: Session Manager

| Priority | Go file + test | TS source | Lines | Notes |
|---|---|---|---|---|
| ⭐ HIGH | `core/session.go` `core/session_test.go` | `coding-agent/src/core/session-manager.ts` | 1401 | Deps: agent/types ✅, ai/types ✅. **Blocks compaction.** |

### Phase 5: Core Infrastructure (deps met)

| Priority | Go file + test | TS source | Lines | Notes |
|---|---|---|---|---|
| ⭐ HIGH | `core/modelregistry.go` `core/modelregistry_test.go` | `coding-agent/src/core/model-registry.ts` | 665 | Deps: ai/types ✅, ai/models ✅, authstorage ✅. **Blocks modelresolver.** |
| ⭐ HIGH | `core/resourceloader.go` `core/resourceloader_test.go` | `coding-agent/src/core/resource-loader.ts` | 871 | Deps: settings ✅, skills ✅, prompttemplates ✅ |
| MEDIUM | `core/bashexec.go` `core/bashexec_test.go` | `coding-agent/src/core/bash-executor.ts` | 278 | Deps: tools/bash ✅ |
| MEDIUM | `core/compaction/utils.go` `core/compaction/utils_test.go` | `coding-agent/src/core/compaction/utils.ts` | 154 | Deps: messages ✅ |

### Phase 5: Blocked (deps not met yet)

| Item | Blocked by |
|---|---|
| `core/modelresolver.go` | needs `modelregistry` |
| `core/compaction/compaction.go` | needs `session` (Phase 4) |
| `core/sdk.go` | needs all above |
| `core/agentsession.go` | needs all above |

---

## How to Claim Work

1. **Check the file doesn't already exist:**
   ```bash
   ls -la pkg/core/modelregistry.go 2>/dev/null
   ```
2. **Check no one is editing nearby files:**
   ```bash
   find pkg/core/ -name "*.go" -mmin -10 2>/dev/null
   ```
3. **Read the TS source** (paths relative to `../pi-mono/packages/`)
4. **Read one existing Go file in the same package** for style consistency
5. **Port, test, verify:**
   ```bash
   go vet ./pkg/core/...
   go test ./pkg/core/...
   ```
6. **Update `07-work-tracker.md`** — re-read it first, then mark `[x]`
