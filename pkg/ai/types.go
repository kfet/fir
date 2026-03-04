// Ported from: packages/ai/src/types.ts
// Upstream hash: 9e22d391
package ai

import (
	"context"
	"encoding/json"
	"slices"
)

// --- Role constants ---

const (
	RoleUser       = "user"
	RoleAssistant  = "assistant"
	RoleToolResult = "toolResult"
)

// --- Content type constants ---

const (
	ContentTypeText     = "text"
	ContentTypeThinking = "thinking"
	ContentTypeImage    = "image"
	ContentTypeToolCall = "toolCall"
)

// --- API and Provider identifiers ---

// Api identifies the wire protocol used to communicate with a provider.
type Api = string

// Known API constants.
const (
	ApiOpenAICompletions     Api = "openai-completions"
	ApiOpenAIResponses       Api = "openai-responses"
	ApiAzureOpenAIResponses  Api = "azure-openai-responses"
	ApiOpenAICodexResponses  Api = "openai-codex-responses"
	ApiAnthropicMessages     Api = "anthropic-messages"
	ApiBedrockConverseStream Api = "bedrock-converse-stream"
	ApiGoogleGenerativeAI    Api = "google-generative-ai"
	ApiGoogleGeminiCLI       Api = "google-gemini-cli"
	ApiGoogleVertex          Api = "google-vertex"
)

// Provider identifies a model hosting service.
type Provider = string

// Known provider constants.
const (
	ProviderAmazonBedrock        Provider = "amazon-bedrock"
	ProviderAnthropic            Provider = "anthropic"
	ProviderGoogle               Provider = "google"
	ProviderGoogleGeminiCLI      Provider = "google-gemini-cli"
	ProviderGoogleAntigravity    Provider = "google-antigravity"
	ProviderGoogleVertex         Provider = "google-vertex"
	ProviderOpenAI               Provider = "openai"
	ProviderAzureOpenAIResponses Provider = "azure-openai-responses"
	ProviderOpenAICodex          Provider = "openai-codex"
	ProviderGitHubCopilot        Provider = "github-copilot"
	ProviderXAI                  Provider = "xai"
	ProviderGroq                 Provider = "groq"
	ProviderCerebras             Provider = "cerebras"
	ProviderOpenRouter           Provider = "openrouter"
	ProviderVercelAIGateway      Provider = "vercel-ai-gateway"
	ProviderZAI                  Provider = "zai"
	ProviderMistral              Provider = "mistral"
	ProviderMinimax              Provider = "minimax"
	ProviderMinimaxCN            Provider = "minimax-cn"
	ProviderHuggingface          Provider = "huggingface"
	ProviderOpenCode             Provider = "opencode"
	ProviderKimiCoding           Provider = "kimi-coding"
)

// --- Thinking ---

// ThinkingLevel controls extended thinking/reasoning effort.
type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off" // disable thinking entirely (agent-layer concept)
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
)

// ThinkingBudgets maps thinking levels to token budgets (token-based providers only).
type ThinkingBudgets struct {
	Minimal *int `json:"minimal,omitempty"`
	Low     *int `json:"low,omitempty"`
	Medium  *int `json:"medium,omitempty"`
	High    *int `json:"high,omitempty"`
}

// BudgetForLevel returns the token budget for a given thinking level, or 0 if unset.
func (tb *ThinkingBudgets) BudgetForLevel(level ThinkingLevel) int {
	if tb == nil {
		return 0
	}
	var p *int
	switch level {
	case ThinkingMinimal:
		p = tb.Minimal
	case ThinkingLow:
		p = tb.Low
	case ThinkingMedium:
		p = tb.Medium
	case ThinkingHigh:
		p = tb.High
	default:
		return 0
	}
	if p == nil {
		return 0
	}
	return *p
}

// --- Transport ---

// Transport specifies the preferred transport for providers that support multiple transports.
type Transport string

const (
	TransportSSE       Transport = "sse"
	TransportWebSocket Transport = "websocket"
	TransportAuto      Transport = "auto"
)

// --- Cache ---

// CacheRetention specifies prompt cache retention preference.
type CacheRetention string

const (
	CacheNone  CacheRetention = "none"
	CacheShort CacheRetention = "short"
	CacheLong  CacheRetention = "long"
)

// --- Stream options ---

