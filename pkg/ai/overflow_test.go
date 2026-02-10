package ai

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func makeErrorMsg(errorMsg string) *AssistantMessage {
	return &AssistantMessage{
		Role:         RoleAssistant,
		Api:          ApiAnthropicMessages,
		Provider:     ProviderAnthropic,
		Model:        "claude-3",
		StopReason:   StopReasonError,
		ErrorMessage: errorMsg,
	}
}

func TestIsContextOverflow_Anthropic(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("prompt is too long: 213462 tokens > 200000 maximum"), 0))
}

func TestIsContextOverflow_OpenAI(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("Your input exceeds the context window of this model"), 0))
}

func TestIsContextOverflow_Google(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)"), 0))
}

func TestIsContextOverflow_XAI(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("This model's maximum prompt length is 131072 but the request contains 537812 tokens"), 0))
}

func TestIsContextOverflow_Groq(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("Please reduce the length of the messages or completion"), 0))
}

func TestIsContextOverflow_OpenRouter(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("This endpoint's maximum context length is 128000 tokens"), 0))
}

func TestIsContextOverflow_LlamaCpp(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("the request exceeds the available context size"), 0))
}

func TestIsContextOverflow_LMStudio(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("greater than the context length"), 0))
}

func TestIsContextOverflow_Copilot(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("prompt token count of 100000 exceeds the limit of 65536"), 0))
}

func TestIsContextOverflow_MiniMax(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("context window exceeds limit"), 0))
}

func TestIsContextOverflow_KimiCoding(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("Your request exceeded model token limit: 128000"), 0))
}

func TestIsContextOverflow_Cerebras(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("400 status code (no body)"), 0))
	assert.True(t, IsContextOverflow(makeErrorMsg("413 (no body)"), 0))
}

func TestIsContextOverflow_GenericFallbacks(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("context_length_exceeded"), 0))
	assert.True(t, IsContextOverflow(makeErrorMsg("context length exceeded"), 0))
	assert.True(t, IsContextOverflow(makeErrorMsg("too many tokens"), 0))
	assert.True(t, IsContextOverflow(makeErrorMsg("token limit exceeded"), 0))
}

func TestIsContextOverflow_NotOverflow(t *testing.T) {
	assert.False(t, IsContextOverflow(makeErrorMsg("Rate limit exceeded"), 0))
	assert.False(t, IsContextOverflow(makeErrorMsg("429 status code (no body)"), 0))
}

func TestIsContextOverflow_SilentOverflow(t *testing.T) {
	msg := &AssistantMessage{
		Role:       RoleAssistant,
		StopReason: StopReasonStop,
		Usage:      Usage{Input: 150000, CacheRead: 10000},
	}
	assert.True(t, IsContextOverflow(msg, 128000))
}

func TestIsContextOverflow_SilentOverflow_WithinWindow(t *testing.T) {
	msg := &AssistantMessage{
		Role:       RoleAssistant,
		StopReason: StopReasonStop,
		Usage:      Usage{Input: 50000, CacheRead: 10000},
	}
	assert.False(t, IsContextOverflow(msg, 128000))
}

func TestIsContextOverflow_NilMessage(t *testing.T) {
	assert.False(t, IsContextOverflow(nil, 0))
}

func TestIsContextOverflow_NoContextWindow(t *testing.T) {
	msg := &AssistantMessage{StopReason: StopReasonStop, Usage: Usage{Input: 999999}}
	assert.False(t, IsContextOverflow(msg, 0))
}

func TestGetOverflowPatterns(t *testing.T) {
	patterns := GetOverflowPatterns()
	assert.True(t, len(patterns) > 0)
	patterns[0] = nil
	assert.NotNil(t, GetOverflowPatterns()[0])
}

func TestIsContextOverflow_Bedrock(t *testing.T) {
	assert.True(t, IsContextOverflow(makeErrorMsg("input is too long for requested model"), 0))
}
