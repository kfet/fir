---
builtin: true
name: skill-creator
description: How to create and modify skills. Use this when adding, editing, or documenting a skill.
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
- [ ] After creating or modifying a skill, tell the user to `/reload`.
