# fir Package Install System — Design

## Overview

Add the ability to install external packages (extensions + skills) from git repositories
or local paths. Similar to pi-mono's `PackageManager` but simpler and Go-native.

## Package Sources

| Format | Example | Notes |
|--------|---------|-------|
| Bare GitHub | `github.com/user/fir-pack` | Implies HTTPS clone |
| HTTPS URL | `https://github.com/user/fir-pack` | Any git host |
| SSH URL | `git@github.com:user/fir-pack` | SSH git |
| Local path | `./relative/path` or `/abs/path` | No clone, just symlink-read |

## Scope

- **User** (default): `<agent-dir>/packages/git/<host>/<path>/` (`~/.config/fir` unless overridden by `FIR_AGENT_DIR` or `--agent-dir`)
- **Project** (`--local`): `.fir/packages/git/<host>/<path>/`

## Package Manifest — `fir.json`

Optional file at the package root. If absent, auto-discovery runs.

```json
{
  "extensions": ["extensions/", "*.py"],
  "skills":     ["skills/"],
  "themes":     ["themes/"]
}
```

Auto-discovery (no manifest):
- Extensions: `*.py`, `*.sh` at any depth (excluding `.git/`, `node_modules/`)
- Skills: `SKILL.md` in subdirs; `*.md` at the package root
- Themes: `*.json` files named `theme.json` or in a `themes/` dir

## Settings Integration

`packages` array in `settings.json` (already exists as `[]any`). Each entry is either:
- A string: `"github.com/user/fir-pack"` (all resources, auto-discover)
- An object with optional per-type filters:
  ```json
  {"source": "github.com/user/fir-pack", "skills": ["shepherd/"], "extensions": []}
  ```

The `SetGlobalPackages` / `SetProjectPackages` methods on `SettingsManager` handle persistence.

## New Package: `pkg/pkg`

File: `pkg/pkg/manager.go`

```go
package pkg

type Source struct {
    Type    string // "git" | "local"
    Raw     string // original user input
    Host    string // e.g. "github.com"
    Path    string // e.g. "user/repo"
    URL     string // canonical HTTPS clone URL
    Ref     string // optional branch/tag/commit
    Pinned  bool   // true if ref was explicitly specified
    Local   string // for type=="local": resolved path
}

type Manager struct { ... }

func New(agentDir, cwd string) *Manager

// Install clones (or verifies local exists) and adds to settings.
func (m *Manager) Install(source string, local bool) error

// Uninstall removes the clone and deregisters from settings.
func (m *Manager) Uninstall(source string, local bool) error

// Update pulls latest for all (or one) registered packages.
func (m *Manager) Update(source string) error

// List returns all registered packages with their install paths.
func (m *Manager) List() ([]InstalledPackage, error)

// Resolve returns the set of resource paths contributed by all installed packages,
// for use by DefaultResourceLoader.
func (m *Manager) Resolve() (*ResolvedPackageResources, error)
```

## New File: `pkg/pkg/parse.go`

Parse a source string into a `Source` struct:
- Detect local path (starts with `.`, `/`, `~`, or Windows absolute)
- Detect SSH URL (`git@host:path`)
- Detect HTTPS URL (`https://host/path`)
- Detect bare `host/path` (treat as HTTPS to that host)
- Extract optional `@ref` suffix (e.g. `github.com/user/repo@main`)

## New File: `pkg/pkg/git.go`

Git operations using `os/exec`:
- `Clone(url, dest string) error`
- `Pull(dir string) error`
- `CurrentRef(dir string) (string, error)` — for listing

## New File: `pkg/pkg/resources.go`

Scan an installed package dir, apply manifest or auto-discover:
- Return lists of extension paths, skill paths, theme paths

## Settings — New Methods on `SettingsManager`

In `pkg/config/settings.go`:
- `GetGlobalPackages() []any`
- `SetGlobalPackages([]any)`  (already has `SetProjectPackages`)

## CLI — `cmd/fir/install.go`

New subcommands (registered in `cmd/fir/main.go`):

```
fir install <source> [--local]        — install a package
fir uninstall <source> [--local]      — remove a package
fir update [source]                   — update packages
fir packages [list]                   — list installed packages
```

Also extend `fir skills install` and `fir extensions install` to accept
external sources (git URLs / local paths) in addition to builtins.

## Resource Loader Integration

In `pkg/resources/resourceloader.go`, after loading local resources, call:
```go
pkgResources, err := pkgMgr.Resolve()
// append pkgResources.SkillPaths, ExtensionPaths, etc.
```

The `DefaultResourceLoader` gets a new optional `PackageManager` field.

## Testing

- `pkg/pkg/parse_test.go` — table-driven tests for source parsing
- `pkg/pkg/manager_test.go` — uses a temp dir to test install/uninstall/list with a fake git repo
- `cmd/fir/install_test.go` — integration: verify CLI dispatches correctly

## CLI Help Example

```
$ fir install github.com/alice/fir-skills
Installing github.com/alice/fir-skills (user scope)...
Cloned to ~/.config/fir/packages/git/github.com/alice/fir-skills
Discovered: 3 skills, 1 extension
Registered in ~/.config/fir/settings.json

$ fir packages list
SOURCE                          SCOPE    SKILLS  EXTENSIONS  PATH
github.com/alice/fir-skills     user     3       1           ~/.config/fir/packages/git/...

$ fir uninstall github.com/alice/fir-skills
Removed github.com/alice/fir-skills

$ fir update
Updating github.com/alice/fir-skills... done (already up to date)
```
