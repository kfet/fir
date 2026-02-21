---
name: loop
description: Repeatedly execute any user-provided prompt on a fixed interval. Ask the user what to repeat and how often, then keep looping until told to stop.
---

# Loop Skill

You are a general-purpose looping agent. You execute a user-defined task on repeat, sleeping between cycles.

## Setup (first run only)

If the user has not yet specified what to repeat, ask them:

1. **What should I repeat?** (the task/prompt to run every cycle)
2. **How long should I wait between cycles?** (default: 30 seconds)

Once you have both answers, begin the loop immediately.

## Loop Cycle

Repeat this sequence forever until the user tells you to stop:

### 1. Note your task and interval

At the start of every cycle, confirm to yourself:
- **Task:** `<the prompt the user gave you>`
- **Interval:** `<N>` seconds

### 2. Execute the task

Carry out the user's prompt fully. Do whatever it asks — read files, run commands, write output, analyze results. Do not truncate or skip steps.

### 3. Report

Briefly summarize what happened this cycle:
> Cycle N complete: <one-line summary of what you did or found>

### 4. Sleep and self-remind

Run a sleep command that echoes a reminder when it finishes. This is the mechanism that keeps the loop alive:

```bash
sleep <N> && echo "=== LOOP REMINDER === Cycle complete. Re-read .fir/skills/loop/SKILL.md and run the next cycle. Task: <the user's prompt>"
```

Use a timeout of `<N + 10>` seconds on the bash call.

### 5. Re-read this skill file and repeat

When you see the reminder output, **immediately**:
1. Re-read `.fir/skills/loop/SKILL.md`
2. Go back to step 1 of this cycle

This re-read is mandatory — it prevents instruction drift over long-running sessions.

## Rules

- **Never stop on your own.** Keep looping until the user explicitly says to stop.
- **One cycle at a time.** Complete each cycle fully before starting the next.
- **Carry the task verbatim.** Do not paraphrase or simplify the user's prompt between cycles — repeat it exactly as given.
- **Report every cycle.** Always tell the user what happened, even if the answer is "nothing changed".
- **Adjust interval on request.** If the user says "slow down" or "speed up", update the interval for the next cycle immediately.
