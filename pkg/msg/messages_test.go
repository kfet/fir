package msg

import (
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

func TestBashExecutionToText_Basic(t *testing.T) {
	msg := &BashExecutionMessage{
		Role:    "bashExecution",
		Command: "echo hello",
		Output:  "hello",
	}
	text := BashExecutionToText(msg)
	if !strings.Contains(text, "echo hello") {
		t.Error("should contain command")
	}
	if !strings.Contains(text, "hello") {
		t.Error("should contain output")
	}
}

func TestBashExecutionToText_NoOutput(t *testing.T) {
	msg := &BashExecutionMessage{
		Role:    "bashExecution",
		Command: "true",
	}
	text := BashExecutionToText(msg)
	if !strings.Contains(text, "(no output)") {
		t.Errorf("text = %q, want contains '(no output)'", text)
	}
}

func TestBashExecutionToText_ExitCode(t *testing.T) {
	code := 42
	msg := &BashExecutionMessage{
		Role:     "bashExecution",
		Command:  "false",
		ExitCode: &code,
	}
	text := BashExecutionToText(msg)
	if !strings.Contains(text, "42") {
		t.Errorf("text = %q, want contains '42'", text)
	}
}

func TestBashExecutionToText_Cancelled(t *testing.T) {
	msg := &BashExecutionMessage{
		Role:      "bashExecution",
		Command:   "sleep 100",
		Cancelled: true,
	}
	text := BashExecutionToText(msg)
	if !strings.Contains(text, "cancelled") {
		t.Errorf("text = %q, want contains 'cancelled'", text)
	}
}

func TestBashExecutionToText_Truncated(t *testing.T) {
	msg := &BashExecutionMessage{
		Role:           "bashExecution",
		Command:        "cat bigfile",
		Output:         "...",
		Truncated:      true,
		FullOutputPath: "/tmp/out.log",
	}
	text := BashExecutionToText(msg)
	if !strings.Contains(text, "truncated") || !strings.Contains(text, "/tmp/out.log") {
		t.Errorf("text = %q, want truncation notice", text)
	}
}

func TestConvertToLLM_UserMessage(t *testing.T) {
	msgs := []agent.AgentMessage{
		agent.NewAgentMessage(ai.NewUserMsg("hello", time.Now().UnixMilli())),
	}
	result, err := ConvertToLLM(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].Role() != "user" {
		t.Errorf("role = %q, want user", result[0].Role())
	}
}

func TestConvertToLLM_BashExecution(t *testing.T) {
	msgs := []agent.AgentMessage{
		{Custom: &BashExecutionMessage{
			Role:      "bashExecution",
			Command:   "ls",
			Output:    "file.txt",
			Timestamp: time.Now().UnixMilli(),
		}},
	}
	result, err := ConvertToLLM(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].Role() != "user" {
		t.Errorf("role = %q, want user", result[0].Role())
	}
}

func TestConvertToLLM_BashExcluded(t *testing.T) {
	msgs := []agent.AgentMessage{
		{Custom: &BashExecutionMessage{
			Role:               "bashExecution",
			Command:            "ls",
			ExcludeFromContext: true,
		}},
	}
	result, err := ConvertToLLM(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 messages for excluded bash, got %d", len(result))
	}
}

func TestConvertToLLM_BranchSummary(t *testing.T) {
	msg := CreateBranchSummaryMessage("summary text", "from-123", time.Now())
	result, err := ConvertToLLM([]agent.AgentMessage{msg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].Role() != "user" {
		t.Errorf("role = %q, want user", result[0].Role())
	}
}

func TestConvertToLLM_CompactionSummary(t *testing.T) {
	msg := CreateCompactionSummaryMessage("compact summary", 5000, time.Now())
	result, err := ConvertToLLM([]agent.AgentMessage{msg})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
}

func TestConvertToLLM_MixedMessages(t *testing.T) {
	now := time.Now().UnixMilli()
	msgs := []agent.AgentMessage{
		agent.NewAgentMessage(ai.NewUserMsg("hello", now)),
		{Custom: &BashExecutionMessage{
			Role:      "bashExecution",
			Command:   "ls",
			Output:    "out",
			Timestamp: now,
		}},
		agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
			Role:       "assistant",
			Content:    []ai.AssistantContent{ai.NewTextContent("hi")},
			Api:        ai.ApiAnthropicMessages,
			Provider:   ai.ProviderAnthropic,
			Model:      "test",
			StopReason: ai.StopReasonStop,
			Timestamp:  now,
		})),
	}
	result, err := ConvertToLLM(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
}
