---
name: aside-advisor
description: "[SYS_EXT] When deliberating, stuck, uncertain, considering a change of approach, or about to declare a task done — escalate to a stronger advisor model via the `aside` tool with `escalate=true` for a second opinion before committing."
builtin: true
---

# Using the Advisor (aside with escalate=true)

The `aside` tool with `escalate=true` sends your full conversation history to a stronger advisor model. It takes NO parameters beyond the question to ask — the advisor sees the task, every tool call you've made, and every result you've seen.

## When to call

Call advisor **BEFORE** substantive work — before writing, before committing to an interpretation, before building on an assumption. If the task requires orientation first (finding files, reading source, seeing what's there), do that, then call advisor. Orientation is not substantive work. Writing, editing, and declaring an answer are.

Also call advisor:

- **When you believe the task is complete.** BEFORE this call, make your deliverable durable: write the file, save the result, commit the change. The advisor call takes time; if the session ends during it, a durable result persists and an unwritten one doesn't.
- **When stuck** — errors recurring, approach not converging, results that don't fit.
- **When considering a change of approach.**

On tasks longer than a few steps, call advisor at least once before committing to an approach and once before declaring done. On short reactive tasks where the next action is dictated by tool output you just read, you don't need to keep calling — the advisor adds most of its value on the first call, before the approach crystallises.

## How to treat the advice

Give the advice serious weight. If you follow a step and it fails empirically, or you have primary-source evidence that contradicts a specific claim (the file says X, the test says Y), adapt. A passing self-test is not evidence the advice is wrong — it's evidence your test doesn't check what the advice is checking.

If you've already retrieved data pointing one way and the advisor points another: don't silently switch. Surface the conflict in one more advisor call — *"I found X, you suggest Y, which constraint breaks the tie?"* The advisor saw your evidence but may have underweighted it; a reconcile call is cheaper than committing to the wrong branch.

## Bottleneck check

Before escalating, apply this quick test: is the gap **reasoning** or **information**? If gathering more data would resolve the uncertainty, do that first. If uncertainty persists after you have the facts, it's a reasoning gap — escalate.

## Cost asymmetry

Escalate when the cost of being wrong outweighs the cost of asking. Don't escalate when the decision is easily reversible or the stakes are low.

## Ask for conciseness

When framing the question, instruct the advisor to be brief: *"Respond in under 100 words using enumerated steps, not explanations."* This cuts advisor output by ~40 % without changing call frequency.