// AnthropicServerTool configures a server-side tool (e.g. web_search, code_execution)
// that Anthropic's API executes on behalf of the model.
type AnthropicServerTool struct {
	// Type is the tool type identifier, e.g. "web_search_20250305" or "code_execution_20250522".
	Type string `json:"type"`
	// Name is an optional display name override.
	Name string `json:"name,omitempty"`
	// MaxUses limits how many times the tool can be used per turn (0 = unlimited).
	MaxUses int `json:"max_uses,omitempty"`
	// AllowedDomains restricts web search to specific domains (web_search only, max 10).
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	// BlockedDomains prevents web search from using specific domains (web_search only, max 25).
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	// UserLocation sets geographic context for web search (web_search only).
	UserLocation *AnthropicUserLocation `json:"user_location,omitempty"`
}

// AnthropicUserLocation provides geographic context for web search.
type AnthropicUserLocation struct {
	Type      string `json:"type"` // always "approximate"
	City      string `json:"city,omitempty"`
	Region    string `json:"region,omitempty"`
	Country   string `json:"country,omitempty"`
	Timezone  string `json:"timezone,omitempty"`
}

// StreamOptions are the base options shared by all streaming calls.
type StreamOptions struct {
	Temperature     *float64              `json:"temperature,omitempty"`
	MaxTokens       *int                  `json:"maxTokens,omitempty"`
	ApiKey          string                `json:"apiKey,omitempty"`
	Transport       Transport             `json:"transport,omitempty"`
	CacheRetention  CacheRetention        `json:"cacheRetention,omitempty"`
	SessionID       string                `json:"sessionId,omitempty"`
	Headers         map[string]string     `json:"headers,omitempty"`
	MaxRetryDelayMs *int                  `json:"maxRetryDelayMs,omitempty"`
	ReasoningEffort ThinkingLevel         `json:"reasoningEffort,omitempty"`
	ToolChoice      string                `json:"toolChoice,omitempty"`
	Metadata        map[string]any        `json:"metadata,omitempty"`
	ServerTools     []AnthropicServerTool `json:"serverTools,omitempty"`
}

// SimpleStreamOptions extends StreamOptions with reasoning/thinking.
type SimpleStreamOptions struct {
	StreamOptions
	Reasoning       ThinkingLevel    `json:"reasoning,omitempty"`
	ThinkingBudgets *ThinkingBudgets `json:"thinkingBudgets,omitempty"`
}

// --- Content types ---

// TextContent represents a text block in a message.
type TextContent struct {
	Type          string `json:"type"` // always "text"
	Text          string `json:"text"`
	TextSignature string `json:"textSignature,omitempty"`
}

// ThinkingContent represents a thinking/reasoning block.
type ThinkingContent struct {
	Type              string `json:"type"` // always "thinking"
	Thinking          string `json:"thinking"`
	ThinkingSignature string `json:"thinkingSignature,omitempty"`
}

// ImageContent represents a base64-encoded image.
type ImageContent struct {
	Type     string `json:"type"` // always "image"
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// ToolCall represents a tool invocation by the assistant.
type ToolCall struct {
	Type             string         `json:"type"` // always "toolCall"
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments"`
	ThoughtSignature string         `json:"thoughtSignature,omitempty"`
}

// --- Usage ---

// UsageCost holds the cost breakdown for a single request.
type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

// Usage holds token counts and cost for a single request.
type Usage struct {
	Input       int       `json:"input"`
	Output      int       `json:"output"`
	CacheRead   int       `json:"cacheRead"`
	CacheWrite  int       `json:"cacheWrite"`
	TotalTokens int       `json:"totalTokens"`
	Cost        UsageCost `json:"cost"`
}

// ZeroUsage returns a Usage with all fields at zero.
func ZeroUsage() Usage {
	return Usage{}
}

// --- Stop reason ---

// StopReason indicates why the assistant stopped generating.
type StopReason string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)

// --- Messages ---

// UserMessage is a message from the user.
type UserMessage struct {
	Role      string `json:"role"`      // always "user"
	Content   any    `json:"content"`   // string or []UserContentBlock
	Timestamp int64  `json:"timestamp"` // Unix ms
}

// UserContentBlock is either TextContent or ImageContent in a UserMessage.
type UserContentBlock = any

// AssistantMessage is a message from the assistant.
type AssistantMessage struct {
	Role         string             `json:"role"` // always "assistant"
	Content      []AssistantContent `json:"content"`
	Api          Api                `json:"api"`
	Provider     Provider           `json:"provider"`
	Model        string             `json:"model"`
	Usage        Usage              `json:"usage"`
	StopReason   StopReason         `json:"stopReason"`
	ErrorMessage string             `json:"errorMessage,omitempty"`
	Timestamp    int64              `json:"timestamp"` // Unix ms
}

