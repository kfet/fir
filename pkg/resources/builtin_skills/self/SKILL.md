---
builtin: true
name: self
description: Details about fir itself — configuration, capabilities, architecture, and how to discover features. Use this when the user asks how to configure fir, what it can do, or how it works.
---

# fir — AI Coding Agent

fir is a fast, portable AI coding agent. Single Go binary, no runtime dependencies. ~10 MB static binary.

For detailed CLI flags, run `fir --help`. For interactive commands and keyboard shortcuts, use `/help` inside fir.

## Modes

- **Interactive** (default) — full TUI with markdown rendering, streaming, slash commands, and live plan visualization (`/plan` to toggle)
- **Print** (`-p` / `--print`) — non-interactive one-shot; process prompt and exit
- **ACP** (`--mode acp`) — Agent Client Protocol, JSON-RPC 2.0 over stdio for IDE integrations
- **ACP** (`--mode acp`) — Agent Client Protocol server for editor integrations (e.g. Zed)

## Configuration Hierarchy

Settings merge in order (later wins):

1. **Global config** — `~/.fir/agent/settings.json` (override dir with `FIR_AGENT_DIR`)
2. **Project config** — `.fir/settings.json` in project root
3. **CLI flags** — override everything

All settings fields are optional. Project settings are merged on top of global settings field-by-field; nested objects merge recursively, arrays and primitives from the override win.

### Global Directory (`~/.fir/agent/`)

| File | Purpose |
|------|---------|
| `settings.json` | Default provider, model, thinking, compaction, retry, terminal, image settings |
| `keybindings.json` | Custom key bindings for interactive mode |
| `sessions/` | Saved conversation sessions |
| `skills/` | User-level skills (shared across projects) |

### Project Directory (`.fir/` in project root)

| Path | Purpose |
|------|---------|
| `settings.json` | Project-level setting overrides |
| `skills/` | Project-specific skills (auto-discovered) |
| `prompts/` | Project-specific prompt templates |
| `extensions/` | Project-specific extensions (auto-discovered) |

### Project Context Files

fir reads `AGENTS.md` (or `CLAUDE.md`) from the working directory and its ancestors, automatically including them in the system prompt. This is the primary way to give fir project-specific instructions.

## Authentication

Two methods:

1. **Environment variables** — set `<PROVIDER>_API_KEY` (e.g. `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`). Run `fir --help` for the full list.
2. **OAuth** — use `/login` in interactive mode. Supports Anthropic, Google (Antigravity & Gemini CLI), OpenAI Codex, and GitHub Copilot. Credentials are persisted automatically.

## Extensions

Extensions are standalone scripts (Python, shell, etc.) in `.fir/extensions/` (project) or `~/.config/fir/extensions/` (global). They communicate with fir over JSON-RPC 2.0 on stdio and can register custom tools, slash commands, and event handlers.

A Python SDK is provided (`fir_ext.py`). No code changes needed to add an extension — just drop a script in the directory.

Control loading via `settings.json` `"extensions"` allowlist, `--extension`/`-e` flags, or `--no-extensions`.

Extensions can optionally self-restrict by mode using script comment frontmatter (`# modes: ...`), e.g. `tui`, `acp`, `json`, or `text`.

Builtin extensions (notify, tmuxspinner, plan_nudger, etc.) are embedded in the binary and auto-discovered at lowest priority. Use `fir extensions` to list them and `fir extensions install <name>` to extract one for customisation.

Extensions auto-reload when their files are created, modified, or removed; interactive mode shows a status message on reload.

### Project extension: `/schedule`

The `schedule.py` extension in `.fir/extensions/` adds the `/schedule` slash command. It defers the next agent turn to a future time, useful after hitting a rate limit.

```
/schedule 45m              — resume in 45 minutes
/schedule 1h30m            — resume in 1 h 30 m
/schedule 2pm              — resume at 2:00 PM local time
/schedule 14:00            — resume at 14:00
/schedule 2:30pm           — resume at 2:30 PM
/schedule 45m run tests    — resume in 45 min with a custom message
/schedule cancel           — cancel (if only one active, else list)
/schedule cancel <id>      — cancel a specific schedule by ID
/schedule cancel all       — cancel all active schedules
/schedule                  — show current schedule status
```

