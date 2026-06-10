// Package ai is the fir-resident AI surface: provider catalog,
// generated model registry, fir-side OAuth registry, and the API
// transport registry. The portable type primitives (Message, Tool,
// Model, Usage, Context, streaming primitives, etc.) now live in the
// external module github.com/kfet/ai and are re-exported here so the
// existing 150+ call sites in fir don't need to change.
//
// See docs/design/ai-agent-extraction.md (Phase 5).
package ai

import core "github.com/kfet/ai"

// ---------------------------------------------------------------
// Type re-exports from core
// ---------------------------------------------------------------

type (
	Api                         = core.API
	Provider                    = core.Provider
	ThinkingLevel               = core.ThinkingLevel
	ThinkingBudgets             = core.ThinkingBudgets
	Transport                   = core.Transport
	CacheRetention              = core.CacheRetention
	AnthropicServerTool         = core.AnthropicServerTool
	AnthropicUserLocation       = core.AnthropicUserLocation
	AnthropicCompaction         = core.AnthropicCompaction
	ProviderResponse            = core.ProviderResponse
	StreamOptions               = core.StreamOptions
	SimpleStreamOptions         = core.SimpleStreamOptions
	TextContent                 = core.TextContent
	ThinkingContent             = core.ThinkingContent
	ImageContent                = core.ImageContent
	ServerContent               = core.ServerContent
	ToolCall                    = core.ToolCall
	UsageCost                   = core.UsageCost
	Usage                       = core.Usage
	StopReason                  = core.StopReason
	UserMessage                 = core.UserMessage
	AssistantMessage            = core.AssistantMessage
	AssistantContent            = core.AssistantContent
	ToolResultMessage           = core.ToolResultMessage
	ToolResultContent           = core.ToolResultContent
	Message                     = core.Message
	Tool                        = core.Tool
	Context                     = core.Context
	AssistantMessageEventType   = core.AssistantMessageEventType
	AssistantMessageEvent       = core.AssistantMessageEvent
	ThinkingFormat              = core.ThinkingFormat
	MaxTokensField              = core.MaxTokensField
	OpenAICompletionsCompat     = core.OpenAICompletionsCompat
	OpenAIResponsesCompat       = core.OpenAIResponsesCompat
	AnthropicMessagesCompat     = core.AnthropicMessagesCompat
	OpenRouterRouting           = core.OpenRouterRouting
	OpenRouterMaxPrice          = core.OpenRouterMaxPrice
	VercelGatewayRouting        = core.VercelGatewayRouting
	InputModality               = core.InputModality
	ModelCost                   = core.ModelCost
	Model                       = core.Model
	StreamFunction              = core.StreamFunction
	SimpleStreamFunction        = core.SimpleStreamFunction
	AssistantMessageEventStream = core.AssistantMessageEventStream
)

// UserContentBlock was dropped from core (github.com/kfet/ai) in v0.1.0; it
// was always just `any`. Preserved here as a fir-local alias so existing
// fir/extension references to ai.UserContentBlock keep compiling.
type UserContentBlock = any

// ---------------------------------------------------------------
// Function re-exports from core
// ---------------------------------------------------------------

var (
	ZeroUsage                      = core.ZeroUsage
	NewTextContent                 = core.NewTextContent
	NewThinkingContent             = core.NewThinkingContent
	NewToolCallContent             = core.NewToolCallContent
	NewServerContent               = core.NewServerContent
	NewUserMsg                     = core.NewUserMsg
	NewAssistantMsg                = core.NewAssistantMsg
	NewToolResultMsg               = core.NewToolResultMsg
	RenderToolResultMeta           = core.RenderToolResultMeta
	BoolPtr                        = core.BoolPtr
	IntPtr                         = core.IntPtr
	NewAssistantMessageEventStream = core.NewAssistantMessageEventStream
)

// ---------------------------------------------------------------
// Constant re-exports from core
// ---------------------------------------------------------------

const (
	RoleUser       = core.RoleUser
	RoleAssistant  = core.RoleAssistant
	RoleToolResult = core.RoleToolResult
)

const (
	ContentTypeText     = core.ContentTypeText
	ContentTypeThinking = core.ContentTypeThinking
	ContentTypeImage    = core.ContentTypeImage
	ContentTypeToolCall = core.ContentTypeToolCall
	ContentTypeServer   = core.ContentTypeServer
)

