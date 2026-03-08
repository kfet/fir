# Project Watch

Lightweight tracking of other coding-agent projects for ideas and feature parity.
Not file-level sync — just periodic review of releases and changelogs.

## Watched Projects

### [aider](https://github.com/paul-gauthier/aider)
- **Last checked**: 2026-03-06
- **Latest**: v0.86.2 (Feb 12, 2026)
- **Why**: Best-in-class repo-map / code indexing, git integration patterns
- **Watch**: Releases, HISTORY.md
- **Interesting areas**: `/aider/repomap.py`, `/aider/coders/`, architect mode
- **Recent notes**: Added Responses API support (o1-pro, o3-pro), Playwright web scraping, `--shell-completions`, `--attribute-co-authored-by` for commit attribution, OCaml repo-map support. Knight Rider spinner animation while waiting for LLM.

### [goose](https://github.com/block/goose)
- **Last checked**: 2026-03-06
- **Latest**: v1.27.1 (Mar 5, 2026) — very active, ~weekly releases
- **Why**: Rust-based agent with interesting extension/plugin model (MCP-native)
- **Watch**: Releases, CHANGELOG
- **Interesting areas**: Extension discovery, session management, tool permissions
- **Recent notes**: Structured {stdout, stderr} from shell tool, tree-sitter AST parsing for platform extensions, Anthropic adaptive thinking, MCP support for agentic CLI providers (Claude Code, Codex, Gemini CLI), ACP model selection support, self-signed HTTPS for goosed server. Worth watching their ACP integration closely.

### [cline](https://github.com/cline/cline)
- **Last checked**: 2026-03-06
- **Latest**: v3.57.0 (Feb 5, 2026)
- **Why**: Popular VS Code agent, good feature comparison baseline
- **Watch**: Releases
- **Interesting areas**: Diff editing approach, context management, approval UX
- **Recent notes**: Cline CLI 2.0 launched (Feb 13) — terminal-first redesign with `-y` for full autonomy, `--json` structured output, stdin/stdout piping for CI/CD integration. SDK API for programmatic access. Added Cline SDK API interface. Skills permanently enabled now.

### [claude-code](https://github.com/anthropics/claude-code)
- **Last checked**: 2026-03-06
- **Latest**: v2.1.x series (very active, multiple releases per week)
- **Why**: The canonical Anthropic reference implementation
- **Watch**: [CHANGELOG.md](https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md), npm releases
- **Interesting areas**: Tool definitions, system prompt patterns, permission model
- **Recent notes**: Opus 4.6 defaults to medium effort, "ultrathink" keyword for high effort. HTTP hooks (POST JSON instead of shell). `/simplify` and `/batch` bundled slash commands. Worktree config sharing. Sandbox mode for BashTool. Plugin ecosystem with marketplace. `/debug` command. `--from-pr` flag. Voice STT in 20 languages. Subagents via `--agents` JSON flag. Tool search for dynamic tool discovery. Compaction preserves images for cache reuse.

## Ideas Worth Exploring

Collected from project watch — things that could improve fir:

- **Structured shell output** (goose): Return {stdout, stderr, exit_code} separately instead of merged text
- **Tree-sitter AST parsing** (goose): For smarter code understanding in extensions
- **CLI piping / headless mode** (cline CLI 2.0): `-y` auto-approve, `--json` structured output for CI
- **Commit attribution** (aider): `--attribute-co-authored-by` for AI commit credit
- **HTTP hooks** (claude-code): POST JSON to URL as alternative to shell hooks
- **Worktree config sharing** (claude-code): Share project config across git worktrees
- **Tool search** (claude-code): Dynamic tool discovery from large catalogs

## How to Check

Run periodically (weekly-ish):
```
# Quick scan of recent releases
open https://github.com/paul-gauthier/aider/releases
open https://github.com/block/goose/releases
open https://github.com/cline/cline/releases
open https://github.com/anthropics/claude-code/releases
```

Or ask fir with the `research` skill to summarize recent changes.
