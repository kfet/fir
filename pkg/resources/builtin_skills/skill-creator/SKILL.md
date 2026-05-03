---
builtin: true
name: skill-creator
description: Create, write, add, edit, update, or review a fir skill — author SKILL.md with frontmatter and agent-facing instructions, run the quality checklist, and tell the user to /reload.
---

# Skill Creator

Use this skill whenever you are writing a new skill, editing an existing one, or just reviewing a SKILL.md for quality.

> **Skill files target a smart AI, not a beginner. Skip anything obvious. Every sentence must earn its place.**

## Location

Skills live under `.fir/skills/<name>/SKILL.md`. The directory name must match the `name` key in frontmatter.

## Format

Every `SKILL.md` starts with YAML frontmatter followed by Markdown instructions for the executing agent:

```yaml
---
name: <skill-name>
description: One-line, keyword-rich trigger statement used by the agent to decide when to load this skill.
---
```

## Steps

1. Create or open `.fir/skills/<name>/SKILL.md`.
2. Write the frontmatter — `name` matching the directory, plus a keyword-rich `description`.
3. Write clear, agent-facing Markdown instructions — no stubs, no TODOs, no user-facing fluff.
4. Run the review checklist below and fix issues in place.
5. Tell the user to `/reload`.

## Catalog pattern (umbrella skills)

When several skills share the same trigger (e.g. four loop-style agents all about "watch the project"), collapse them into a single **catalog skill** to cut system-prompt bloat:

- One real `SKILL.md` at `<name>/SKILL.md` with a narrow keyword-rich `description` and a body that lists each child role and when to use it.
- Children live as plain markdown under `<name>/docs/<role>.md` — **no frontmatter, not named `SKILL.md`** so the discovery walker silently skips them.
- The catalog directs the agent to `Read` the relevant sub-doc on demand. Supporting scripts live under `<name>/scripts/` at the skill root (not under `docs/`), so they're easy to find and reference from any sub-doc.

See `project-ops` for a working example.

## Review checklist

Apply this on every create or edit (including review-only passes on hand-edited files).

### Frontmatter

`description` is the sole trigger signal. It must be a single tight sentence naming the concrete action and the key nouns a user would say — precise and keyword-rich.

Good:
```yaml
name: releaser
description: Release a new version. Confirms reviews and tests pass, updates VERSION and CHANGELOG.md, commits, tags, and installs.
```

### Body

Cut anything vague, redundant, incomplete, or longer than necessary. Instructions must target the executing agent, not the user.

**No project-root boilerplate.** The shell already starts in the project root. Remove any `git rev-parse --show-toplevel` calls and any `PROJECT_ROOT` / `PROJECT` preambles that exist only to locate the repo. Use `$PWD` or relative paths directly.

### Checklist

- `name` matches the directory name
- `description` is a one-sentence keyword-rich trigger
- No stubs or TODOs
- Agent-facing language throughout
