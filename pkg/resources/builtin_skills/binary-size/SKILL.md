---
name: binary-size
description: Analyze fir binary size per-package, identify bloat contributors, and check the size guard baseline.
override: true
---

# Binary Size Analysis

`make build` runs `check-size`: it fails above the 20 MB cap or at >5% growth vs `BINARY_SIZE_BASELINE` (bytes of the stripped `bin/fir`). Lower the baseline whenever you shrink the binary.

## Measure

```bash
make build && stat -c%s bin/fir 2>/dev/null || stat -f%z bin/fir
go install github.com/Zxilly/go-size-analyzer/cmd/gsa@latest   # once
go build -trimpath -o /tmp/fir-sym ./cmd/fir/                 # keep symbols for attribution
gsa -f text /tmp/fir-sym | head -40                           # per dependency
gsa -f json /tmp/fir-sym > /tmp/gsa.json
jq -r '.packages["github.com/kfet/fir"].subPackages|to_entries[]|"\(.value.size) \(.key)"' /tmp/gsa.json | sort -rn | head
go mod why -m <module>                                         # who pulls a heavy dep
```

Report the top entries; flag new ones above ~200 KB.

## Already trimmed — keep it that way

- Embedded skills, builtin extensions and SDK stubs are gzip tar archives served by `pkg/zfs` (`pkg/resources/builtin.tar.gz`, `pkg/extension/sdk/sdk.tar.gz`). `make tidy` (and so build/test/all) regenerates them via `go generate`; freshness tests fail if they are stale. Do not go back to raw `//go:embed` of directories.
- The model catalog is `pkg/ai/models_catalog.bin` (flate JSON), decoded lazily on first registry access. Never re-introduce Go struct literals for it.
- Self-update uses a small stdlib client (`pkg/update/release.go`), not go-selfupdate/go-github.
- Kept on purpose: goldmark (TUI markdown), MCP go-sdk and its segmentio JSON, yaml.v3, x/image. No UPX: it breaks notarization and triggers AV.
