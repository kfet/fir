# Go-Specific Decisions

## Dependencies (Minimal, No CGo)

| Purpose | Library | Notes |
|---|---|---|
| Terminal raw mode | `golang.org/x/term` | stdlib extension |
| Unicode width | `github.com/mattn/go-runewidth` | Essential for TUI |
| YAML | `gopkg.in/yaml.v3` | Settings files |
| Glob matching | `github.com/gobwas/glob` | .gitignore patterns |
| UUID | `github.com/google/uuid` | Session IDs |
| Diff | `github.com/sergi/go-diff` | edit-diff tool |
| Image resize | `golang.org/x/image` | Replaces photon-node WASM |
| JSON, HTTP, OS | stdlib | No external deps |

**No CGo** — pure Go for trivial cross-compilation to all targets.

## Binary Size

- `go build -ldflags="-s -w"` strips symbols (~30% size reduction)
- UPX compression if needed (further ~50% reduction)
- Target: <15MB stripped for ARM

## Image Handling

Two separate concerns:

### 1. Image resize for LLM context (read tool)

The TS version uses `photon-node` (Rust/WASM) to resize images before sending to LLMs
(Anthropic has a 5MB limit). In Go, use `image` stdlib + `golang.org/x/image` for
Lanczos resizing. This is pure Go, works everywhere, and is fast enough — resize
happens once per image read.

**No risk on Pi Zero** — this is CPU work that happens rarely and Go's image
libraries are well-optimized for ARM.

### 2. Terminal image display (TUI)

The TUI can render images inline using Kitty or iTerm2 graphics protocols.
This sends base64-encoded image data as terminal escape sequences.

**No risk on Pi Zero** — the Pi Zero itself won't have a graphical terminal
(you SSH into it). The image display simply won't activate because the terminal
capabilities detection will return `images: null` for an SSH session. The code
path is skipped entirely. If someone somehow uses Kitty over SSH with
forwarding, it works fine — the image bytes are just piped through.

### Summary: Image risk is NONE

Both image paths work fine on constrained hardware:
- Resize: pure Go, runs rarely, ~100ms on ARM for a typical image
- Display: auto-detected, gracefully disabled when unsupported

## Performance on Pi Zero

| Concern | Assessment |
|---|---|
| Startup | Go: ~10ms. Node.js: ~500ms+ |
| Memory idle | Go: ~10MB. Node.js: ~80MB+ |
| Memory streaming | Go: ~30-50MB peak. Processes chunks as they arrive |
| CPU during streaming | I/O bound — single core is fine |
| TUI rendering | Differential renderer minimizes writes — good for slow SSH |
| Tool execution | Subprocess I/O — same on any platform |

## Error Handling

TS uses `throw` + `try/catch`. Go uses explicit error returns.

Pattern for ported code:
```go
// TS: const result = await someOp(); // throws
// Go:
result, err := someOp()
if err != nil {
    return fmt.Errorf("someOp: %w", err)
}
```

## Concurrency

The agent loop is inherently sequential (prompt → stream → tools → repeat)
but has concurrent concerns:
- Steering messages arriving during streaming → channel + select
- Abort signals → `context.WithCancel`
- TUI input handling → separate goroutine
- Background bash processes → goroutine + process group management
