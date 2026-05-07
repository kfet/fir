---
name: extract-module
description: Extract a self-contained Go package out of fir into its own repo at github.com/kfet/<name>. Use when carving a subtree out of pkg/ for reuse — e.g. firpty, skipstone, pinoauth precedent. While using this skill, update it in place when a genuinely new, non-obvious high-level pattern emerges that the current instructions wouldn't have produced — skip anything an AI would already do by default.
---

# Extract a Go module out of fir into its own repo

Use when the user wants to carve a self-contained subtree (typically under `pkg/`) into a standalone module published at `github.com/kfet/<name>`.

## Discover the precedent — don't reinvent

There are already extracted siblings under `/Users/kfet/dev/ai/`. **Find them, study them, adopt what applies.**

```bash
grep -rh "github.com/kfet" go.mod go.sum \
  | grep -oE "github.com/kfet/[A-Za-z0-9_.-]+" \
  | grep -v "^github.com/kfet/fir$" \
  | sort -u
```

For each sibling repo found, inspect on disk at `/Users/kfet/dev/ai/<name>/`:

- Top-level layout (`ls`)
- `Makefile` — coverage gate, test/fmt/vet targets, release flow
- `.covignore` — what's excluded from the coverage gate and why
- `.github/` — CI workflows, issue templates, anything else
- `README.md`, `LICENSE`, `CHANGELOG.md`, `AGENTS.md`, `doc.go`, `VERSION`, `.gitignore`
- `go.mod` — minimum Go version, dependency policy

**Adopt every applicable convention.** Where siblings differ (e.g. firpty has `cmd/` + `publish`, skipstone has `e2e/` + stricter fmt/vet gates), pick the union of what fits the new repo's shape, not the lowest common denominator. Strict-by-default — match the strictest sibling unless the user opts out.

### Coverage gate is mandatory — do not ship a Makefile without it

A bare `go test -race ./...` Makefile is **not acceptable** for an extracted module. Every sibling repo enforces a coverage threshold (typically 100%) and fails the build below it. The new module must do the same from the first commit.

Concretely, the `Makefile` must have a `cover` target invoked by `all` that:

1. Runs `go test -coverprofile=coverage.out ./...`
2. Filters `.covignore` patterns out of the profile via `grep -v -E -f .covignore`.
3. Computes the total coverage percentage.
4. **Exits non-zero** if the percentage is below the threshold.

Copy the exact recipe from the strictest sibling (`/Users/kfet/dev/ai/skipstone/Makefile` or `/Users/kfet/dev/ai/firpty/Makefile`) — do not roll your own. If a `.covignore` is needed for files that genuinely cannot be covered (e.g. real PTY syscalls, real network exec), copy the sibling's `.covignore` shape too and document each exemption inline.

If you write the Makefile and `make all` passes without a coverage check having run, you have skipped this step. Re-check before declaring the extraction done.

## Idiomatic-Go pass — extraction is a fresh start, not a copy

The in-fir code was shaped by fir's surrounding code, conventions, and constraints. **Extraction is a one-time opportunity to make the package idiomatic Go on its own terms.** Treat the copy-paste as the *starting* state, not the finished state. After the files compile in the new repo and tests pass, do an explicit idiomatic-Go review pass before the first commit (or as a separate cleanup commit).

Lens to apply:

- **Package name & symbol stutter** — `pinoauth.PinoauthProvider` is wrong; rename until callers read cleanly. The package qualifier *is* the noun prefix.
- **Doc comments on every exported symbol**, starting with the symbol name. The package needs a `doc.go` opening with a one-paragraph package overview.
- **Testable examples** (`Example_xxx` funcs) — they show up on `pkg.go.dev` and double as compile-checked usage docs. At least one for the headline API.
- **Errors** — sentinel errors (`var ErrFoo = errors.New(...)`) or typed errors with `Is`/`As` support; wrap with `%w`; don't return bare `fmt.Errorf("...: %v", err)`.
- **Accept interfaces, return concrete types.** Interfaces belong near the consumer, not the producer. If the in-fir version has a `FooProvider` interface that only describes one struct, consider whether the interface needs to exist at all in the new repo.
- **Functional options** for constructors with more than ~3 optional knobs. Avoid sprawling config structs unless the option count is small and stable.
- **Zero values usable** where reasonable — a `var x Foo` should either work or panic clearly, not silently misbehave.
- **Context propagation** — every blocking/IO method takes `ctx context.Context` as the first arg. Background goroutines must respect cancellation.
- **No globals, no `init()`** beyond what the AGENTS.md explicitly allows. Test-swappable singletons (e.g. an HTTP client variable) are tolerated only with a comment justifying why.
- **File layout** — group by topic, not by visibility. One concept per file (`pkce.go`, `callback_server.go`), tests next to source.
- **Naming** — short receiver names, consistent across methods of the same type. No `this`/`self`. No Hungarian. No unnecessary getters.
- **Pre-Go-1.18 idioms** — replace `interface{}` with `any`. If the floor is Go 1.21+, prefer `slices`/`maps`/`cmp` stdlib packages over hand-rolled equivalents.
- **Concurrency safety** — document it (`// Foo is safe for concurrent use.` or `// Foo is not safe for concurrent use; …`). Don't make callers guess.
- **Static checks** — beyond `go vet`, run `gofmt -l`, `staticcheck`, `go vet -vettool=fieldalignment`. Add the relevant ones to the Makefile so they gate every build.

Don't change behaviour during this pass — semantics stay identical. This is shape, naming, and ergonomics only. If a behavioural improvement surfaces, file it as a separate post-extraction issue.

## Pre-flight checks

1. **Self-containment** — the candidate subtree must not import fir:
   ```bash
   grep -rn "kfet/fir" <subtree>/
   ```
   Empty → proceed. Non-empty → refactor inside fir first.
2. **`make all` green** on the source branch.
3. **Name availability**: `github.com/kfet/<name>` repo slot is 404, no module published on `pkg.go.dev/github.com/kfet/<name>`.

## Decisions to confirm with the user before writing files

- New repo name and module path (`github.com/kfet/<name>`).
- Package name — flat at module root is the precedent (`firpty.NewManager`, `skipstone.Client`); rename from the in-fir package name.
- License + copyright year (mirror sibling LICENSEs).
- Initial `VERSION` (typical: `0.0.1`).
- Symbol renames — usually keep, flag any that read awkwardly under the new package qualifier.

## Execution

## Fir-side switch — separate change, after the new repo is published

## Hand-back
