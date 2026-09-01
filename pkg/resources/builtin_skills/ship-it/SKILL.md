---
name: ship-it
description: Finish a feature on a worktree branch — loop review-and-fix until clean, ff-merge to main, cut and publish a release, clean up the worktree, and log any instruction friction. Use when implementation is done and make all passes.
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
3. **release** — in the main worktree (the branch is already merged there),
   invoke the `release` skill: it determines the version from the
   `## [Unreleased]` entries in `CHANGELOG.md`, runs `make all`, updates
   `CHANGELOG.md` and `VERSION`, commits, tags, and installs. Invoking `ship-it`
   *is* the confirmation that skill asks for — do not stop to ask again.
4. **deploy** — complete the `release` skill's Publishing step: `make publish`
   (pushes the commit and tag; GoReleaser CI builds and uploads the release),
   followed by its post-publish workflow monitoring. Rolling the new version out
   to hosts is then `fir update` **run on each host**, once CI is green — never
   an scp/cp of a build artifact onto a host's `fir`. `make deploy HOST=<host>`
   is the exception, not the rollout: use it only when the release has no
   artifact for that platform, or for deliberate pre-release testing, say so out
   loud, and clean up the `~/.local/bin/fir.prev` file it leaves behind once the
   new binary is verified.
5. **Cleanup** — remove the worktree and branch.
6. **Instruction friction log** — see below.

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

Report: review-and-fix summary, merge result + commit, release version + tag,
deploy result (published release or host deployed to), confirmation the worktree
was removed, and any friction entries logged (or "no friction logged").
