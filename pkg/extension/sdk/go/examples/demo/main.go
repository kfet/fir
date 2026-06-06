// Command demo is a reference fir extension written in Go using the firext SDK.
//
// It mirrors the spirit of the Python demo (pkg/resources/builtin_extensions/
// demo.py): it registers a tool, a slash command, an event handler and a hook,
// and exercises a couple of extension→fir callbacks.
//
// Build it into an extensions directory so fir can discover it:
//
//	go build -o ~/.config/fir/extensions/go-demo/main ./examples/demo
//
// fir resolves a sub-directory extension by looking for an executable named
// `main` (among others), so the directory name `go-demo` becomes the extension
// name. Alternatively use the provided main.sh wrapper which `go run`s this
// package on each launch (handy during development).
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kfet/fir/pkg/extension/sdk/go/firext"
)

func main() {
	app := firext.New("go-demo")

	// --- a tool ---
	app.Tool(firext.ToolSpec{
		Name:        "go_wordcount",
		Description: "Count the words in a string (Go demo extension).",
		Parameters: firext.Object(firext.Props{
			"text": firext.Str("Input text to count words in"),
		}, "text"),
		DisplayHint: &firext.DisplayHint{
			TitleArgs: []firext.TitleArgSpec{{Name: "text", Style: "accent"}},
		},
	}, func(p json.RawMessage, ctx *firext.Context) (*firext.ToolResult, error) {
		var in struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(p, &in); err != nil {
			return firext.ErrorText("bad params: " + err.Error()), nil
		}
		n := len(strings.Fields(in.Text))
		return firext.Text(fmt.Sprintf("%d word(s)", n)), nil
	})

	// --- a tool that calls back into fir (exec) ---
	app.Tool(firext.ToolSpec{
		Name:        "go_uname",
		Description: "Run `uname -a` via fir's exec callback and return the result.",
		Parameters:  firext.Object(firext.Props{}),
	}, func(p json.RawMessage, ctx *firext.Context) (*firext.ToolResult, error) {
		res, err := ctx.Exec("uname", "-a")
		if err != nil {
			return nil, err
		}
		return firext.Text(strings.TrimSpace(res.Stdout)), nil
	})

	// --- a slash command ---
	app.Command("go-demo", "Show a greeting from the Go demo extension", func(args []string, ctx *firext.Context) (*firext.CommandResult, error) {
		msg := "hello from the Go demo extension"
		if len(args) > 0 {
			msg = "hello, " + strings.Join(args, " ") + " — from Go"
		}
		return &firext.CommandResult{Message: msg, PrintResponse: true}, nil
	})

	// --- an event handler: set a footer status on session start ---
	app.On("session_start", func(p json.RawMessage, ctx *firext.Context) {
		_ = ctx.SetStatus("go-demo: ready")
		_ = ctx.PutObservable("current", "ready", "Go demo extension loaded")
	})

	// --- a hook: block bash commands containing 'rm -rf /' ---
	app.Hook("tool_call", func(p json.RawMessage, ctx *firext.Context) (*firext.HookDecision, error) {
		var in struct {
			ToolName string `json:"tool_name"`
			Params   struct {
				Command string `json:"command"`
			} `json:"params"`
		}
		_ = json.Unmarshal(p, &in)
		if in.ToolName == "bash" && strings.Contains(in.Params.Command, "rm -rf /") {
			return &firext.HookDecision{Block: true, Reason: "go-demo: refusing to run 'rm -rf /'"}, nil
		}
		return nil, nil // allow
	})

	if err := app.Run(); err != nil {
		// Errors here mean stdin closed unexpectedly; nothing to do but exit.
		_ = err
	}
}
