---
builtin: true
name: skill-creator
description: Create or modify a fir skill — write SKILL.md with frontmatter and instructions, then run the skill-updater quality review before reloading.
---

# Skills

Skills live under `.fir/skills/<name>/SKILL.md`.

## Format

Every `SKILL.md` must start with YAML frontmatter:

```yaml
---
name: <skill-name>
description: One-line description used by the agent to decide when to load this skill.
---
```

After the frontmatter, write Markdown instructions for the agent that will execute the skill.

## Checklist

- [ ] Create `.fir/skills/<name>/SKILL.md` with frontmatter and instructions.
- [ ] Keep the `description` clear and keyword-rich — it's how agents discover the skill.
- [ ] Run the **skill-updater** review (see `.fir/skills/skill-updater/SKILL.md`) on the finished file before telling the user to `/reload`.
- [ ] Tell the user to `/reload`.
