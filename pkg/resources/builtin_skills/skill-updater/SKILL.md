---
builtin: true
name: skill-updater
description: Review and improve any SKILL.md after it is created or edited — checks frontmatter trigger quality, keyword coverage, and writing clarity.
---

# Skill Updater

> **Skill files target a smart AI, not a beginner. Skip anything obvious. Every sentence must earn its place.**

Run after every skill create or edit. Fix the file in place, summarise changes in one paragraph, end with "Run `/reload` to pick up the updated skill."

## Frontmatter

`description` is the sole trigger signal. It must be a single tight sentence naming the concrete action and the key nouns a user would say — precise and keyword-rich.

Good:
```yaml
name: releaser
description: Release a new version. Confirms reviews and tests pass, updates VERSION and CHANGELOG.md, commits, tags, and installs.
```

## Body

Cut anything vague, redundant, incomplete, or longer than necessary. Instructions must target the executing agent, not the user.

**No project-root boilerplate.** The shell already starts in the project root. Remove any `git rev-parse --show-toplevel` calls and any `PROJECT_ROOT` / `PROJECT` preambles that exist only to locate the repo. Use `$PWD` or relative paths directly.

## Checklist

- `name` matches the directory name
- `description` is a one-sentence keyword-rich trigger
- No stubs or TODOs
- Agent-facing language throughout
