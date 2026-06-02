---
name: ship-it
description: Finish a feature on a worktree branch — loop review-and-fix until clean, ff-merge to main, optionally clean up the worktree, and log any instruction friction. Use when implementation is done and make all passes.
builtin: true
override: true
---

# ship-it

Pure orchestration. No loop logic of your own — `review-and-fix` owns the
iterate-until-clean loop; trust it.

Run only on a worktree feature branch with implementation complete and
`make all` passing. Not on `main`.

## Steps

1. **review-and-fix** — invoke the `review-and-fix` skill. It loops internally
   until a pass finds zero new issues, then commits.
2. **merge-to-main** — invoke the `merge-to-main` skill (squash, rebase, verify
   no main content lost, ff-merge, build).
3. **Cleanup — only if the task is tagged final.** The task text carries the
   signal; do not infer it:
   - `Mode: do.` → stop here, leave the worktree and branch in place (more work
     is coming).
   - `Mode: do, final.` → remove the worktree and branch.
4. **Instruction friction log** — see below.

## Instruction friction log

**Empty by default. Silence is the signal.** Only act if something actively got
in the way *this task*: an AGENTS.md rule that misdirected you, a skill
instruction that was ambiguous and led to a wrong turn, a contradiction between
instructions, an unhelpful or unclear task prompt, or dead-weight guidance that
never applied. No score, no summary, no "here are three thoughts".

If — and only if — there is real friction, append one JSON object per
observation (one line each) to `~/.config/fir/instruction-feedback.jsonl`,
creating the file and `~/.config/fir/` if needed:

```bash
mkdir -p ~/.config/fir
cat >> ~/.config/fir/instruction-feedback.jsonl <<'EOF'
{"ts":"<RFC3339>","project":"<abs project path>","host":"<hostname>","branch":"<branch>","target":"AGENTS.md | skills/<name>/SKILL.md | task-prompt","issue":"<short>","evidence":"<what made the friction visible>"}
EOF
```

`target: "task-prompt"` is for friction in the spawning task text itself (no file
to edit; it aggregates into prompt-writing patterns). These entries are processed
later, deliberately, by the `instruction-tune` skill — do not act on them now.

## Output

Report: review-and-fix summary, merge result + commit, whether the worktree was
cleaned up, and any friction entries logged (or "no friction logged").
