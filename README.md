# fir

A fast, portable AI coding agent. Single binary, no runtime dependencies.

`fir` originated as Go implementation of [pi](https://github.com/badlogic/pi-mono) by
[Mario Zechner](https://github.com/badlogic) and initally closely tracked it upstream.

It took about two weeks of using Pi on a Claude Max subscription to become self-hosted,
i.e. for the fir agent to become usable enough to drive its own implementation.

The motivation for this port is size and portability, specifically I was aiming
for an efficient minimal agent running on a Raspberry Pi Zero W 1.1.

Additionally I found the lack of MCP and ACP support personally very limiting,
since MCP is extremely useful in my work, and good ACP support is required to use an
agent in my editor of choice - [Zed](https://zed.dev).

## Features

- **Interactive & non-interactive modes** — REPL, one-shot (`-p`), and ACP
- **Multi-provider** — Anthropic, OpenAI, Google Gemini, Groq, xAI, Mistral, OpenRouter, AWS Bedrock, Azure OpenAI
- **Built-in tools** — `read`, `bash`, `edit`, `write`, `grep`, `find`, `ls`
- **Sessions** — continue (`-c`) or resume (`-r`) previous conversations
- **Extensions & skills** — extend with custom tools and workflows
- **Tiny footprint** — ~10 MB static binary, runs on a Raspberry Pi Zero W 1.1

## Install

### Homebrew (macOS, Linux)

```bash
brew install kfet/ai/fir
```

To pin or roll back to a specific minor channel (`fir@MAJOR.MINOR`, the
10 most recent are kept in the tap):

```bash
brew unlink fir 2>/dev/null
brew install kfet/ai/fir@0.29   # or: brew link --overwrite fir@0.29
```

The pinned formula tracks the latest patch within that minor; jump back
to the rolling latest with `brew unlink fir@0.29 && brew install kfet/ai/fir`.

### Install script (macOS, Linux, Raspberry Pi)

```bash
curl -fsSL https://raw.githubusercontent.com/kfet/fir-dist/main/install.sh | sh
```

Binaries are served from the public [`kfet/fir-dist`](https://github.com/kfet/fir-dist)
mirror — no authentication required. To install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/kfet/fir-dist/main/install.sh | VERSION=0.30.0 sh
```

### Shell completion

Bash and zsh completion are installed automatically by Homebrew and the
`install.sh` script. For manual setup (e.g. after `go install`):

```bash
fir completion bash > ~/.local/share/bash-completion/completions/fir
fir completion zsh  > "${fpath[1]}/_fir" && compinit
```

The completion handles every flag and subcommand and dynamically completes
`--provider`, `--model`, `--extension`, `--skill`, and `--session`.

## Usage

```bash
# Interactive mode; inside the TUI type /help for in-session help
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

# ...And more - checkout the help of the CLI options
fir --help

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

### Global config (`~/.config/fir/`)

The global config directory (override with `FIR_AGENT_DIR` or the `--agent-dir <dir>` CLI flag) holds:

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

Extensions are standalone scripts (Python, shell, etc.) that run as subprocesses
and communicate with fir over JSON-RPC 2.0. Place them in `.fir/extensions/`
in your project directory and they are automatically discovered and started.

fir ships a Python SDK in `pkg/extension/sdk/python/fir_ext.py` that
handles the JSON-RPC protocol for you.

Extensions placed in `.fir/extensions/` (project) or `<agent-dir>/extensions/`
(global; `~/.config/fir/extensions/` by default, override with `FIR_AGENT_DIR`
or `fir --agent-dir <dir>`) are discovered automatically. To restrict which
extensions are loaded, set `extensions` in your settings file as a **name
allowlist**:

```jsonc
// .fir/settings.json (project) or ~/.config/fir/settings.json (global)
{
  "extensions": ["demo", "hello"]
}
```

When the list is non-empty, only discovered extensions whose name matches an
entry are started. When absent or empty, all discovered extensions run.

You can also enable specific extensions per-invocation with `--extension` /
`-e`, or disable all of them with `--no-extensions`:

```bash
fir -e demo -e hello "do something"   # only these two
fir --no-extensions "do something"     # none at all
```

### MCP Servers

fir integrates with [Model Context Protocol](https://modelcontextprotocol.io) (MCP) servers.
Configure them in `.fir/mcp.json` (project) or `~/.config/fir/mcp.json` (global), or use
drop-in files in `~/.config/fir/mcp.d/*.json`. Use `/mcp` to inspect configured servers and
`/mcp reload` to pick up config changes without restarting. See [docs/mcp.md](docs/mcp.md)
for full details.

## Build

```bash
make build          # build to ./bin/fir
make install        # install to $GOPATH/bin
make all            # build for all targets with all tests (incl. the coverage gate)
make test-cover     # run tests with coverage, print the per-function breakdown
make coverage       # run the covgate coverage gate (see .covignore)
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

## Contributing & security

fir is primarily a personal project published in the open. Bug fixes,
documentation, and security fixes are welcome; large refactors and new core
features are usually better built on the [extension surface](#extensions). See
[CONTRIBUTING.md](CONTRIBUTING.md) for the full stance before opening a PR.

To report a security vulnerability, **do not** open a public issue — use
GitHub's private vulnerability reporting and see [SECURITY.md](SECURITY.md).

## License

`fir` itself is distributed under the [MIT License](LICENSE).

Third-party Go modules linked into the released binaries are covered by their
own licenses (MIT, Apache-2.0, BSD-2/3-Clause, MPL-2.0, ISC). A generated
attribution file is published alongside every GitHub release as
`THIRD_PARTY_NOTICES.md`, together with a `checksums.txt` covering every
asset. Running `fir --version` prints a link to the exact release page.

To regenerate the notices file locally:

```sh
make notices          # writes THIRD_PARTY_NOTICES.md
make check-licenses   # fails on forbidden/restricted licenses
```

