# Contributing to fir

Thanks for your interest in fir.

## Project stance

fir is, first and foremost, **a personal project published in the open.** It is
built and maintained by one person, primarily for that person's own use, and it
is largely self-hosting — fir is used to develop fir. The source is public so
others can read it, learn from it, run it, and build on top of it, not because
it is run as a community project with a roadmap and a review rota.

Please set expectations accordingly: issues and pull requests are read on a
best-effort basis, there is no response-time guarantee, and design decisions
ultimately rest with the maintainer. None of this is meant to discourage you —
it's meant to be honest about what this repository is.

## The intended extension point

**The extension and plugin surface is where fir is designed to be extended.**
If you want to add a capability — a new tool, an integration, a workflow — the
supported, first-class way to do that is a fir *extension* (an external process
speaking the JSON-RPC bridge protocol), not a change to fir's core. Extensions
live outside this repository, can be written in any language (a Python SDK is
provided), and are loaded from your own config or installed as packages. See the
`Extensions` section of the [README](README.md) and the extension protocol docs
for how to build one.

Building on the extension surface means your work is not gated on a core PR being
accepted, and it keeps working across fir upgrades. This is the path most likely
to succeed.

## Pull requests

Because this is a personal project, some kinds of change are much more likely to
land than others.

**Welcome:**

- Clear, well-scoped **bug fixes** with a description of the problem and, where
  practical, a test.
- **Security fixes** — but please read [SECURITY.md](SECURITY.md) first and
  report the underlying vulnerability privately before opening a public PR.
- **Documentation** corrections and clarifications.
- Small, self-contained fixes to obvious defects.

**Likely to be declined (or left to sit):**

- Large refactors, architectural rewrites, or sweeping style changes.
- New core features that would fit better as an extension (see above).
- Changes that add dependencies, broaden the maintenance surface, or introduce
  configuration knobs for narrow use cases.
- Anything that changes behaviour without a clear, self-contained rationale.

If you're thinking about a non-trivial change, **open an issue first** to check
whether it's likely to be accepted before investing time in a PR. A PR that
lands out of the blue as a large diff will usually be declined simply because
the maintenance cost falls on one person.

## Practical notes

- fir is Go. Before submitting, make sure the Go build and tests pass:
  `go build ./... && go vet ./... && go test ./...` (or `make all`, which also
  runs the Python SDK tests, cross-compiles, checks licenses, and enforces the
  coverage gate — see `.covignore`).
- Keep the diff minimal and focused on one thing.
- There is **no API stability guarantee** for the `pkg/` tree — it is v0.x and
  internal-shaped even though it is importable. See the README note before
  depending on it.

## The coverage ratchet

`make coverage` (part of `make all`) runs two tiers over one profile. Tier 1
is a hard `-min=100` over everything *not* matched by `.covignore`; tier 2 is
a whole-tree floor (`COVERAGE_FLOOR` in the Makefile) that catches rot in the
excluded part. Tier 1's 100% never moves.

`.covignore` is a debt ledger, not a carve-out. Section 1 is structural
(entrypoints, TUI rendering, generated tables); section 2 is ordinary code
that simply is not covered yet, and every line in it exists to be deleted.
Sealing a package is one commit: cover it to 100%, delete its line, watch the
gate pass. Going the other way — adding a line — needs a justification in the
commit message, and is usually the wrong move: prefer pushing the unmockable
part behind a narrow wrapper and excluding only the wrapper. The patterns are
non-recursive on purpose, so a **new package is gated at 100% from its first
commit** unless someone deliberately writes it into the ledger. `BACKLOG.md`
carries the promotion queue, cheapest first.

## License

By contributing, you agree that your contributions are licensed under the same
[MIT License](LICENSE) that covers the project.
