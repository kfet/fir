package interactive

import (
	"context"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// answerElicit replies to successive prompts with answers ("<esc>" presses
// Escape).
func answerElicit(t *testing.T, answers ...string) {
	t.Helper()
	ch := make(chan string, len(answers))
	for _, a := range answers {
		ch <- a
	}
	elicitShown = func(in *tuicomp.Input) {
		a := <-ch
		go func() {
			if a == "<esc>" {
				in.OnEscape()
			} else {
				in.OnSubmit(a)
			}
		}()
	}
	t.Cleanup(func() { elicitShown = nil })
}

func formReq() *sdk.ElicitRequest {
	return &sdk.ElicitRequest{Params: &sdk.ElicitParams{Message: "Who?", RequestedSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"age":  map[string]any{"type": "integer"},
		},
		"required": []any{"name"},
	}}}
}

func TestElicitMCP_Form(t *testing.T) {
	tm := newTestMode(t)
	// name (required) empty → re-asked; then age invalid → re-asked; then skipped.
	answerElicit(t, "", "Ann", "x", "")
	res, err := tm.mode.ElicitMCP(context.Background(), formReq())
	require.NoError(t, err)
	assert.Equal(t, "accept", res.Action)
	assert.Equal(t, map[string]any{"name": "Ann"}, res.Content)
	assert.Contains(t, tm.mode.editorContainer.Children, tm.mode.editor)
}

func TestElicitMCP_EscapeDeclines(t *testing.T) {
	tm := newTestMode(t)
	answerElicit(t, "<esc>")
	res, err := tm.mode.ElicitMCP(context.Background(), formReq())
	require.NoError(t, err)
	assert.Equal(t, "decline", res.Action)
}

func TestElicitMCP_ContextCancel(t *testing.T) {
	tm := newTestMode(t)
	ctx, cancel := context.WithCancel(context.Background())
	elicitShown = func(*tuicomp.Input) { cancel() }
	t.Cleanup(func() { elicitShown = nil })
	res, err := tm.mode.ElicitMCP(ctx, formReq())
	require.NoError(t, err)
	assert.Equal(t, "cancel", res.Action)
}