Multiple schedules can be active concurrently, each assigned a unique ID (e.g. `[s1]`, `[s2]`). While active, a live countdown is shown in the status bar.

### Builtin extension: `autoresearch`

The `autoresearch` builtin extension provides an autonomous optimisation loop. It
registers two agent tools and one slash command:

**Tools (called by the agent during a loop):**
- `run_experiment` — runs `autoresearch.sh` in the repo root, parses every
  `METRIC name=value` line from stdout, and returns a structured metrics dict.
- `log_experiment` — appends a JSONL record to `autoresearch.jsonl` (timestamp,
  description, hypothesis, metrics, delta%, status).

**Slash command:**
```
/autoresearch    — print a summary table of all experiments logged so far
```

Use the `autoresearch-create` skill to set up and run a loop (see Skills below).
Install a customisable copy with `fir extensions install autoresearch`.

## Skills

Skills are Markdown instruction files at `.fir/skills/<name>/SKILL.md` with YAML frontmatter. They provide specialized workflows the agent can follow. Skills are auto-discovered from project, user, and builtin directories.

Use `/skills` to list loaded skills, `/reload` to pick up changes. Use `fir skills` to list all skills and `fir skills install <name>` to extract a builtin skill for customisation.

### Builtin skill: `autoresearch-create`

