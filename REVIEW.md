# Package Install System — Code Review

## URGENT

### 1. Command injection via git clone URL
`git.go` passes user-provided `url` directly to `exec.Command("git", "clone", url, dest)`. While `exec.Command` does use `argv` (not shell), git itself interprets URLs starting with `-` as flags. A source like `--upload-pack=evil` could be passed through `ParseSource` as a bare local path fallback and end up in `Clone`. **Fix**: validate that `url` doesn't start with `-` before passing to git, or prepend `--` before positional args: `git clone -- <url> <dest>`.

### 2. `@ref` is parsed but never used during clone
`ParseSource` correctly extracts `Ref` from `github.com/user/repo@main`, but `Manager.Install` calls `Clone(src.URL, dest)` without ever checking out the specified ref. The `Pinned` field is set but ignored. The clone will get the default branch, silently ignoring the user's requested ref. Either pass `--branch ref` to clone, or `git checkout ref` after clone.

### 3. Extensions not wired into resource loader
`resourceloader.go:219` destructures `ResolvePackageResources()` as `_, pkgSkills, pkgPrompts, _, err` — the first return (extensions) and fourth (themes) are discarded with `_`. Package extensions and themes are never loaded. This means `fir install` claims to discover extensions/themes but they have no effect.

### 4. Uninstall of local packages removes the original directory
`Uninstall` calls `os.RemoveAll(dest)` for git packages, which is correct. But if a user somehow registers a local path in the wrong scope, `removePackage` only filters by exact string match on the raw source. If the user passes a slightly different path form (e.g. trailing slash, relative vs absolute), the settings entry won't be found and the package remains registered. Not destructive, but confusing.

### 5. `ssh-missing-colon` test case is wrong
The test expects `git@github.com/user/repo` to error. But `ParseSource` first checks `strings.HasPrefix(s, "git@")` → enters `parseSSH` → `strings.Index(rest, ":")` returns -1 → error. This is actually correct behavior. (Not urgent, just verified.)

## SUGGESTIONS

### Dedup by canonical form, not raw string
`containsPackage` compares `entrySource(p) == source` using the raw input string. Installing `github.com/user/repo` and then `https://github.com/user/repo` creates two entries for the same repo. Compare by canonical `Host + "/" + Path` instead.

### `Update` only returns the last error
If multiple packages fail to update, only the last error is returned. Consider collecting all errors with `errors.Join`.

### `SetGlobalPackages` / `SetProjectPackages` don't persist
Need to verify these actually write to disk. The grep shows they exist, but if they only set in-memory state without calling `Save()`, packages are lost on restart.

### No `resources_test.go`
Auto-discovery and manifest expansion have no tests. These are non-trivial path-walking functions with glob patterns. Should have table-driven tests with temp dirs.

### No `install_test.go`
DESIGN.md specifies `cmd/fir/install_test.go` for integration tests but it doesn't exist.

### Manager test doesn't exercise git code path
`TestInstallUnlist` installs a local path (absolute dir), not a git URL. The git `Clone`/`Pull` path is only indirectly tested via `initBareRepo` helper. No test actually exercises `Install` with `src.Type == "git"`.

### `isLocalPath` has redundant logic
```go
if runtime.GOOS == "windows" || isWindowsAbsolute(s) {
    if isWindowsAbsolute(s) {
```
On non-windows, `isWindowsAbsolute` is called twice. On windows, any string enters the outer block even if not a windows path. Should just be `if isWindowsAbsolute(s) { return true }`.

### `autoDiscover` marks root `.md` as skills
Any `.md` file at root (README.md, LICENSE.md, CONTRIBUTING.md) gets registered as a skill. This will cause noise. Consider requiring files to be in a `skills/` subdir or have SKILL.md naming.

### `fmt.Printf` in library code
`Manager.Install`, `Uninstall`, `Update` all print to stdout directly. This makes the package hard to test and reuse. Pass an `io.Writer` or use a callback/logger.

### Missing `--ref` / `--branch` flag on install CLI
No way to specify a ref from the CLI other than `@ref` suffix syntax.

## CHECKLIST

- [x] CHANGELOG.md has entry under `## [Unreleased]`
- [x] Self skill (`.fir/skills/self/SKILL.md`) documents the new commands
- [ ] Extensions/themes from packages actually loaded (URGENT #3)
- [ ] `@ref` honored during clone (URGENT #2)
- [ ] Git arg injection hardened (URGENT #1)
- [ ] `resources_test.go` exists
- [ ] `install_test.go` integration test exists
