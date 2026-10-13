---
builtin: true
name: orient
description: Map an unfamiliar repo once and bookmark the map (layout, build/test commands, key packages, gotchas) with note prefix `orient:` so future sessions find it via `recall`. Run when `recall` finds no `orient:` bookmarks for this repo, or when the user asks to orient/seed a project.
---

# orient — seed a repo map future sessions can recall

Agent-driven; no human step. Budget: a handful of cheap commands, a few
minutes. Pointers, not conclusions.

## 1. Explore briefly

```bash
git rev-parse --show-toplevel; git log --oneline -5
ls; cat AGENTS.md CLAUDE.md README* 2>/dev/null | head -80
ls Makefile justfile package.json go.mod pyproject.toml Cargo.toml 2>/dev/null
grep -E '^[a-z][a-z0-9_-]*:' Makefile 2>/dev/null | head -20
```

Glance at top-level dirs (`ls pkg cmd src 2>/dev/null`) — don't read code
deeply.

## 2. Write the map as one short message

Under ~25 lines, covering only:

- **layout** — top-level dirs, one line each
- **build/test** — exact commands (`make all`, `make test`, …)
- **key packages/entry points** — paths
- **gotchas** — things that would bite a newcomer (generated files, min
  language versions, editor-requiring git commands, symlinked dirs)

Paths and commands, not opinions about the code. It will go stale; pointers
age better than conclusions.

## 3. Bookmark it

Quote a distinctive line of that message:

```
bookmark(quote="<distinctive line from the map>", note="orient: <repo-name> map — layout, build/test, gotchas")
```

One bookmark per repo per orient run. Additional standalone gotchas found
later may be bookmarked with `orient: gotcha — …`. Never put secrets in them.