const (
	ApiOpenAICompletions     = core.APIOpenAICompletions
	ApiOpenAIResponses       = core.APIOpenAIResponses
	ApiAzureOpenAIResponses  = core.APIAzureOpenAIResponses
	ApiOpenAICodexResponses  = core.APIOpenAICodexResponses
	ApiAnthropicMessages     = core.APIAnthropicMessages
	ApiBedrockConverseStream = core.APIBedrockConverseStream
	ApiGoogleGenerativeAI    = core.APIGoogleGenerativeAI
	ApiGoogleVertex          = core.APIGoogleVertex
)

const (
	ProviderAmazonBedrock        = core.ProviderAmazonBedrock
	ProviderAnthropic            = core.ProviderAnthropic
	ProviderGoogle               = core.ProviderGoogle
	ProviderGoogleVertex         = core.ProviderGoogleVertex
	ProviderOpenAI               = core.ProviderOpenAI
	ProviderAzureOpenAIResponses = core.ProviderAzureOpenAIResponses
	ProviderOpenAICodex          = core.ProviderOpenAICodex
	ProviderGitHubCopilot        = core.ProviderGitHubCopilot
	ProviderXAI                  = core.ProviderXAI
	ProviderGroq                 = core.ProviderGroq
	ProviderCerebras             = core.ProviderCerebras
	ProviderOpenRouter           = core.ProviderOpenRouter
	ProviderVercelAIGateway      = core.ProviderVercelAIGateway
	ProviderZAI                  = core.ProviderZAI
	ProviderMistral              = core.ProviderMistral
	ProviderMinimax              = core.ProviderMinimax
	ProviderMinimaxCN            = core.ProviderMinimaxCN
	ProviderMoonshotAI           = core.ProviderMoonshotAI
	ProviderMoonshotAICN         = core.ProviderMoonshotAICN
	ProviderDeepseek             = core.ProviderDeepseek
	ProviderFireworks            = core.ProviderFireworks
	ProviderHuggingface          = core.ProviderHuggingface
	ProviderOpenCode             = core.ProviderOpenCode
	ProviderOpenCodeGo           = core.ProviderOpenCodeGo
	ProviderKimiCoding           = core.ProviderKimiCoding
	ProviderCloudflareWorkersAI  = core.ProviderCloudflareWorkersAI
	ProviderCloudflareAIGateway  = core.ProviderCloudflareAIGateway
	ProviderXiaomi               = core.ProviderXiaomi
	ProviderPoe                  = core.ProviderPoe
)

const (
	ThinkingOff     = core.ThinkingOff
	ThinkingMinimal = core.ThinkingMinimal
	ThinkingLow     = core.ThinkingLow
	ThinkingMedium  = core.ThinkingMedium
	ThinkingHigh    = core.ThinkingHigh
	ThinkingXHigh   = core.ThinkingXHigh
	ThinkingMax     = core.ThinkingMax
)

const (
	TransportSSE             = core.TransportSSE
	TransportWebSocket       = core.TransportWebSocket
	TransportWebSocketCached = core.TransportWebSocketCached
	TransportAuto            = core.TransportAuto
)

const (
	CacheNone  = core.CacheNone
	CacheShort = core.CacheShort
	CacheLong  = core.CacheLong
)

const (
	StopReasonStop    = core.StopReasonStop
	StopReasonLength  = core.StopReasonLength
	StopReasonToolUse = core.StopReasonToolUse
	StopReasonError   = core.StopReasonError
	StopReasonAborted = core.StopReasonAborted
)

const (
	EventStart         = core.EventStart
	EventTextStart     = core.EventTextStart
	EventTextDelta     = core.EventTextDelta
	EventTextEnd       = core.EventTextEnd
	EventThinkingStart = core.EventThinkingStart
	EventThinkingDelta = core.EventThinkingDelta
	EventThinkingEnd   = core.EventThinkingEnd
	EventToolcallStart = core.EventToolcallStart
	EventToolcallDelta = core.EventToolcallDelta
	EventToolcallEnd   = core.EventToolcallEnd
	EventDone          = core.EventDone
	EventError         = core.EventError
)

const (
	ThinkingFormatOpenAI      = core.ThinkingFormatOpenAI
	ThinkingFormatOpenRouter  = core.ThinkingFormatOpenRouter
	ThinkingFormatDeepSeek    = core.ThinkingFormatDeepSeek
	ThinkingFormatZAI         = core.ThinkingFormatZAI
	ThinkingFormatQwen        = core.ThinkingFormatQwen
	ThinkingFormatQwenChatTpl = core.ThinkingFormatQwenChatTpl
)

const (
	MaxTokensFieldMaxCompletionTokens = core.MaxTokensFieldMaxCompletionTokens
	MaxTokensFieldMaxTokens           = core.MaxTokensFieldMaxTokens
)

const (
	InputText  = core.InputText
	InputImage = core.InputImage
)
