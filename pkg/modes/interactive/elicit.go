package interactive

import (
	"context"
	"fmt"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/modes/interactive/components"
	itheme "github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/session"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
	"github.com/kfet/tui"
)

// elicitMu serialises MCP elicitation prompts: each one takes over the
// editor area until answered.
var elicitMu sync.Mutex

// elicitShown, when set (tests), receives each prompt's input once shown.
var elicitShown func(*tuicomp.Input)

// elicitAnswer is one prompt's outcome.
type elicitAnswer struct {
	value  string
	cancel bool // Escape
}

// ElicitMCP answers an MCP server's elicitation request in the editor area.
// Form mode asks for each field in turn; URL mode shows the URL and opens it
// on Enter. Escape declines. It blocks until the user answers or ctx ends.
func (m *InteractiveMode) ElicitMCP(ctx context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
	elicitMu.Lock()
	defer elicitMu.Unlock()
	msg := mcp.ElicitMessage(req)

	if req.Params != nil && req.Params.Mode == "url" {
		ans, err := m.elicitPrompt(ctx, []string{msg, req.Params.URL}, "Press Enter to open this URL, Escape to decline.", "")
		if err != nil {
			return &sdk.ElicitResult{Action: "cancel"}, nil
		}
		if ans.cancel {
			return &sdk.ElicitResult{Action: "decline"}, nil
		}
		if err := session.OpenBrowser(req.Params.URL); err != nil {
			m.showMessage("Open this URL: " + req.Params.URL)
		}
		return &sdk.ElicitResult{Action: "accept"}, nil
	}

	fields, err := mcp.ElicitFields(req)
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		ans, err := m.elicitPrompt(ctx, []string{msg}, "Press Enter to accept, Escape to decline.", "")
		if err != nil {
			return &sdk.ElicitResult{Action: "cancel"}, nil
		}
		if ans.cancel {
			return &sdk.ElicitResult{Action: "decline"}, nil
		}
		return &sdk.ElicitResult{Action: "accept", Content: map[string]any{}}, nil
	}

	content := map[string]any{}
	problem := ""
	for i := 0; i < len(fields); {
		f := fields[i]
		lines := []string{msg, fmt.Sprintf("(%d/%d) %s", i+1, len(fields), elicitFieldPrompt(f))}
		if problem != "" {
			lines = append(lines, problem)
		}
		hint := "Enter to continue, Escape to decline."
		if !f.Required {
			hint = "Optional: Enter on empty skips. Escape declines."
		}
		def := ""
		if f.Default != nil {
			def = fmt.Sprint(f.Default)
		}
		ans, err := m.elicitPrompt(ctx, lines, hint, def)
		if err != nil {
			return &sdk.ElicitResult{Action: "cancel"}, nil
		}
		if ans.cancel {
			return &sdk.ElicitResult{Action: "decline"}, nil
		}
		v, ok, perr := mcp.ParseElicitValue(f, ans.value)
		switch {
		case perr != nil:
			problem = perr.Error()
			continue
		case !ok && f.Required:
			problem = f.Label() + " is required."
			continue
		case ok:
			content[f.Name] = v
		}
		problem = ""
		i++
	}
	return &sdk.ElicitResult{Action: "accept", Content: content}, nil
}

func elicitFieldPrompt(f mcp.ElicitField) string {
	var b strings.Builder
	b.WriteString(f.Label())
	if f.Description != "" {
		b.WriteString(" — " + f.Description)
	}
	switch {
	case len(f.Enum) > 0:
		b.WriteString(" [" + strings.Join(f.Enum, " | ") + "]")
	case f.Type == "boolean":
		b.WriteString(" [yes | no]")
	case f.Type != "string":
		b.WriteString(" (" + f.Type + ")")
	}
	return b.String()
}

// elicitPrompt shows lines and an input in the editor area and waits for
// Enter or Escape. It returns ctx's error if ctx ends first.
func (m *InteractiveMode) elicitPrompt(ctx context.Context, lines []string, hint, initial string) (elicitAnswer, error) {
	ch := make(chan elicitAnswer, 1)
	restore := func() {
		m.editorContainer.Clear()
		m.editorContainer.AddChild(m.editor)
		m.ui.SetFocus(m.editor)
		m.ui.RequestRender(false)
	}
	t := itheme.GetTheme()
	container := &tui.Container{}
	container.AddChild(components.NewDynamicBorder(nil))
	container.AddChild(tuicomp.NewText(t.Fg("warning", "MCP server request (from the server, not the user)"), 1, 0, nil))
	for _, l := range lines {
		container.AddChild(tuicomp.NewText(l, 1, 0, nil))
	}
	container.AddChild(tuicomp.NewText(t.Fg("muted", hint), 1, 0, nil))
	input := tuicomp.NewInput()
	if initial != "" {
		input.SetValue(initial)
	}
	var once sync.Once
	finish := func(a elicitAnswer) {
		once.Do(func() {
			restore()
			ch <- a
		})
	}
	input.OnSubmit = func(v string) { finish(elicitAnswer{value: v}) }
	input.OnEscape = func() { finish(elicitAnswer{cancel: true}) }
	container.AddChild(input)
	container.AddChild(tuicomp.NewSpacer(1))
	container.AddChild(components.NewDynamicBorder(nil))

	m.editorContainer.Clear()
	m.editorContainer.AddChild(container)
	m.ui.SetFocus(input)
	m.ui.RequestRender(true)
	if elicitShown != nil {
		elicitShown(input)
	}

	select {
	case a := <-ch:
		return a, nil
	case <-ctx.Done():
		once.Do(restore)
		return elicitAnswer{}, ctx.Err()
	}
}
