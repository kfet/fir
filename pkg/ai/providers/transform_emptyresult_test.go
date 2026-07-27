package providers

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func testModelForFloor() *ai.Model {
	return &ai.Model{Provider: "anthropic", API: ai.ApiAnthropicMessages, ID: "claude-test"}
}

func toolResultText(t *testing.T, msg ai.Message) string {
	t.Helper()
	tr := msg.AsToolResult()
	if tr == nil {
		t.Fatalf("expected a tool result message, got role %q", msg.Role())
	}
	var parts []string
	for _, c := range tr.Content {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "")
}

// A tool result with no content blocks must reach the model as an explicit
// out-of-band marker, never as the empty string.
func TestTransformMessages_FloorsEmptyToolResult(t *testing.T) {
	msgs := []ai.Message{
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role:       ai.RoleToolResult,
			ToolCallID: "tc-1",
			ToolName:   "some_mcp_tool",
			Content:    nil,
		}),
	}

	out := TransformMessages(msgs, testModelForFloor(), nil)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	got := toolResultText(t, out[0])
	if !strings.Contains(got, "no output") {
		t.Fatalf("empty tool result was not floored, got %q", got)
	}
	if strings.Contains(got, "failed") {
		t.Fatalf("success result must not be described as failed, got %q", got)
	}
}

// Whitespace-only content is just as unreadable as no content.
func TestTransformMessages_FloorsWhitespaceOnlyToolResult(t *testing.T) {
	msgs := []ai.Message{
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role:       ai.RoleToolResult,
			ToolCallID: "tc-2",
			ToolName:   "some_tool",
			Content:    []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "  \n\t "}},
		}),
	}

	got := toolResultText(t, TransformMessages(msgs, testModelForFloor(), nil)[0])
	if !strings.Contains(got, "no output") {
		t.Fatalf("whitespace-only tool result was not floored, got %q", got)
	}
}

// An empty *error* result says so, so the model does not read a failure as a
// successful no-op.
func TestTransformMessages_FloorsEmptyErrorToolResult(t *testing.T) {
	msgs := []ai.Message{
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role:       ai.RoleToolResult,
			ToolCallID: "tc-3",
			ToolName:   "some_tool",
			IsError:    true,
		}),
	}

	got := toolResultText(t, TransformMessages(msgs, testModelForFloor(), nil)[0])
	if !strings.Contains(got, "failed") {
		t.Fatalf("empty error result must be marked as a failure, got %q", got)
	}
}

// Real content must pass through byte-for-byte.
func TestTransformMessages_LeavesNonEmptyToolResultAlone(t *testing.T) {
	msgs := []ai.Message{
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role:       ai.RoleToolResult,
			ToolCallID: "tc-4",
			ToolName:   "some_tool",
			Content:    []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "hello"}},
		}),
	}

	if got := toolResultText(t, TransformMessages(msgs, testModelForFloor(), nil)[0]); got != "hello" {
		t.Fatalf("non-empty tool result was modified: %q", got)
	}
}

// The floor is LLM-bound only: the caller's (persisted) message must not be
// mutated, so session history keeps recording the truthful empty result.
func TestTransformMessages_FloorDoesNotMutateInput(t *testing.T) {
	orig := ai.ToolResultMessage{
		Role:       ai.RoleToolResult,
		ToolCallID: "tc-5",
		ToolName:   "some_tool",
		Content:    nil,
	}
	msgs := []ai.Message{ai.NewToolResultMsg(orig)}

	_ = TransformMessages(msgs, testModelForFloor(), nil)

	if tr := msgs[0].AsToolResult(); len(tr.Content) != 0 {
		t.Fatalf("persisted tool result was mutated: %+v", tr.Content)
	}
}

// An image-bearing result is not blank even with no text.
func TestTransformMessages_DoesNotFloorImageOnlyToolResult(t *testing.T) {
	model := testModelForFloor()
	model.Input = append(model.Input, ai.InputImage)
	msgs := []ai.Message{
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role:       ai.RoleToolResult,
			ToolCallID: "tc-6",
			ToolName:   "screenshot",
			Content: []ai.ToolResultContent{{
				Type:     ai.ContentTypeImage,
				Data:     "aGVsbG8=",
				MimeType: "image/png",
			}},
		}),
	}

	tr := TransformMessages(msgs, model, nil)[0].AsToolResult()
	if len(tr.Content) != 1 || !tr.Content[0].IsImage() {
		t.Fatalf("image-only tool result must pass through untouched, got %+v", tr.Content)
	}
}
