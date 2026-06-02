---
name: instruction-tune
description: Process accumulated instruction-feedback into edits — read the global friction log, cluster by target, propose and apply fixes to AGENTS.md and skill files across projects, then archive. Use when asked to tune, review, or correct your instructions.
builtin: true
override: true
---

# instruction-tune

The reader side of the instruction-feedback loop. `ship-it` writes friction
entries passively; this skill turns them into actual edits, on demand. User-
invoked only — never auto-run.

## Steps

1. **Read the log:**
   ```bash
   cat ~/.config/fir/instruction-feedback.jsonl 2>/dev/null
   ```
   If absent or empty, say so and stop.

2. **Cluster by `target`.** Patterns across multiple entries are signal; a lone
   one-off is likely noise — flag it as low-confidence rather than acting
   automatically. `target: "task-prompt"` entries have no file to edit; surface
   them grouped as prompt-writing patterns for the user.

3. **For each cluster with a file target**, locate the file. Entries record an
   absolute `project` path, so the target may live in **another project** — read
   it there. Propose a concrete edit that resolves the friction (tighten an
   ambiguous instruction, remove dead-weight guidance, fix a contradiction).
   Show the user the diff and apply on consent.

4. **Archive processed entries** so they do not resurface: append them to
   `~/.config/fir/instruction-feedback.archive.jsonl` and rewrite the live log
   without them. Preserve any entries you did not process.

## Rules

- **Propose, then apply on consent** — never silently rewrite a user's
  instructions.
- **One-offs are low-confidence.** Prefer clustering; don't over-fit to a single
  report.
- **Cross-project is expected.** Edit the file where it actually lives, not a
  copy in the current project.

## Output

Per cluster: target, number of entries, the friction, the edit made (or
deferred). End with how many entries were archived and how many remain.
