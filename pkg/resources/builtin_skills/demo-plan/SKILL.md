---
name: demo-plan
description: Run a demo plan with simulated work steps to showcase the TUI plan visualization feature.
---

# Demo Plan

Run a simple multi-step plan, sleeping briefly between steps to simulate work. This demonstrates the plan widget, footer progress indicator, and metadata header.

## Steps

1. Create a plan with 5 steps, all pending except the first which is in_progress. Include metadata (see below).
2. For each step in order:
   - Mark the current step as `in_progress` (all previous as `completed`).
   - Run `sleep 2` to simulate work.
   - After the sleep, update the plan to mark that step `completed` and the next one `in_progress`.
   - **Always include the same metadata on every update** (the plan tool replaces the full state each call).
3. After all steps complete, update the plan one final time with everything `completed`.

## Metadata to use

Include this metadata on **every** plan call:

```json
{
  "session": "demo-fleet",
  "attach": "tmux attach -t demo-fleet",
  "worktree": "/tmp/demo-project-wt"
}
```

## Plan entries to use

Use these exact entries:

| # | Content | Priority |
|---|---------|----------|
| 1 | Analyze requirements | high |
| 2 | Set up project structure | medium |
| 3 | Implement core feature | high |
| 4 | Write tests | medium |
| 5 | Update documentation | low |

## Example flow

```
plan: title="Demo Plan", metadata={session, attach, worktree}, step 1 in_progress, rest pending
sleep 2
plan: same title+metadata, step 1 completed, step 2 in_progress, rest pending
sleep 2
plan: same title+metadata, steps 1-2 completed, step 3 in_progress, rest pending
sleep 2
plan: same title+metadata, steps 1-3 completed, step 4 in_progress, rest pending
sleep 2
plan: same title+metadata, steps 1-4 completed, step 5 in_progress
sleep 2
plan: same title+metadata, all completed
```

After the final update, print a short congratulations message.
