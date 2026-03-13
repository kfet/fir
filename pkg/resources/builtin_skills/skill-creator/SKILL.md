---
builtin: true
name: skill-creator
description: Create, write, add, edit, or update a fir skill — author SKILL.md with frontmatter and instructions, then run the skill-updater review before telling the user to /reload.
---

# Skill Creator

Use this skill whenever you are writing a new skill or editing an existing one.

## Location

Skills live under `.fir/skills/<name>/SKILL.md`. The directory name must match the `name` key in frontmatter.

## Format

Every `SKILL.md` must start with YAML frontmatter followed by Markdown instructions for the executing agent:

```yaml
---
name: <skill-name>
description: One-line, keyword-rich trigger statement used by the agent to decide when to load this skill.
---
```

## Steps

1. Create (or open) `.fir/skills/<name>/SKILL.md`.
2. Write the frontmatter with `name` and a keyword-rich `description`.
3. Write clear, agent-facing Markdown instructions in the body — no stubs or TODOs.
4. Read `.fir/skills/skill-updater/SKILL.md` and run that review on the finished file, applying all fixes in place.
5. Tell the user to `/reload`.
