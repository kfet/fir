---
name: loop
description: Repeatedly execute any user-provided prompt on a fixed interval. Ask the user what to repeat and how often, then keep looping until told to stop.
override: true
---

# Loop Skill

You are a general-purpose looping agent. You execute a user-defined task on repeat, sleeping between cycles.

## Setup (first run only)

If the user has not yet specified what to repeat, ask them:

1. **What should I repeat?** (the task/prompt to run every cycle)
2. **How long should I wait between cycles?** (default: 30 seconds)

Once you have both answers, begin the loop immediately.

## Loop Cycle

Each cycle follows this exact order:

### 0. Print the next reminder command

Before doing anything else, output this as a plain code block so it's visible in the chat even if the session times out:

```
Next reminder command:
sleep <N> && echo "=== LOOP REMINDER === Cycle complete. Re-read this skill file and run the next cycle. Task: <the user's prompt>"
```

### 1. Re-read this skill file

Re-read `this skill file` to keep instructions in context. Long-running agents drift — this is mandatory.

### 2. Note your task and interval

Confirm to yourself:
- **Task:** `<the prompt the user gave you>`
- **Interval:** `<N>` seconds

### 3. Execute the task

Carry out the user's prompt fully. Do whatever it asks — read files, run commands, write output, analyze results. Do not truncate or skip steps.

### 4. Report

Briefly summarize what happened this cycle:
> Cycle N complete: <one-line summary of what you did or found>

### 5. Run the reminder command

```bash
sleep <N> && echo "=== LOOP REMINDER === Cycle complete. Re-read this skill file and run the next cycle. Task: <the user's prompt>"
```

Use a timeout of `<N + 10>` seconds on the bash call. When you see the reminder output, immediately go back to step 0.

## Rules

- **Carry the task verbatim.** Do not paraphrase or simplify the user's prompt between cycles — repeat it exactly as given.
- **Report every cycle.** Always tell the user what happened, even if the answer is "nothing changed".
- **Adjust interval on request.** If the user says "slow down" or "speed up", update the interval for the next cycle immediately.
