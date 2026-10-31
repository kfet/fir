# Install an Agent Plugin in fir

fir loads [Agent Plugins](https://agent-plugins.org/) — portable packages of
[Agent Skills](https://agentskills.io/) and MCP servers that work across
compatible coding agents. fir implements the
[Agent Plugins 1.0.0 specification](https://agent-plugins.org/specification)
for both component types: skills and MCP servers (stdio, Streamable HTTP and
legacy HTTP+SSE).

## Install

An Agent Plugin is any directory with a `plugin.json` at its root. Install it
from a git repository or a local path with `fir install`:

```bash
fir install github.com/example/my-plugin          # user scope, every project
fir install github.com/example/my-plugin@v1.2.0   # pin a tag, branch or commit
fir install ./path/to/my-plugin                   # a local directory
fir install ./path/to/my-plugin --local           # this project only (.fir/)
```

A subdirectory of a repository works too:
`fir install github.com/example/plugins/tree/main/my-plugin`.

fir prints what it found:

```
Agent Plugin "my-plugin": 2 skill(s), 1 MCP server(s)
```

and any problems (an invalid skill, a skipped MCP server) as warnings. Start
a new session — or run `/reload` in a running one — to use it.

Manage installed plugins like any other fir package:

```bash
fir packages list                      # what is installed, and where
fir packages update [source]           # pull the latest version
fir uninstall github.com/example/my-plugin
```

## What fir loads

| Plugin file              | What fir does                                                                 |
|--------------------------|-------------------------------------------------------------------------------|
| `plugin.json`            | Validated first. An invalid manifest disables the whole plugin.               |
| `skills/<name>/SKILL.md` | Each one is a skill (one level deep only). Invalid skills are skipped.        |
| `mcp.json`               | Each server becomes an MCP server in fir. Invalid entries are skipped.        |
| anything else            | Ignored. `fir.json` and fir's own auto-discovery do not apply to plugins.     |

Skills appear in `/skills` and `fir skills list` with origin
`pkg:<source>`. MCP servers appear in `/mcp` and `fir mcp list` under their
names from `mcp.json`; their tools are named `mcp__<server>__<tool>` as usual.

### MCP servers and your own config

Plugin MCP servers are the lowest-precedence layer of fir's
[MCP configuration](mcp.md):

1. installed Agent Plugins (`<plugin>/mcp.json`)
2. `~/.config/fir/mcp.json`
3. `~/.config/fir/mcp.d/*.json`
4. `<project>/.fir/mcp.json`

If your user or project config defines a server with the same name as a
plugin's, yours wins and fir warns that the plugin's server is overridden.
That is also how you adjust a plugin server — redefine it under the same
name. If two plugins define the same name, the first installed one wins.

To keep a plugin's skills but not its MCP servers, override each server with
`"disabled": true` in your own config.

Remote (`streamable-http`, `sse`) servers that answer `401` go through fir's
normal MCP OAuth login (`/mcp login <server>`); plugins carry no credentials.

### Runtime environment of stdio servers

- **Working directory**: the plugin root, unless the entry sets `cwd`.
- **`PLUGIN_ROOT`**: the plugin's real (symlink-resolved) install directory.
- **`PLUGIN_DATA`**: a persistent, writable directory for the plugin's own
  state. fir creates it before launching the server and keeps it across
  updates and reinstalls:
  - user scope: `~/.config/fir/plugin-data/<plugin-name>-<id>/`
  - project scope: `<project>/.fir/plugin-data/<plugin-name>-<id>/`

  `<id>` is derived from the install source, so two packages with the same
  plugin name never share data. fir creates it only for plugins that have a
  stdio server.

  Uninstalling a plugin leaves its data directory in place; delete it by hand
  if you no longer need it.
- `${PLUGIN_ROOT}` and `${PLUGIN_DATA}` are expanded in `args`, `env` values
  and `cwd` — nothing else is expanded, anywhere.
- A bare `command` (e.g. `npx`) is found on fir's own `PATH`; a `./` command
  runs the executable bundled in the plugin.

## Safety

Installing a plugin runs its MCP servers' commands on your machine with your
permissions, the same as adding them to `mcp.json` yourself. Only install
plugins you trust, and read `mcp.json` before you do.

fir refuses any file a plugin references that resolves (through `..` or a
symlink) outside the plugin directory, and it never downloads schemas or
anything else while loading a plugin.

## Troubleshooting

- **Nothing loaded / "plugin.json is invalid"** — run `fir install` again on
  the path to see the reason, or check fir's log. Common causes: a missing or
  unknown `$schema`, or a `name` that is not lowercase `a-z 0-9 - .`.
- **A skill is missing** — its `SKILL.md` needs frontmatter with a `name`
  equal to its directory name and a `description`.
- **An MCP server is missing** — `fir mcp list` and the startup warnings say
  why an entry was skipped (unknown field, unsupported `type`, a `command` or
  `cwd` outside the plugin, a non-https remote URL, …).
- **A server is listed but has no tools** — it was valid but failed to start
  or connect; `/mcp` shows the error.

## For plugin authors

See [Build an Agent Plugin](https://agent-plugins.org/plugin-authors/build-an-agent-plugin).
A minimal plugin:

```
my-plugin/
├── plugin.json
├── skills/
│   └── summarize/
│       └── SKILL.md
└── mcp.json
```

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "my-plugin",
  "version": "1.0.0",
  "description": "Summarize documents"
}
```

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "docs": {
      "type": "stdio",
      "command": "./bin/docs-server",
      "args": ["--cache", "${PLUGIN_DATA}/cache"]
    }
  }
}
```

### fir's client namespace

fir reserves the client extension namespace **`dev.fir`** — both the
`extensions["dev.fir"]` object in `plugin.json` and a top-level `dev.fir/`
directory. fir defines no contents for it yet and ignores both; do not rely
on any behaviour there. Other clients' namespaces are ignored without being
validated.

## Conformance

fir meets every item of the specification's
[client conformance checklist](https://agent-plugins.org/client-implementers/conformance).
The tests live in `pkg/agentplugin/conformance_test.go`, one per checklist
item, against the fixture plugin in `pkg/agentplugin/testdata/fixture-plugin`.
Interpretation notes:

- A bare `command` containing whitespace is rejected as a shell command line
  rather than a single executable token.
- Path containment (§4.1) covers every file fir itself opens for a plugin:
  `plugin.json`, `skills/`, each `SKILL.md`, `mcp.json`, and server
  `command` and `cwd`. What a running skill script or MCP server does at
  runtime is not sandboxed — the spec explicitly leaves that out of scope.
- The loopback exception for `http://` URLs matches the host `localhost`
  exactly (lowercase) or a loopback IP literal.
