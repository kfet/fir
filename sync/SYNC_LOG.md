# Sync Log

## 2026-02-12 — Sync to commit bd040072

- `ai/types.ts`: Added `metadata` field to StreamOptions (map[string]any)
- `models.generated.ts`: Regenerated from 724 models (added new providers and models)
- `anthropic.ts`: Refactored to simplify code (removed unnecessary tool_result check)
- Various provider fixes and updates
- Extension event forwarding (not yet implemented in Go)

## 2025-02-08 — Initial port from commit 1caadb2e

- Phase 0: Scaffolding (go.mod, Makefile, directory structure)
- Phase 1: AI layer core types, event stream, models, registry, providers/options, providers/transform