// AssistantContent is a discriminated union: TextContent | ThinkingContent | ToolCall.
// Exactly one of Text, Thinking, or ToolCall will be non-nil.
type AssistantContent struct {
	Text     *TextContent     `json:"text,omitempty"`
	Thinking *ThinkingContent `json:"thinking,omitempty"`
	ToolCall *ToolCall        `json:"toolCall,omitempty"`
}

// ContentType returns the type string for this content block.
func (c *AssistantContent) ContentType() string {
	if c.Text != nil {
		return ContentTypeText
	}
	if c.Thinking != nil {
		return ContentTypeThinking
	}
	if c.ToolCall != nil {
		return ContentTypeToolCall
	}
	return ""
}

// IsText returns true if this content block is a text block.
func (c *AssistantContent) IsText() bool { return c.Text != nil }

// IsThinking returns true if this content block is a thinking block.
func (c *AssistantContent) IsThinking() bool { return c.Thinking != nil }

// IsToolCall returns true if this content block is a tool call.
func (c *AssistantContent) IsToolCall() bool { return c.ToolCall != nil }

// MarshalJSON produces the flat JSON form matching the TS wire format:
//
//	{"type":"text","text":"hello"} or {"type":"thinking","thinking":"..."} or {"type":"toolCall","id":"...","name":"...","arguments":{...}}
func (c AssistantContent) MarshalJSON() ([]byte, error) {
	if c.Text != nil {
		return json.Marshal(c.Text)
	}
	if c.Thinking != nil {
		return json.Marshal(c.Thinking)
	}
	if c.ToolCall != nil {
		return json.Marshal(c.ToolCall)
	}
	return []byte("null"), nil
}

// UnmarshalJSON parses the flat JSON form and populates the correct pointer field.
func (c *AssistantContent) UnmarshalJSON(data []byte) error {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	switch probe.Type {
	case ContentTypeText:
		c.Text = &TextContent{}
		return json.Unmarshal(data, c.Text)
	case ContentTypeThinking:
		c.Thinking = &ThinkingContent{}
		return json.Unmarshal(data, c.Thinking)
	case ContentTypeToolCall:
		c.ToolCall = &ToolCall{}
		return json.Unmarshal(data, c.ToolCall)
	}
	return nil
}

// NewTextContent creates an AssistantContent wrapping a TextContent.
func NewTextContent(text string) AssistantContent {
	return AssistantContent{Text: &TextContent{Type: ContentTypeText, Text: text}}
}

// NewThinkingContent creates an AssistantContent wrapping a ThinkingContent.
func NewThinkingContent(thinking string) AssistantContent {
	return AssistantContent{Thinking: &ThinkingContent{Type: ContentTypeThinking, Thinking: thinking}}
}

// NewToolCallContent creates an AssistantContent wrapping a ToolCall.
func NewToolCallContent(id, name string, args map[string]any) AssistantContent {
	return AssistantContent{ToolCall: &ToolCall{Type: ContentTypeToolCall, ID: id, Name: name, Arguments: args}}
}

// ToolResultMessage is the result of a tool invocation.
type ToolResultMessage struct {
	Role       string              `json:"role"` // always "toolResult"
	ToolCallID string              `json:"toolCallId"`
	ToolName   string              `json:"toolName"`
	Content    []ToolResultContent `json:"content"`
	Details    any                 `json:"details,omitempty"`
	IsError    bool                `json:"isError"`
	Timestamp  int64               `json:"timestamp"` // Unix ms
}

