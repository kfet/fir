# firext — Go SDK for fir extensions

A minimal, dependency-free Go SDK for writing [fir](../../../..) extensions.

A fir extension is any executable that speaks the fir JSON-RPC 2.0 protocol over
stdio (one JSON object per line). This module implements that protocol so you
only write handlers. The wire protocol is specified in
[`docs/extension-protocol.md`](../../../../docs/extension-protocol.md).

This is a **nested Go module** with no dependencies, so importing it does not
pull in fir's dependency tree.

## Install

```sh
go get github.com/kfet/fir/pkg/extension/sdk/go/firext
```

## Quickstart

```go
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kfet/fir/pkg/extension/sdk/go/firext"
)

func main() {
	app := firext.New("hello-go")

	app.Tool(firext.ToolSpec{
		Name:        "wordcount",
		Description: "Count words in a string",
		Parameters:  firext.Object(firext.Props{"text": firext.Str("input")}, "text"),
	}, func(p json.RawMessage, ctx *firext.Context) (*firext.ToolResult, error) {
		var in struct{ Text string `json:"text"` }
		_ = json.Unmarshal(p, &in)
		return firext.Text(fmt.Sprintf("%d words", len(strings.Fields(in.Text)))), nil
	})

	app.On("session_start", func(p json.RawMessage, ctx *firext.Context) {
		_ = ctx.SetStatus("hello-go ready")
	})

	app.Run()
}
```

## API surface

| Construct | Method |
|-----------|--------|
| Tool | `app.Tool(spec, handler)` |
| Slash command | `app.Command(name, desc, handler)` |
| Event | `app.On("session_start", handler)` |
| Hook | `app.Hook("tool_call", handler)` (subscribes `hook/tool_call`) |
| Run loop | `app.Run()` |

Handlers receive a `*firext.Context` for callbacks into fir:

`Notify`, `Exec`, `SetStatus`, `SetSessionName`, `SendUserMessage`,
`SendMessage`, `PutObservable`, `SideQuery`, `SetSessionData`,
`GetSessionData`, `GetSessionID`, and `Call` (escape hatch for any other
method in the protocol).

Schema helpers — `Str`, `Int`, `Bool`, `Object(props, required...)`, `Props` —
keep you from hand-building `map[string]any` JSON Schema.

## Concurrency model

`Run()` dispatches each inbound request/notification in its own goroutine, so a
handler may make outbound calls (e.g. `ctx.Exec`) — whose responses arrive on
the same stdio stream — without deadlocking. Writes are serialized internally.

## Demo extension

See [`examples/demo`](examples/demo). It registers two tools (one pure, one
that calls back via `ctx.Exec`), a slash command, a `session_start` event
handler, and a `tool_call` hook that blocks `rm -rf /`.

### Running the demo in fir

fir discovers a **sub-directory** extension by finding an executable entry
point named `main` (among others); the directory name becomes the extension
name. Build the demo into an extensions directory:

```sh
# Project-local:
go build -o .fir/extensions/go-demo/main ./examples/demo

# Or global:
go build -o ~/.config/fir/extensions/go-demo/main ./examples/demo
```

fir will load `go-demo` on next start. (For development, the provided
`examples/demo/main.sh` wrapper `go run`s the package on each launch — symlink
the demo dir into an extensions dir and fir uses `main.sh` as the entry point.)

## Testing

```sh
go test ./...            # unit + integration (compiles the demo, drives the wire protocol)
go test -short ./...     # skip the build-and-run integration test
```
