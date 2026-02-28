# Fleet: fir-statusline

- **Session:** fir-statusline
- **Project:** /Users/kfet/dev/ai/fir
- **Worktree:** /Users/kfet/dev/ai/fir-wt-statusline
- **Branch:** fleet/fir-statusline

## Goal

Separate the status line UI into two distinct elements:
1. **Command status** — transient messages from slash commands (e.g. `/queue` output, "Model: ...", warnings)
2. **Activity indicator** — agent work spinners ("Working...", "Compacting...")

Currently both share a single `statusContainer`, so one overwrites the other.

## Agents

| Window     | Role       | Current Task | Status |
|------------|------------|-------------|--------|
| researcher | researcher | Analyze codebase and produce plan | active |
