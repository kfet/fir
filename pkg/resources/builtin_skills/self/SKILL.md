---
name: self
description: \[SYS_EXT\] Your are fir. Understand details about yourself and how you operate — configuration, capabilities, architecture, and how to discover features. Configure yourself/fir, answer what you/fir can do, or how your operate.
builtin: true
---

# fir — AI Coding Agent

You are fir. fir is a fast, portable AI coding agent. Single Go binary, no runtime dependencies. <2>0 MB static binary.

For detailed CLI flags, run `fir --help`. For interactive commands and keyboard shortcuts, use `/help` inside fir.

## Modes

- **Interactive** (default) — full TUI with markdown rendering, streaming, slash commands, and live plan visualization (`/plan` to toggle)
- **Print** (`-p` / `--print`) — non-interactive one-shot; process prompt and exit
- **ACP** (`--mode acp`) — Agent Client Protocol, JSON-RPC 2.0 over stdio for IDE integrations (e.g. Zed)

## Configuration Hierarchy

Settings merge in order (later wins):

1. **Global config** — `~/.config/fir/settings.json` (override dir with `FIR_AGENT_DIR`)
2. **Project config** — `.fir/settings.json` in project root
3. **CLI flags** — override everything

All settings fields are optional. Project settings are merged on top of global settings field-by-field; nested objects merge recursively, arrays and primitives from the override win.

### Global Directory (`~/.config/fir/`)

| File | Purpose |
|------|---------|
| `settings.json` | Default provider, model, thinking, compaction, retry, terminal, image settings |
| `keybindings.json` | Custom key bindings for interactive mode |
| `sessions/` | Saved conversation sessions |
| `skills/` | User-level skills (shared across projects) |
| `prompts/` | User-level prompt templates (shared across projects) |
| `extensions/` | User-level extensions (shared across projects) |
| `packages/` | Installed external packages (user scope) |

### Project Directory (`.fir/` in project root)

| Path | Purpose |
|------|---------|
| `settings.json` | Project-level setting overrides |
| `keybindings.json` | Project-level keybinding overrides (merged on top of global) |
| `skills/` | Project-specific skills (auto-discovered) |
| `prompts/` | Project-specific prompt templates |
| `extensions/` | Project-specific extensions (auto-discovered) |
| `packages/` | Installed external packages (project scope, via `--local`) |
| `mcp.json` | MCP server configuration (project-level) |

### Project Context Files

fir reads `AGENTS.md` (or `CLAUDE.md`) from the working directory and its ancestors, automatically including them in the system prompt. This is the primary way to give fir project-specific instructions.

## Authentication

Two methods:

1. **Environment variables** — set `<PROVIDER>_API_KEY` (e.g. `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`). See the full list below.
2. **OAuth** — use `/login` in interactive mode. Supports Anthropic, Google (Antigravity & Gemini CLI), OpenAI Codex, and GitHub Copilot. Credentials are persisted automatically.
3. New OAuth providers can be added as extensions

## Environment Variables

{{FIR_ENV_VARS_TABLE}}

## Extensions

Extensions are standalone scripts (Python, shell, etc.) in `.fir/extensions/` (project) or `~/.config/fir/extensions/` (global). They communicate with fir over JSON-RPC 2.0 on stdio and can register custom tools, slash commands, and event handlers.

A Python SDK is provided (`fir_ext.py`). No code changes needed to add an extension — just drop a script in the directory.

Control loading via `settings.json` `"extensions"` allowlist, `--extension`/`-e` flags, or `--no-extensions`.

Extensions can optionally self-restrict by mode using script comment frontmatter (`# modes: ...`), e.g. `tui`, `acp`, or `text`.

Builtin extensions (notify, tmuxspinner, plan_nudger, etc.) are embedded in the binary and auto-discovered at lowest priority. Use `fir extensions` to list them and `fir extensions install <name>` to extract one for customisation.

Extensions can be reloaded via `/reload`; there is no automatic file watching. Reloading extensions rebuilds the system prompt, which might impact prompt cacheing.

## Skills

Skills are Markdown instruction files at `.fir/skills/<name>/SKILL.md` with YAML frontmatter. They provide specialized workflows the agent can follow. Skills are auto-discovered from project, user, and builtin directories.

Use `/skills` to list loaded skills, `/reload` to pick up changes. Use `fir skills` to list all skills and `fir skills install <name>` to extract a builtin skill for customisation.

## External Packages

Install/manage external packages (git repos or local paths) that contribute skills, extensions, prompts, and themes:

```
fir install github.com/user/fir-pack        # install to user scope (~/.config/fir/packages/)
fir install ./local/path --local            # install to project scope (.fir/packages/)
fir uninstall github.com/user/fir-pack      # remove a package
fir packages list                           # list installed packages (source, scope, skill/ext counts, path)
fir packages update [source]                # pull latest for one or all packages
```

Packages are stored in `settings.json` under `"packages"`. Each entry is a string (`"github.com/user/repo"`) or an object with `"source"` and optional per-type filters. Installed package skills, prompts, extensions, and themes are automatically loaded.

## Resource Lookup Paths

Skills, prompts, and themes are discovered from multiple locations, merged in priority order (earlier wins on name collisions):

