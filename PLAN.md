# tau Planning Documents

Go port of the pi coding agent. Target: Raspberry Pi Zero W/W2.

## Plan Files

1. [Overview](docs/plan/01-overview.md) — goals, build targets, source stats
2. [Architecture](docs/plan/02-architecture.md) — layer diagram, TS→Go translations
3. [Directory Structure](docs/plan/03-directory-structure.md) — full file tree
4. [Phases](docs/plan/04-phases.md) — implementation phases with milestones
5. [Upstream Sync](docs/plan/05-upstream-sync.md) — how to track and merge TS changes
6. [Go Decisions](docs/plan/06-go-decisions.md) — dependencies, image handling, performance
7. **[Work Tracker](docs/plan/07-work-tracker.md)** — per-file checklist with deps and TS sources
8. **[Agent Resume Guide](docs/plan/08-agent-resume.md)** — how any agent picks up work
9. [Teamwork](docs/plan/09-teamwork.md) — how multiple agents avoid stepping on each other
10. [Testing Strategy](docs/plan/10-testing.md) — coverage rules, mock patterns, fixture recording
11. [Modes as Extensions](docs/plan/15-modes-as-extensions.md) — could ACP/RPC/print be extensions? Analysis + recommendation

## Current Work: Phase 14 — ACP Mode

Port the ACP (Agent Client Protocol) mode from `../pi-mono-acp`. This exposes tau as an ACP-compliant
agent over stdio, enabling integration with editors like Zed.

See `docs/plan/07-work-tracker.md` → Phase 14 for the full task breakdown.

**Key facts:**
- ACP is a standalone mode (`pkg/modes/acp/`), NOT an extension — the upstream analysis was for TS runtime extensions, but the same 5 architectural gaps apply to tau's compiled-in Go extensions (see `AGENTS.md`)
- Uses `github.com/coder/acp-go-sdk` for stable types + JSON-RPC transport (saves ~1000 lines). SDK is schema 0.10.7; TS SDK is 0.14.1. Missing unstable types (~10 structs) defined locally.
- TS source: `../pi-mono-acp/packages/coding-agent/src/modes/acp/` (~1,770 lines + 1,248 lines tests)
- Core changes to existing code are minimal (session factory refactoring + ~25 lines in `args.go`, `app.go`)

## Quick Start for Agents

```
Read docs/plan/08-agent-resume.md   # Full workflow: orient → pick → port → test → done
```
