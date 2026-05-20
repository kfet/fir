# fir

A fast, portable AI coding agent. Single binary, no runtime dependencies.

`fir` is a Go implementation of [pi](https://github.com/badlogic/pi-mono) by
[Mario Zechner](https://github.com/badlogic) and closely tracks it upstream.

The motivation for this port is size and portability, specifically I was aiming
for an efficient minimal agent running on a Raspberry Pi Zero W.

One additional feature is the native ACP mode: run fir as an [Agent Client Protocol](https://github.com/coder/acp-go-sdk) server (`--mode acp`) for coding editor integrations such as Zed; communicates via newline-delimited JSON-RPC 2.0 over stdio.

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
brew install kfet/fir/fir
```

To pin or roll back to a specific minor channel (`fir@MAJOR.MINOR`, the
10 most recent are kept in the tap):

```bash
brew unlink fir 2>/dev/null
brew install kfet/fir/fir@0.29   # or: brew link --overwrite fir@0.29
```

The pinned formula tracks the latest patch within that minor; jump back
to the rolling latest with `brew unlink fir@0.29 && brew install kfet/fir/fir`.

### Install script (macOS, Linux, Raspberry Pi)

```bash
curl -fsSL https://raw.githubusercontent.com/kfet/fir-dist/main/install.sh | sh
```

Binaries are served from the public [`kfet/fir-dist`](https://github.com/kfet/fir-dist)
mirror — no authentication required. To install a specific version:

```bash
VERSION=0.30.0 curl -fsSL https://raw.githubusercontent.com/kfet/fir-dist/main/install.sh | sh
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

### macOS Gatekeeper Note

If you download the pre-compiled binary on macOS (via the install script or GitHub Releases), macOS may block it with the error: *"Apple could not verify 'fir-darwin-arm64' is free of malware"*.

To fix this, remove the quarantine attribute from the downloaded binary:

```bash
xattr -d com.apple.quarantine $(which fir)
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

## Build

```bash
make build          # build to ./bin/fir
make install        # install to $GOPATH/bin
make all            # build for all targets with all tests
make test-cover     # run tests with coverage
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
  extension/     Extension system (stdio JSON-RPC, external processes)
  modes/         Output modes (interactive, print, ACP)
  tui/           Terminal UI (markdown rendering, themes)
```

## Built with pi and fir

The initial port was built using the original
[pi](https://github.com/badlogic/pi-mono) coding agent. Once enough of the
codebase was functional, development switched to self-hosting: fir now
continues its own development.

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

## Release distribution

Every tag push publishes the same set of artefacts (binaries, `LICENSE`,
`THIRD_PARTY_NOTICES.md`, `checksums.txt`) to **two** GitHub Releases:

1. [`kfet/fir`](https://github.com/kfet/fir/releases) — the source repo.
2. [`kfet/fir-dist`](https://github.com/kfet/fir-dist/releases) — a public,
   binaries-only mirror. Same tag, same assets, same checksums.

Consumers (`install.sh`, the self-updater, the Homebrew formula, and the
`licensesURL` embedded in the binary) read from `kfet/fir-dist` so they
work without GitHub authentication regardless of the source repo's
visibility.

### Required repo secret

The release workflow uses `FIR_DIST_TOKEN` to push to `kfet/fir-dist`.
Create a fine-grained Personal Access Token scoped to that repo with
**Contents: Read and write**, and store it as a repository secret named
`FIR_DIST_TOKEN` on `kfet/fir`. Without this secret the mirror step is
skipped with a warning and the source-repo release still succeeds.