Sets up and drives an autonomous optimisation loop (inspired by
[pi-autoresearch](https://github.com/davebcn87/pi-autoresearch) and
[karpathy/autoresearch](https://github.com/karpathy/autoresearch)).

Invoke it by telling fir: *"use the autoresearch-create skill to optimise \<goal\>"*

The skill guides through:
1. **Setup** — create a git branch, write `autoresearch.sh` (benchmark) and
   `autoresearch.md` (living memory doc), run and log the baseline.
2. **Loop** — pick hypothesis → edit code → commit → `run_experiment` →
   `log_experiment` → keep or `git reset --hard HEAD~1` → repeat.
3. **Wrap-up** — final summary, offer to merge branch.

`autoresearch.jsonl` is an append-only audit trail of every experiment. The
`/autoresearch` command (from the `autoresearch` extension) shows a summary table.

## External Packages

Install external packages (git repos or local paths) that contribute skills, extensions, prompts, and themes:

```
fir install github.com/user/fir-pack        # install to user scope (~/.config/fir/packages/)
fir install ./local/path --local            # install to project scope (.fir/packages/)
fir uninstall github.com/user/fir-pack      # remove a package
fir packages list                           # list installed packages (source, scope, skill/ext counts, path)
fir packages update [source]               # pull latest for one or all packages
```

Packages are stored in `settings.json` under `"packages"`. Each entry is a string (`"github.com/user/repo"`) or an object with `"source"` and optional per-type filters. Installed package skills, prompts, extensions, and themes are automatically loaded.

## Key Concepts

- **Sessions** — conversations are persisted and can be continued (`-c`) or resumed (`-r`). Sessions form a tree; double-Escape or `/tree` navigates branches. Use `/session` for version, IDs, message/token stats, and enabled extensions. Use `/new [name]` to start a fresh session, optionally naming it.
- **Re-exec for local build testing** — use `/reexec` to restart into the current binary while preserving the active session, or `/reexec <path>` to switch to a specific built binary.
- **In-place update** — use `/update` to check for, download, and install the latest release, then automatically restart the session.
- **Compaction** — when context grows large, fir automatically summarizes older messages to stay within the model's context window. Configurable via `settings.json`.
- **Simplify** — use `/simplify [focus]` to review recent git changes (staged, unstaged, or last commit) and ask the agent to apply simplifications across code reuse, quality, and efficiency. Optional focus text narrows the review (e.g. `/simplify memory allocation`). Provided by the builtin `simplify` extension.
- **Btw (side questions)** — use `/btw <question>` to ask a quick question using the current session context without adding the exchange to history. The response is shown as a notification and the main conversation is unaffected. Works even while a task is streaming.
- **Thinking levels** — control reasoning depth: none, minimal, low, medium, high. Toggle with `Shift+Tab` or `--thinking`.
- **Scoped models** — use `/scoped-models` to pick which models `Ctrl+P` cycles through.
- **Tool steering** — `"steeringMode"` in settings controls whether the agent runs tools one-at-a-time or in parallel.

## settings.json Reference

All fields are optional. Nested objects merge recursively; arrays and primitives from overrides win.

```jsonc
{
  // Model defaults
  "defaultProvider": "anthropic",        // Provider name
  "defaultModel": "claude-sonnet-4-20250514",  // Model ID
  "defaultThinkingLevel": "medium",      // none, minimal, low, medium, high

  // UI
  "theme": "dark",                       // Theme name or path
  "hideThinkingBlock": false,            // Hide thinking blocks in output
  "quietStartup": false,                 // Suppress startup messages
  "collapseChangelog": false,            // Collapse changelog on startup
  "doubleEscapeAction": "tree",          // "tree" or "quit"
  "editorPaddingX": 0,                   // Horizontal editor padding
  "autocompleteMaxVisible": 5,           // Max autocomplete suggestions shown
  "showHardwareCursor": false,           // Show hardware cursor

  // Shell
  "shellPath": "/bin/bash",              // Shell for bash tool
  "shellCommandPrefix": "",              // Prepended to every bash command

  // Compaction
  "compaction": {
    "enabled": true,                     // Auto-compact when context is large
    "reserveTokens": 16384,              // Tokens reserved for compaction summary
    "keepRecentTokens": 20000,           // Recent tokens to keep uncompacted
    "maxContextTokens": 0               // Hard token cap — compact when exceeded (0 = disabled)
  },

  // Branch summary
  "branchSummary": {
    "reserveTokens": 16384               // Tokens reserved for branch summaries
  },

  // Retry on transient errors
  "retry": {
    "enabled": true,
    "maxRetries": 3,
    "baseDelayMs": 2000,
    "maxDelayMs": 60000
  },

  // Thinking token budgets per level
  "thinkingBudgets": {
    "minimal": 1024,
    "low": 4096,
    "medium": 10240,
    "high": 32768
  },

  // Terminal display
  "terminal": {
    "showImages": true,                  // Render images in terminal
    "clearOnShrink": false               // Clear screen when terminal shrinks
  },

  // Image handling
  "images": {
    "autoResize": true,                  // Auto-resize large images
    "blockImages": false                 // Block all image content
  },

  // Markdown rendering
  "markdown": {
    "codeBlockIndent": "  "              // Indentation for code blocks
  },

  // Model filtering (glob patterns; empty = all)
  "enabledModels": ["claude-*", "gpt-4*"],

  // Extension/skill/prompt/theme allowlists (empty = all)
  "extensions": [],
  "skills": [],
  "prompts": [],
  "themes": [],
  "enableSkillCommands": true,           // Allow /skills slash commands

  // Agent behavior
  "steeringMode": "one-at-a-time",      // "one-at-a-time" or "auto"
  "followUpMode": "one-at-a-time",      // "one-at-a-time" or "auto"
  "transport": "sse",                    // Transport protocol
  "serverTools": ["web_search"],         // Anthropic server-side tools: "web_search", "web_fetch", "code_execution"
                                          // You can also use raw types (e.g. "web_search_20260209"); model support is controlled per-model in models.json
  "serverCompaction": {                  // Anthropic server-side context compaction
    "enabled": true,
    "triggerTokens": 150000,             // When to trigger (min 50000, default 150000)
    "instructions": ""                   // Custom summarization prompt (replaces default)
  },

  // Internal (managed by fir)
  "lastChangelogVersion": "",            // Last changelog version shown to user
  "packages": []                         // Package metadata
}
```
