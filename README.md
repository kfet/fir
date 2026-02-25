# fir

A fast, portable AI coding agent. Single binary, no runtime dependencies.

`fir` is a Go implementation of [pi](https://github.com/badlogic/pi-mono) by
[Mario Zechner](https://github.com/badlogic) and closely tracks it upstream.
All credit for the original design — the agent loop, tool system, TUI,
multi-provider architecture, and TS extension framework goes to that project.

The motivation for this port is size and portability, specifically I was aiming
for an efficient minimal agent running on a Raspberry Pi Zero W.

One additional feature is the native ACP mode: run fir as an [Agent Client Protocol](https://github.com/coder/acp-go-sdk) server (`--mode acp`) for coding editor integrations such as Zed; communicates via newline-delimited JSON-RPC 2.0 over stdio.

## Features

- **Interactive & non-interactive modes** — REPL, one-shot (`-p`), and JSON-RPC
- **Multi-provider** — Anthropic, OpenAI, Google Gemini, Groq, xAI, Mistral, OpenRouter, AWS Bedrock, Azure OpenAI
- **Built-in tools** — `read`, `bash`, `edit`, `write`, `grep`, `find`, `ls`
- **Sessions** — continue (`-c`) or resume (`-r`) previous conversations
- **Extensions & skills** — extend with custom tools and workflows
- **Tiny footprint** — ~10 MB static binary, runs on a Raspberry Pi Zero W 1.1

## Install

### Install script (macOS, Linux, Raspberry Pi)

```bash
curl -fsSL https://raw.githubusercontent.com/kfet/fir/main/install.sh | sh
```

For private repos, the script uses `gh` if installed, or `GITHUB_TOKEN`:
```bash
# Option 1: gh CLI (handles auth automatically)
gh auth login
curl -fsSL https://raw.githubusercontent.com/kfet/fir/main/install.sh | sh

# Option 2: token
GITHUB_TOKEN=ghp_... curl -fsSL https://raw.githubusercontent.com/kfet/fir/main/install.sh | sh
```

### Go install

Requires [Go 1.24+](https://go.dev/dl/).

```bash
go install github.com/kfet/fir/cmd/fir@latest
```

For private repos, set `GOPRIVATE` and use SSH:
```bash
GOPRIVATE=github.com/kfet/fir go install github.com/kfet/fir/cmd/fir@latest
```

### Build from source

```bash
git clone https://github.com/kfet/fir.git
cd fir
make install    # installs to $GOPATH/bin
```

### Update

```bash
fir update
```

`fir` checks for updates automatically and shows a notice when a new version
is available.

### macOS Gatekeeper Note

If you download the pre-compiled binary on macOS (via the install script or GitHub Releases), macOS may block it with the error: *"Apple could not verify 'fir-darwin-arm64' is free of malware"*.

To fix this, remove the quarantine attribute from the downloaded binary:

```bash
xattr -d com.apple.quarantine $(which fir)
```

## Usage

```bash
# Interactive mode
fir

# Non-interactive (process and exit)
fir -p "List all Go files in pkg/"

# Include files as context
fir @README.md @main.go "Summarize this project"

# Continue previous session
fir -c "What were we working on?"

# Pick a provider and model
fir --provider anthropic --model claude-sonnet-4-20250514

# Set thinking level
fir --thinking high "Design a distributed cache"

# List available models
fir --list-models gemini
```

## Configuration

### API keys

Set provider keys via environment variables:

```bash
export ANTHROPIC_API_KEY="sk-..."
export OPENAI_API_KEY="sk-..."
export GEMINI_API_KEY="..."
export GROQ_API_KEY="gsk_..."
export XAI_API_KEY="xai-..."
export OPENROUTER_API_KEY="sk-or-..."
export MISTRAL_API_KEY="..."
export AWS_PROFILE="..."           # for Bedrock
```

### Global config (`~/.fir/agent/`)

The global config directory (override with `FIR_AGENT_DIR`) holds:

| File | Purpose |
|---|---|
| `settings.json` | Default provider, model, thinking budgets, compaction, retry, terminal, and image settings |
| `keybindings.json` | Custom key bindings for interactive mode |
| `sessions/` | Saved conversation sessions |

### Project config (`.fir/` in your project root)

Per-project settings live in a `.fir/` directory at the root of your repo:

| Path | Purpose |
|---|---|
| `.fir/settings.json` | Project-level overrides (merged on top of global settings) |
| `.fir/skills/` | Project-specific skills (auto-discovered) |
| `.fir/prompts/` | Project-specific prompt templates |

### Project context files

fir reads `AGENTS.md` (or `CLAUDE.md`) files from the working directory and
its ancestors, automatically including them in the system prompt as project
context.

### Extensions

Extensions add custom tools, commands, keyboard shortcuts, and event hooks.
They are **off by default** and must be enabled by name.

**Enable via config** — add an `"extensions"` array to `settings.json` (global
or project):

```json
{
  "extensions": ["notify", "sandbox"]
}
```

**Enable via CLI** — use `--extension` / `-e` (merges with config, deduplicated):

```bash
fir -e notify -e sandbox "do something"
```

**Disable all** — `--no-extensions` overrides everything:

```bash
fir --no-extensions "do something"
```

#### Built-in extensions

| Name | Description |
|---|---|
| `notify` | Sends a native terminal notification (OSC 777/99) when the agent finishes. Works in Ghostty, iTerm2, WezTerm, Kitty. |
| `sandbox` | (TBD) Wraps bash commands with OS-level filesystem and network restrictions. Configured via `~/.fir/agent/sandbox.json` (global) or `.fir/sandbox.json` (project). |

#### Writing custom extensions

Extensions are Go packages that register themselves at build time via `init()`:

```go
package myext

import "github.com/kfet/fir/pkg/extension"

func init() {
    extension.Register("myext", func(api extension.API) {
        api.On("agent_end", func(event *extension.Event, ctx extension.Context) (any, error) {
            // called when the agent finishes a response
            return nil, nil
        })
    })
}
```

Add a blank import in `cmd/fir/app.go` and rebuild:

```go
import _ "github.com/kfet/fir/pkg/extensions/myext"
```

Extensions can subscribe to lifecycle events (`session_start`, `agent_start`,
`agent_end`, `tool_call`, `tool_result`, `input`, etc.), register tools
callable by the LLM, add slash commands, define CLI flags, and register
keyboard shortcuts. See `pkg/extension/types.go` for the full API.

## Build

```bash
make build          # build to ./bin/fir
make install        # install to $GOPATH/bin
make build-all      # cross-compile for all targets
make test           # run tests
make test-race      # run tests with race detector
make test-cover     # run tests with coverage
make vet            # static analysis
make clean          # remove build artifacts
```

### Cross-compilation targets

| Target               | GOOS    | GOARCH | Notes                      |
|----------------------|---------|--------|----------------------------|
| macOS Apple Silicon  | darwin  | arm64  | M1/M2/M3/M4               |
| macOS Intel          | darwin  | amd64  |                            |
| Raspberry Pi Zero W  | linux   | arm    | ARMv6                      |
| Raspberry Pi Zero 2W | linux   | arm64  | ARMv8 quad-core            |
| Linux x86_64         | linux   | amd64  |                            |

## Project structure

```
cmd/fir/          CLI entry point
pkg/
  agent/         Core agent loop
  ai/            LLM providers, streaming, model registry
  core/          Tools, sessions, prompt templates
  extension/     Extension loading
  extensions/    Built-in extensions
  modes/         Output modes (text, JSON, RPC)
  tui/           Terminal UI (markdown rendering, themes)
```

## Built with pi and fir

The initial port was built using the original
[pi](https://github.com/badlogic/pi-mono) coding agent. Once enough of the
codebase was functional, development switched to self-hosting: fir now
continues its own development.