1. **CLI flags** — `--skill <path>`, `--prompt-template <path>`, etc.
2. **Project directory** — `.fir/skills/`, `.fir/prompts/`, `.fir/extensions/`
3. **User directory** — `~/.config/fir/skills/`, `~/.config/fir/prompts/`, `~/.config/fir/extensions/`
4. **Settings paths** — the `"skills"`, `"prompts"`, and `"themes"` arrays in `settings.json`
5. **Installed packages** — skills/prompts/extensions/themes contributed by `fir install`-ed packages
6. **Builtins** — embedded in the binary

### Settings Path Resolution

Paths in `"skills"`, `"prompts"`, and `"themes"` settings arrays support three forms:

| Form | Example | Resolves to |
|------|---------|-------------|
| Absolute | `"/opt/shared/skills"` | `/opt/shared/skills` |
| Home-relative | `"~/my-skills"` | `$HOME/my-skills` |
| Relative | `"skills"` | `$CWD/skills` (resolved at startup) |

**Relative paths are resolved against the current working directory**, not against the settings file. This is intentional — it makes relative paths portable across projects:

```jsonc
// In ~/.config/fir/settings.json (global):
{
  "skills": ["skills"],       // → finds ./skills/ in any project that has one
  "prompts": ["prompts"]      // → finds ./prompts/ in any project that has one
}
```

This lets you establish a convention (e.g. "every project keeps skills in `./skills/`") and have fir discover them automatically without per-project configuration. Paths that don't exist are silently skipped.

For project-specific settings (`.fir/settings.json`), relative paths also resolve against cwd (the project root), which is the natural expectation.

## Key Concepts

- **Sessions** — conversations are persisted and can be continued (`-c`) or resumed (`-r`). Sessions form a tree; double-Escape or `/tree` navigates branches. Use `/session` for version, IDs, message/token stats, and enabled extensions. Use `/new [prompt]` to start a fresh session, optionally with an initial prompt that is submitted immediately.
- **Re-exec for local build testing** — use `/reexec` to restart into the current binary while preserving the active session, or `/reexec <path>` to switch to a specific built binary.
- **In-place update** — use `/update` to check for, download, and install the latest release, then automatically restart the session.
- **Compaction** — when context grows large, fir automatically summarizes older messages to stay within the model's context window. Configurable via `settings.json`.
- **Review & Fix** — use the `review-and-fix` skill to run a one-shot review pass over recent changes. Covers build breaks, security, correctness, test gaps, and simplification (code reuse, quality, efficiency). Fixes all issues found, verifies the build, and commits.
- **Aside (ephemeral side queries)** — use `/aside <question>` to ask a quick side question using the current session context without adding the exchange to history. The response is shown as a notification and the main conversation is unaffected. Works even while a task is streaming. When given tool calls, `/aside` orchestrates multi-tool workflows ephemerally via `ctx.call_tool()` + `ctx.side_query()` — raw tool outputs never enter the main conversation, only the synthesis is returned. Use `/aside read the 5 largest .go files and summarise` to ask the agent to build and run an aside automatically. The `aside` tool can also be called directly by the agent.
- **Thinking levels** — control reasoning depth: none, minimal, low, medium, high. Toggle with `Shift+Tab` or `--thinking`.
- **Tool steering** — `"steeringMode"` in settings controls whether the agent runs tools one-at-a-time or in parallel.
- **call_tool bridge** — extensions can call any registered tool (built-in, extension, or MCP) programmatically via `ctx.call_tool(name, params)`. Results are returned directly and never enter conversation history. This enables extensions to build rich orchestration workflows.
- **MCP channel servers** — MCP servers that advertise the `claude/channel` experimental capability can push messages into the running session via `notifications/claude/channel` notifications. Messages are injected into the agent conversation automatically. The server's `channel_reply` tool is a regular MCP tool. Configure channel servers in `.fir/mcp.json` like any other MCP server — no special config needed.
- **MCP configuration** — configure MCP servers in `.fir/mcp.json` (project) or `~/.config/fir/mcp.json` (global). Three transports are supported:
  - **stdio** (default) — launches a local subprocess:
    ```json
    {
      "mcpServers": {
        "filesystem": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/user/projects"],
          "env": { "NODE_PATH": "/usr/local/lib/node_modules" }
        }
      }
    }
    ```
  - **streamable** (recommended for remote) — connects to a remote HTTP MCP server:
    ```json
    {
      "mcpServers": {
        "my-remote": {
          "transport": "streamable",
          "url": "https://my-server.example.com/mcp"
        }
      }
    }
    ```
  - **sse** — connects via Server-Sent Events (legacy HTTP transport):
    ```json
    {
      "mcpServers": {
        "legacy": {
          "transport": "sse",
          "url": "https://old-server.example.com/sse"
        }
      }
    }
    ```
- **MCP inspection** — use `/mcp` to see all configured MCP servers with their connection status, transport, capabilities (resources, prompts), and a full list of exposed tools with descriptions.

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

  // Additional resource lookup paths (resolved per-invocation relative to cwd)
  // Use absolute paths, ~/... paths, or relative paths.
  // Relative paths resolve against the working directory, making them portable:
  //   "skills": ["skills"]  → looks for ./skills/ in every project you open
  //   "skills": ["~/shared-skills"]  → always points to the same directory
  "skills": [],
  "prompts": [],
  "themes": [],
  "enableSkillCommands": true,           // Allow /skills slash commands
  "enableSysExtensions": true,           // Allow extensions to prepend [SYS_EXT] context blocks

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
