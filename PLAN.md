# pi-go Planning Documents

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

## Quick Start for Agents

```
Read docs/plan/08-agent-resume.md   # Full workflow: orient → pick → port → test → done
```