// ToolResultContent is either a text or image result from a tool.
type ToolResultContent struct {
	Type     string `json:"type"` // "text" or "image"
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// IsText returns true if this is a text result.
func (c *ToolResultContent) IsText() bool { return c.Type == ContentTypeText }

// IsImage returns true if this is an image result.
func (c *ToolResultContent) IsImage() bool { return c.Type == ContentTypeImage }

// --- Message (union type) ---

// Message is a discriminated union of UserMessage, AssistantMessage, or ToolResultMessage.
// The Role field determines which type it is.
type Message struct {
	user       *UserMessage
	assistant  *AssistantMessage
	toolResult *ToolResultMessage
}

// Role returns the message role: "user", "assistant", or "toolResult".
func (m *Message) Role() string {
	if m.user != nil {
		return RoleUser
	}
	if m.assistant != nil {
		return RoleAssistant
	}
	if m.toolResult != nil {
		return RoleToolResult
	}
	return ""
}

// AsUser returns the UserMessage or nil.
func (m *Message) AsUser() *UserMessage { return m.user }

// AsAssistant returns the AssistantMessage or nil.
func (m *Message) AsAssistant() *AssistantMessage { return m.assistant }

// AsToolResult returns the ToolResultMessage or nil.
func (m *Message) AsToolResult() *ToolResultMessage { return m.toolResult }

// MarshalJSON serializes the inner message.
func (m Message) MarshalJSON() ([]byte, error) {
	if m.user != nil {
		return json.Marshal(m.user)
	}
	if m.assistant != nil {
		return json.Marshal(m.assistant)
	}
	if m.toolResult != nil {
		return json.Marshal(m.toolResult)
	}
	return []byte("null"), nil
}

// UnmarshalJSON deserializes based on the "role" field.
func (m *Message) UnmarshalJSON(data []byte) error {
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	switch probe.Role {
	case RoleUser:
		m.user = &UserMessage{}
		return json.Unmarshal(data, m.user)
	case RoleAssistant:
		m.assistant = &AssistantMessage{}
		return json.Unmarshal(data, m.assistant)
	case RoleToolResult:
		m.toolResult = &ToolResultMessage{}
		return json.Unmarshal(data, m.toolResult)
	}
	return nil
}

// NewUserMsg creates a Message wrapping a UserMessage.
func NewUserMsg(content any, timestamp int64) Message {
	return Message{user: &UserMessage{Role: RoleUser, Content: content, Timestamp: timestamp}}
}

// NewAssistantMsg creates a Message wrapping an AssistantMessage.
func NewAssistantMsg(msg AssistantMessage) Message {
	msg.Role = RoleAssistant
	return Message{assistant: &msg}
}

// NewToolResultMsg creates a Message wrapping a ToolResultMessage.
func NewToolResultMsg(msg ToolResultMessage) Message {
	msg.Role = RoleToolResult
	return Message{toolResult: &msg}
}

// --- Tool ---

// Tool defines a tool that the model can call.
// Parameters holds a JSON Schema describing the tool's input.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"` // JSON Schema object
}

// --- Context ---

// Context is the full context sent to a model: system prompt, messages, and available tools.
type Context struct {
	SystemPrompt string    `json:"systemPrompt,omitempty"`
	Messages     []Message `json:"messages"`
	Tools        []Tool    `json:"tools,omitempty"`
}

// --- Assistant message events (streaming) ---

// AssistantMessageEventType enumerates streaming event types.
type AssistantMessageEventType string

const (
	EventStart         AssistantMessageEventType = "start"
	EventTextStart     AssistantMessageEventType = "text_start"
	EventTextDelta     AssistantMessageEventType = "text_delta"
	EventTextEnd       AssistantMessageEventType = "text_end"
	EventThinkingStart AssistantMessageEventType = "thinking_start"
	EventThinkingDelta AssistantMessageEventType = "thinking_delta"
	EventThinkingEnd   AssistantMessageEventType = "thinking_end"
	EventToolcallStart AssistantMessageEventType = "toolcall_start"
	EventToolcallDelta AssistantMessageEventType = "toolcall_delta"
	EventToolcallEnd   AssistantMessageEventType = "toolcall_end"
	EventDone          AssistantMessageEventType = "done"
	EventError         AssistantMessageEventType = "error"
)

// AssistantMessageEvent represents a single streaming event from the assistant.
type AssistantMessageEvent struct {
	Type         AssistantMessageEventType `json:"type"`
	ContentIndex int                       `json:"contentIndex,omitempty"`
	Delta        string                    `json:"delta,omitempty"`    // for *_delta events
	Content      string                    `json:"content,omitempty"`  // for *_end events
	ToolCall     *ToolCall                 `json:"toolCall,omitempty"` // for toolcall_end
	Reason       StopReason                `json:"reason,omitempty"`   // for done/error
	Partial      *AssistantMessage         `json:"partial,omitempty"`  // snapshot for non-done events
	Message      *AssistantMessage         `json:"message,omitempty"`  // final message for done
	Error        *AssistantMessage         `json:"error,omitempty"`    // final message for error
}

// IsDone returns true if this event terminates the stream (done or error).
func (e *AssistantMessageEvent) IsDone() bool {
	return e.Type == EventDone || e.Type == EventError
}

// FinalMessage returns the completed AssistantMessage for done/error events.
func (e *AssistantMessageEvent) FinalMessage() *AssistantMessage {
	if e.Type == EventDone {
		return e.Message
	}
	if e.Type == EventError {
		return e.Error
	}
	return nil
}

