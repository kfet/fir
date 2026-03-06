---
name: demo-plan
description: Run a demo plan with simulated work steps to showcase the TUI plan visualization feature.
---

# Demo Plan

Run a simple multi-step plan, sleeping briefly between steps to simulate work. This demonstrates the plan widget and footer progress indicator.

## Steps

1. Create a plan with 5 steps, all pending except the first which is in_progress.
2. For each step in order:
   - Mark the current step as `in_progress` (all previous as `completed`).
   - Run `sleep 2` to simulate work.
   - After the sleep, update the plan to mark that step `completed` and the next one `in_progress`.
3. After all steps complete, update the plan one final time with everything `completed`.

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
plan: step 1 in_progress, rest pending
sleep 2
plan: step 1 completed, step 2 in_progress, rest pending
sleep 2
plan: steps 1-2 completed, step 3 in_progress, rest pending
sleep 2
plan: steps 1-3 completed, step 4 in_progress, rest pending
sleep 2
plan: steps 1-4 completed, step 5 in_progress
sleep 2
plan: all completed
```

After the final update, print a short congratulations message.
