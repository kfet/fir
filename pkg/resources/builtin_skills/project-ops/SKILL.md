---
name: project-ops
builtin: true
description: Long-running, multi-agent project operations — coordinating ongoing work across several agents on the same project. Continuous review/fix cycles against URGENT.md/BACKLOG.md, monitoring file activity and build health, running e2e suites against in-progress work. Not for one-shot tasks; use only when work is ongoing and recurring.
override: true
---

# Project Ops Catalog

This skill is a catalog. It does no work itself — it points to the right sub-document for the kind of long-running, multi-agent project operation you need to perform.

When the user's request matches one of the entries below, **read the listed document and follow its instructions**. Paths are relative to this skill's directory; resolve and load via the Read tool.

## Catalog

| Sub-skill | Description | Document |
|---|---|---|
| **fix** | Continuously pick up issues from the review agent's URGENT.md and BACKLOG.md and fix them. Handles build breaks, security issues, simplification, test gaps, and correctness bugs filed by the reviewer. | `./docs/fix.md` |
| **review** | Continuously review code produced by other agents. Checks staged and recent changes for simplification opportunities, security issues, test gaps, and correctness. Run this when multiple agents are actively working on the project. | `./docs/review.md` |
| **monitor** | Continuously monitor a project's file activity, build health, and work-tracker progress. Loops every 30 seconds, reports changes, and flags stuck or broken states. | `./docs/monitor.md` |
| **e2e** | Run the e2e test suite in tests/e2e/, interpret failures, and file bugs to docs/review/ for the fix agent. | `./docs/e2e.md` |

Pick the single best match, load its document, and proceed as that document instructs.