// --- OpenAI compatibility settings ---

// ThinkingFormat controls how reasoning/thinking is sent to the provider.
type ThinkingFormat string

const (
	ThinkingFormatOpenAI ThinkingFormat = "openai"
	ThinkingFormatZAI    ThinkingFormat = "zai"
	ThinkingFormatQwen   ThinkingFormat = "qwen"
)

// MaxTokensField controls which JSON field name is used for max tokens.
type MaxTokensField string

const (
	MaxTokensFieldMaxCompletionTokens MaxTokensField = "max_completion_tokens"
	MaxTokensFieldMaxTokens           MaxTokensField = "max_tokens"
)

// OpenAICompletionsCompat holds compatibility overrides for OpenAI-compatible completions APIs.
type OpenAICompletionsCompat struct {
	SupportsStore                    *bool                 `json:"supportsStore,omitempty"`
	SupportsDeveloperRole            *bool                 `json:"supportsDeveloperRole,omitempty"`
	SupportsReasoningEffort          *bool                 `json:"supportsReasoningEffort,omitempty"`
	SupportsUsageInStreaming         *bool                 `json:"supportsUsageInStreaming,omitempty"`
	MaxTokensField                   MaxTokensField        `json:"maxTokensField,omitempty"`
	RequiresToolResultName           *bool                 `json:"requiresToolResultName,omitempty"`
	RequiresAssistantAfterToolResult *bool                 `json:"requiresAssistantAfterToolResult,omitempty"`
	RequiresThinkingAsText           *bool                 `json:"requiresThinkingAsText,omitempty"`
	RequiresMistralToolIds           *bool                 `json:"requiresMistralToolIds,omitempty"`
	ThinkingFormat                   ThinkingFormat        `json:"thinkingFormat,omitempty"`
	OpenRouterRouting                *OpenRouterRouting    `json:"openRouterRouting,omitempty"`
	VercelGatewayRouting             *VercelGatewayRouting `json:"vercelGatewayRouting,omitempty"`
	SupportsStrictMode               *bool                 `json:"supportsStrictMode,omitempty"`
}

// OpenAIResponsesCompat holds compatibility overrides for OpenAI Responses APIs.
type OpenAIResponsesCompat struct {
	// Reserved for future use.
}

// OpenRouterRouting configures OpenRouter provider routing preferences.
type OpenRouterRouting struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}

// VercelGatewayRouting configures Vercel AI Gateway routing preferences.
type VercelGatewayRouting struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}

// --- Model ---

// InputModality represents what a model can accept.
type InputModality = string

const (
	InputText  InputModality = "text"
	InputImage InputModality = "image"
)

// ModelCost holds per-million-token pricing.
type ModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// Model describes a specific LLM model and how to call it.
type Model struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Api           Api               `json:"api"`
	Provider      Provider          `json:"provider"`
	BaseURL       string            `json:"baseUrl"`
	Reasoning     bool              `json:"reasoning"`
	Input         []InputModality   `json:"input"`
	Cost          ModelCost         `json:"cost"`
	ContextWindow int               `json:"contextWindow"`
	MaxTokens     int               `json:"maxTokens"`
	Headers       map[string]string `json:"headers,omitempty"`
	Compat        any               `json:"compat,omitempty"` // *OpenAICompletionsCompat or *OpenAIResponsesCompat
}

// StreamFunction is the raw provider streaming function signature.
type StreamFunction func(ctx context.Context, model *Model, prompt Context, options *StreamOptions) *AssistantMessageEventStream

// SimpleStreamFunction is the simplified streaming function with reasoning support.
type SimpleStreamFunction func(ctx context.Context, model *Model, prompt Context, options *SimpleStreamOptions) *AssistantMessageEventStream

// SupportsImages returns true if the model accepts image input.
func (m *Model) SupportsImages() bool {
	return slices.Contains(m.Input, InputImage)
}

// GetOpenAICompletionsCompat returns the OpenAI completions compat settings, or nil.
func (m *Model) GetOpenAICompletionsCompat() *OpenAICompletionsCompat {
	if c, ok := m.Compat.(*OpenAICompletionsCompat); ok {
		return c
	}
	return nil
}

// GetOpenAIResponsesCompat returns the OpenAI responses compat settings, or nil.
func (m *Model) GetOpenAIResponsesCompat() *OpenAIResponsesCompat {
	if c, ok := m.Compat.(*OpenAIResponsesCompat); ok {
		return c
	}
	return nil
}
