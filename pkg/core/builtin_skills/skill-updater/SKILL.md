---
builtin: true
name: skill-updater
description: Review and improve any SKILL.md after it is created or edited — checks frontmatter trigger quality, keyword coverage, and writing clarity.
---

# Skill Updater

Run this review at the end of every skill create or update. Read the full `SKILL.md` and fix any issues before telling the user to `/reload`.

## 1. Frontmatter Quality

Read the `description` field. It is the sole signal used to decide whether to load this skill, so it must be precise and keyword-rich.

Ask yourself:
- Does it name the concrete action or situation that should trigger it? (e.g. "creating a skill", not just "skills")
- Does it mention the key nouns a user would say when they need this? (e.g. "release", "version", "publish")
- Is it one tight sentence — not vague ("helper for X"), not a list?
- Would an agent reading dozens of descriptions pick this one when appropriate?

If the answer to any is no, rewrite the `description` so it passes all four.

**Good examples:**
```yaml
description: Release a new version. Confirms reviews and tests pass, updates VERSION and CHANGELOG.md, commits, tags, and installs.
description: Sync a downstream port with upstream source changes. Detects changed files, applies equivalent changes, and updates the baseline.
```

**Bad examples:**
```yaml
description: Helper skill for doing things with skills.
description: Use this skill.
```

## 2. Writing Clarity

Read every section of the skill body. Fix anything that is:

- **Vague** — replace "handle appropriately" with the exact action to take.
- **Redundant** — remove repeated instructions already stated elsewhere in the file.
- **Incomplete** — stubs, TODOs, or "see X" with no link or content.
- **Overly long** — if a section can say the same thing in half the words, cut it.
- **Missing structure** — add headers, bullet lists, or code blocks where they'd make scanning faster.

## 3. Checklist

- [ ] `name` matches the directory name.
- [ ] `description` is a one-sentence, keyword-rich trigger statement.
- [ ] All sections are complete — no stubs or TODOs.
- [ ] Instructions are written for the *executing agent*, not the user.
- [ ] Examples use fenced code blocks with the correct language tag.
- [ ] File ends with a newline.

## 4. Apply and Report

Make all fixes directly to the file. Then summarise what changed in one short paragraph so the user knows what was improved. End with: "Run `/reload` to pick up the updated skill."
