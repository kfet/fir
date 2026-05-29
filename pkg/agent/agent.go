// Ported from: packages/agent/src/agent.ts
// Upstream hash: 036bde0a
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/ratelimit"
	firlog "github.com/kfet/fir/pkg/log"
)

// simplePromptRetryBackoffs controls retries of a single-shot SimplePrompt /
// SideQuery LLM call. Its length is the number of EXTRA attempts (so total
// attempts = len+1). Retries cover two transient classes that would otherwise
// dead-end an advisor/side query with no recourse (there is no agent loop or
// tool result to react to here):
//
//   - transport/stream errors (connection reset, broken pipe, EOF, …)
//   - degenerate generations that carry no usable content — e.g. a thinking
//     model that emits an empty/thinking-only block with no text. A re-roll of
//     an idempotent side query almost always returns text.
//
// A genuine model/API rejection (400, auth, context-length) is NOT retried.
// Package var so tests can shorten the backoffs.
var simplePromptRetryBackoffs = []time.Duration{
	300 * time.Millisecond,
	1 * time.Second,
}

// DefaultConvertToLLM keeps only LLM-compatible messages.
func DefaultConvertToLLM(messages []AgentMessage) ([]ai.Message, error) {
	var out []ai.Message
	for _, m := range messages {
		role := m.Role()
		if role == "user" || role == "assistant" || role == "toolResult" {
			out = append(out, m.Message)
		}
	}
	return out, nil
}

// AgentOptions configures an Agent.
type AgentOptions struct {
	// InitialState overrides the default AgentState fields.
	InitialState *AgentState

	// ConvertToLLM converts AgentMessages to LLM Messages before each call.
	// Default filters to user/assistant/toolResult.
	ConvertToLLM func(messages []AgentMessage) ([]ai.Message, error)

	// TransformContext is applied before ConvertToLLM for context pruning etc.
	TransformContext func(ctx context.Context, messages []AgentMessage) ([]AgentMessage, error)

	// SteeringMode: "all" = send all steering messages at once, "one-at-a-time" = one per turn.
	SteeringMode string

	// FollowUpMode: "all" = send all follow-up messages at once, "one-at-a-time" = one per turn.
	FollowUpMode string

	// StreamFn is a custom stream function. Default uses ai.StreamSimple.
	StreamFn StreamFn

	// SessionID is forwarded to LLM providers for session-based caching.
	SessionID string

	// GetApiKey resolves an API key dynamically for each LLM call.
	GetApiKey func(provider string) (string, error)

	// ThinkingBudgets sets custom token budgets for thinking levels.
	ThinkingBudgets *ai.ThinkingBudgets

	// Transport is the preferred transport for providers that support multiple transports.
	Transport ai.Transport

	// MaxRetryDelayMs caps how long to wait for server-requested retries.
	MaxRetryDelayMs *int

	// ServerTools configures Anthropic server-side tools (web search, code execution, etc.).
	ServerTools []ai.AnthropicServerTool

	// Compaction configures Anthropic server-side context compaction.
	Compaction *ai.AnthropicCompaction

	// OnPayload is an optional callback to inspect or replace provider payloads before sending.
	// Return nil to keep the original payload unchanged.
	OnPayload func(payload any, model *ai.Model) any
}

// Agent orchestrates the agent loop with state management and event dispatch.
type Agent struct {
	mu sync.Mutex

	state AgentState

	listeners       map[int]func(AgentEvent)
	nextListenerID  int
	abortCancel     context.CancelFunc
	convertToLLM    func([]AgentMessage) ([]ai.Message, error)
	transformCtx    func(context.Context, []AgentMessage) ([]AgentMessage, error)
	steeringQueue   []AgentMessage
	followUpQueue   []AgentMessage
	steeringMode    string
	followUpMode    string
	streamFn        StreamFn
	sessionID       string
	getApiKey       func(string) (string, error)
	thinkingBudgets *ai.ThinkingBudgets
	transport       ai.Transport
	maxRetryDelayMs *int
	serverTools     []ai.AnthropicServerTool
	compaction      *ai.AnthropicCompaction
	onPayload       func(any, *ai.Model) any

	// idleCh is closed when the agent finishes processing.
	idleCh chan struct{}
}

// NewAgent creates a new Agent with the given options.
func NewAgent(opts AgentOptions) *Agent {
	a := &Agent{
		state: AgentState{
			SystemPrompt:     "",
			Model:            nil,
			ThinkingLevel:    ThinkingOff,
			Tools:            NewToolSet(),
			Messages:         nil,
			IsStreaming:      false,
			StreamMessage:    nil,
			PendingToolCalls: make(map[string]bool),
		},
		listeners:    make(map[int]func(AgentEvent)),
		convertToLLM: DefaultConvertToLLM,
		steeringMode: "one-at-a-time",
		followUpMode: "one-at-a-time",
		idleCh:       nil,
	}

	if opts.InitialState != nil {
		s := opts.InitialState
		if s.SystemPrompt != "" {
			a.state.SystemPrompt = s.SystemPrompt
		}
		if s.Model != nil {
			a.state.Model = s.Model
		}
		if s.ThinkingLevel != "" {
			a.state.ThinkingLevel = s.ThinkingLevel
		}
		if s.Tools != nil {
			a.state.Tools = s.Tools
		}
		if s.Messages != nil {
			a.state.Messages = s.Messages
		}
	}

	if opts.ConvertToLLM != nil {
		a.convertToLLM = opts.ConvertToLLM
	}
	if opts.TransformContext != nil {
		a.transformCtx = opts.TransformContext
	}
	if opts.SteeringMode != "" {
		a.steeringMode = opts.SteeringMode
	}
	if opts.FollowUpMode != "" {
		a.followUpMode = opts.FollowUpMode
	}
	if opts.StreamFn != nil {
		a.streamFn = opts.StreamFn
	}
	if opts.SessionID != "" {
		a.sessionID = opts.SessionID
	}
	if opts.GetApiKey != nil {
		a.getApiKey = opts.GetApiKey
	}
	if opts.ThinkingBudgets != nil {
		a.thinkingBudgets = opts.ThinkingBudgets
	}
	if opts.Transport != "" {
		a.transport = opts.Transport
	} else {
		a.transport = ai.TransportAuto
	}
	if opts.MaxRetryDelayMs != nil {
		a.maxRetryDelayMs = opts.MaxRetryDelayMs
	}
	if len(opts.ServerTools) > 0 {
		a.serverTools = opts.ServerTools
	}
	if opts.Compaction != nil {
		a.compaction = opts.Compaction
	}
	if opts.OnPayload != nil {
		a.onPayload = opts.OnPayload
	}

	return a
}

// State returns the current agent state. The caller should not modify it.
func (a *Agent) State() AgentState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// GetSessionID returns the current session ID.
func (a *Agent) GetSessionID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionID
}

// SetSessionID sets the session ID for provider caching.
func (a *Agent) SetSessionID(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionID = id
}

// GetThinkingBudgets returns the current thinking budgets.
func (a *Agent) GetThinkingBudgets() *ai.ThinkingBudgets {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.thinkingBudgets
}

// SetThinkingBudgets sets custom thinking budgets.
func (a *Agent) SetThinkingBudgets(tb *ai.ThinkingBudgets) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.thinkingBudgets = tb
}

// GetTransport returns the current preferred transport.
func (a *Agent) GetTransport() ai.Transport {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.transport
}

// SetTransport sets the preferred transport.
func (a *Agent) SetTransport(t ai.Transport) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.transport = t
}

// GetMaxRetryDelayMs returns the current max retry delay.
func (a *Agent) GetMaxRetryDelayMs() *int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.maxRetryDelayMs
}

// SetMaxRetryDelayMs sets the max retry delay.
func (a *Agent) SetMaxRetryDelayMs(ms *int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.maxRetryDelayMs = ms
}

// SetStreamFn overrides the stream function used for LLM calls.
func (a *Agent) SetStreamFn(fn StreamFn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.streamFn = fn
}

// Subscribe registers an event listener. Returns an unsubscribe function.
func (a *Agent) Subscribe(fn func(AgentEvent)) func() {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := a.nextListenerID
	a.nextListenerID++
	a.listeners[id] = fn
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.listeners, id)
	}
}

// SetSystemPrompt sets the system prompt.
func (a *Agent) SetSystemPrompt(prompt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.SystemPrompt = prompt
}

// SetModel sets the model.
func (a *Agent) SetModel(m *ai.Model) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Model = m
}

// SetThinkingLevel sets the thinking level.
func (a *Agent) SetThinkingLevel(level ThinkingLevel) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.ThinkingLevel = level
}

// SetSteeringMode sets the steering mode.
func (a *Agent) SetSteeringMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steeringMode = mode
}

// GetSteeringMode returns the current steering mode.
func (a *Agent) GetSteeringMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.steeringMode
}

// SetFollowUpMode sets the follow-up mode.
func (a *Agent) SetFollowUpMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.followUpMode = mode
}

// GetFollowUpMode returns the current follow-up mode.
func (a *Agent) GetFollowUpMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.followUpMode
}

// SetServerTools updates the Anthropic server-side tools (web search, code execution, etc.).
func (a *Agent) SetServerTools(tools []ai.AnthropicServerTool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.serverTools = tools
}

// SetCompaction updates the Anthropic server-side compaction settings.
func (a *Agent) SetCompaction(c *ai.AnthropicCompaction) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.compaction = c
}

// UpdateTools applies fn to the agent's ToolSet under the agent lock.
// This is the safe way to mutate tools — the callback sees the current
// state and all changes are atomic. No stale snapshots, no clobbering.
func (a *Agent) UpdateTools(fn func(ts *ToolSet)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.Tools == nil {
		a.state.Tools = NewToolSet()
	}
	fn(a.state.Tools)
}

// ReplaceMessages replaces all messages.
func (a *Agent) ReplaceMessages(msgs []AgentMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Messages = make([]AgentMessage, len(msgs))
	copy(a.state.Messages, msgs)
}

// AppendMessage appends a message.
func (a *Agent) AppendMessage(m AgentMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Messages = append(a.state.Messages, m)
}

// ClearMessages clears all messages.
func (a *Agent) ClearMessages() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Messages = nil
}

// Steer queues a steering message to interrupt the agent mid-run.
func (a *Agent) Steer(m AgentMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steeringQueue = append(a.steeringQueue, m)
}

// FollowUp queues a follow-up message for after the agent finishes.
func (a *Agent) FollowUp(m AgentMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.followUpQueue = append(a.followUpQueue, m)
}

// FollowUpQueueLen returns the number of queued follow-up messages.
func (a *Agent) FollowUpQueueLen() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.followUpQueue)
}

// PeekFollowUpQueue returns a snapshot of the follow-up queue without modifying it.
func (a *Agent) PeekFollowUpQueue() []AgentMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := make([]AgentMessage, len(a.followUpQueue))
	copy(cp, a.followUpQueue)
	return cp
}

// RemoveFollowUp removes and returns the message at the given 0-based index.
// Returns the message and true if found, zero value and false otherwise.
func (a *Agent) RemoveFollowUp(index int) (AgentMessage, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if index < 0 || index >= len(a.followUpQueue) {
		return AgentMessage{}, false
	}
	msg := a.followUpQueue[index]
	a.followUpQueue = append(a.followUpQueue[:index], a.followUpQueue[index+1:]...)
	return msg, true
}

// ClearSteeringQueue clears the steering queue.
func (a *Agent) ClearSteeringQueue() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steeringQueue = nil
}

// ClearFollowUpQueue clears the follow-up queue.
func (a *Agent) ClearFollowUpQueue() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.followUpQueue = nil
}

// GetAndClearFollowUpQueue atomically returns and clears the follow-up queue.
func (a *Agent) GetAndClearFollowUpQueue() []AgentMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	q := a.followUpQueue
	a.followUpQueue = nil
	return q
}

// ClearAllQueues clears both steering and follow-up queues.
func (a *Agent) ClearAllQueues() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steeringQueue = nil
	a.followUpQueue = nil
}

// HasQueuedMessages returns true if there are any queued messages.
func (a *Agent) HasQueuedMessages() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.steeringQueue) > 0 || len(a.followUpQueue) > 0
}

// Abort cancels the current streaming operation.
func (a *Agent) Abort() {
	a.mu.Lock()
	cancel := a.abortCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// WaitForIdle blocks until the agent finishes processing.
func (a *Agent) WaitForIdle() {
	a.mu.Lock()
	ch := a.idleCh
	a.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

// Reset clears all state except system prompt and model.
func (a *Agent) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Messages = nil
	a.state.IsStreaming = false
	a.state.StreamMessage = nil
	a.state.PendingToolCalls = make(map[string]bool)
	a.state.Error = ""
	a.steeringQueue = nil
	a.followUpQueue = nil
}

// Prompt sends a text prompt to the agent.
func (a *Agent) Prompt(input string) error {
	msg := AgentMessage{
		Message: ai.NewUserMsg(input, time.Now().UnixMilli()),
	}
	return a.PromptMessages([]AgentMessage{msg})
}

// PromptMessages sends agent messages as a prompt.
func (a *Agent) PromptMessages(messages []AgentMessage) error {
	a.mu.Lock()
	if a.state.IsStreaming {
		a.mu.Unlock()
		return fmt.Errorf("agent is already processing a prompt; use Steer() or FollowUp() to queue messages")
	}
	if a.state.Model == nil {
		a.mu.Unlock()
		return fmt.Errorf("no model configured")
	}
	a.mu.Unlock()

	a.runLoop(messages, false)
	return nil
}

// Continue resumes from the current context (retries, queued messages).
func (a *Agent) Continue() error {
	a.mu.Lock()
	if a.state.IsStreaming {
		a.mu.Unlock()
		return fmt.Errorf("agent is already processing; wait for completion before continuing")
	}
	msgs := a.state.Messages
	if len(msgs) == 0 {
		a.mu.Unlock()
		return fmt.Errorf("no messages to continue from")
	}

	last := msgs[len(msgs)-1]
	a.mu.Unlock()

	if last.Role() == "assistant" {
		// Try steering queue first
		steering := a.dequeueSteeringMessages()
		if len(steering) > 0 {
			a.runLoop(steering, true)
			return nil
		}
		// Try follow-up queue
		followUp := a.dequeueFollowUpMessages()
		if len(followUp) > 0 {
			a.runLoop(followUp, false)
			return nil
		}
		// No queued messages — the assistant was likely interrupted mid-stream.
		// Queue a synthetic "continue" as a steering message (invisible to the
		// user) and kick off the loop with an empty prompt so the steering
		// poll picks it up on the first iteration.
		continueMsg := NewAgentMessage(ai.NewUserMsg("continue", 0))
		a.mu.Lock()
		a.steeringQueue = append(a.steeringQueue, continueMsg)
		a.mu.Unlock()
		a.runLoop(nil, false)
		return nil
	}

	a.runLoop(nil, false)
	return nil
}

// SimplePromptOptions overrides per-call settings for SimplePrompt.
// All fields are optional; nil/empty values inherit the agent's current state.
//
// Used to support "advisor" patterns where a side query is routed to a
// different (typically stronger) model than the executor agent is running on.
type SimplePromptOptions struct {
	// Model overrides the LLM model used for this call.
	// When non-nil, the provider is implied by Model.Api and the appropriate
	// API key is resolved via the agent's GetApiKey for that provider.
	Model *ai.Model

	// Reasoning overrides the thinking/reasoning effort.
	// Empty string ("") inherits the agent's current ThinkingLevel.
	// Use ai.ThinkingOff explicitly to disable thinking for this call.
	Reasoning ai.ThinkingLevel
}

// SimplePrompt makes a single-turn LLM call with the given messages.
// See SimplePromptStream for the full contract — this is a thin wrapper
// that drops streaming events on the floor.
//
// NO-COMPACTION CONTRACT: SimplePrompt MUST NOT trigger auto-compaction, ever.
// See SimplePromptStream's contract for the same guarantee.
func (a *Agent) SimplePrompt(ctx context.Context, messages []AgentMessage, opts *SimplePromptOptions) (string, error) {
	text, _, err := a.SimplePromptStream(ctx, messages, opts, nil)
	return text, err
}

// SimplePromptStream makes a single-turn LLM call with the given messages and
// forwards each agent event to onEvent as it is emitted. Behavior is
// otherwise identical to SimplePrompt:
//
//   - Reuses the agent's model, streamFn, api key resolution, and transport
//     config but sends no tools, runs no agent loop, and does not modify the
//     agent's state. The caller provides the full message list.
//   - Safe to call concurrently while the agent loop is running.
//
// onEvent may be nil — events are then discarded. Callbacks are invoked
// synchronously on the same goroutine that drains the stream, so callers
// must keep their work cheap (the next event blocks until the callback
// returns).
//
// Returns the rendered text, the final assistant message (or nil on error),
// and any error. On "no usable content" the error string includes a
// per-block summary so callers can diagnose redacted/empty responses
// without losing the raw message.
//
// NO-COMPACTION CONTRACT: SimplePromptStream MUST NOT trigger auto-compaction.
// This is guaranteed by two design choices that must be preserved:
//  1. The AgentLoopConfig built here intentionally omits the Compaction field,
//     so no server-side compaction is requested.
//  2. The events channel is a private, local channel drained synchronously by
//     this function — events never reach AgentSession.checkAutoCompaction.
//
// Do not forward these events to the session or add Compaction to the config.
func (a *Agent) SimplePromptStream(ctx context.Context, messages []AgentMessage, opts *SimplePromptOptions, onEvent func(AgentEvent)) (string, *ai.AssistantMessage, error) {
	a.mu.Lock()
	model := a.state.Model
	systemPrompt := a.state.SystemPrompt
	reasoning := ai.ThinkingOff
	if a.state.ThinkingLevel != ThinkingOff {
		reasoning = ToAIThinkingLevel(a.state.ThinkingLevel)
	}
	streamFn := a.streamFn
	convertToLLM := a.convertToLLM
	getApiKey := a.getApiKey
	transport := a.transport
	sessionID := a.sessionID
	thinkingBudgets := a.thinkingBudgets
	maxRetryDelayMs := a.maxRetryDelayMs
	a.mu.Unlock()

	// Apply per-call overrides.
	if opts != nil {
		if opts.Model != nil {
			model = opts.Model
		}
		if opts.Reasoning != "" {
			reasoning = opts.Reasoning
		}
	}

	if model == nil {
		return "", nil, fmt.Errorf("no model selected")
	}

	if streamFn == nil {
		streamFn = func(m *ai.Model, c ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			return ai.StreamSimple(ctx, ai.DefaultRegistry, m, c, opts)
		}
	}

	if convertToLLM == nil {
		return "", nil, fmt.Errorf("no ConvertToLLM function configured")
	}

	config := &AgentLoopConfig{
		Model:           model,
		Reasoning:       reasoning,
		SessionID:       sessionID,
		Transport:       transport,
		ThinkingBudgets: thinkingBudgets,
		MaxRetryDelayMs: maxRetryDelayMs,
		ConvertToLLM:    convertToLLM,
		GetApiKey:       getApiKey,
	}

	// Single-shot streaming with bounded retry. Unlike the agent loop, this
	// path has no tools, no steering, and no auto-resume — a single bad roll
	// (transport reset, or a degenerate thinking-only/empty response from a
	// thinking model) would otherwise dead-end the call with no recourse.
	// Retry those transient classes; surface everything else immediately.
	//
	// Refinement for the no-usable-content case: rather than a blind re-roll
	// (which can repeat a thinking-only / budget-exhausted outcome), the retry
	// after such a result forces thinking OFF, so the model is structurally
	// required to emit a text answer. The original reasoning level is used for
	// the first attempt and for transport-error retries.
	baseReasoning := config.Reasoning
	forceNoThinking := false
	var (
		msg     *ai.AssistantMessage
		text    string
		lastErr error
	)
	maxAttempts := len(simplePromptRetryBackoffs) + 1
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if forceNoThinking {
			config.Reasoning = ai.ThinkingOff
		} else {
			config.Reasoning = baseReasoning
		}

		// A fresh context per attempt ensures a failed partial is never
		// replayed to the model on the next try.
		msg = streamSinglePrompt(ctx, systemPrompt, messages, config, streamFn, onEvent)

		switch {
		case msg == nil:
			lastErr = fmt.Errorf("no response from model")
			forceNoThinking = false
		case msg.ErrorMessage != "":
			lastErr = fmt.Errorf("%s", msg.ErrorMessage)
			// Only transport/stream errors are worth a re-roll; a genuine
			// model/API rejection (400, auth, context-length) is terminal.
			if !ratelimit.IsRetryableError(msg.ErrorMessage) {
				return "", msg, lastErr
			}
			// Transport error — keep the original reasoning level on retry.
			forceNoThinking = false
		default:
			var renderErr error
			text, _, renderErr = renderSimplePromptContent(msg.Content)
			if renderErr == nil {
				return text, msg, nil
			}
			// Degenerate empty / thinking-only response. Surface the stop
			// reason (stop vs length tells us "model chose silence" vs
			// "budget exhausted") and force thinking OFF on the next attempt
			// so the model must emit text instead of risking a repeat.
			lastErr = fmt.Errorf("%w (stop_reason=%s)", renderErr, msg.StopReason)
			forceNoThinking = true
		}

		if attempt < maxAttempts-1 {
			firlog.Warn("SimplePrompt transient result, retrying",
				"attempt", attempt+1, "err", lastErr)
			select {
			case <-ctx.Done():
				return text, msg, ctx.Err()
			case <-time.After(simplePromptRetryBackoffs[attempt]):
			}
		}
	}

	return text, msg, lastErr
}

// streamSinglePrompt runs one tool-less streaming LLM call and returns the
// final assistant message. Events are forwarded to onEvent (if non-nil) on the
// calling goroutine — preserving order without extra synchronization and the
// NO-COMPACTION contract (events never reach the session). A fresh AgentContext
// is built per call so a failed partial from a prior attempt is never replayed.
func streamSinglePrompt(
	ctx context.Context,
	systemPrompt string,
	messages []AgentMessage,
	config *AgentLoopConfig,
	streamFn StreamFn,
	onEvent func(AgentEvent),
) *ai.AssistantMessage {
	agentCtx := &AgentContext{
		SystemPrompt: systemPrompt,
		Messages:     messages,
		Tools:        NewToolSet(), // empty — no tools
	}

	events := make(chan AgentEvent, 64)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for ev := range events {
			if onEvent != nil {
				onEvent(ev)
			}
		}
	}()

	msg := streamAssistantResponse(ctx, agentCtx, config, streamFn, events)
	close(events)
	<-doneCh
	return msg
}

// BlockSummary is a compact description of a single content block from an
// assistant message. It carries enough to diagnose "empty" / redacted
// responses (where a thinking block with sig_len>0 and len=0 is the smoking
// gun) without keeping the raw payload around.
type BlockSummary struct {
	Type   string `json:"type"`
	Len    int    `json:"len"`
	SigLen int    `json:"sig_len,omitempty"`
}

// SummarizeBlocks produces a BlockSummary slice for the given content.
// Exported so session-layer code can attach the same summary to a
// SideQueryResult on success.
func SummarizeBlocks(content []ai.AssistantContent) []BlockSummary {
	out := make([]BlockSummary, 0, len(content))
	for _, c := range content {
		switch {
		case c.Text != nil:
			out = append(out, BlockSummary{Type: "text", Len: len(c.Text.Text)})
		case c.Thinking != nil:
			out = append(out, BlockSummary{
				Type:   "thinking",
				Len:    len(c.Thinking.Thinking),
				SigLen: len(c.Thinking.ThinkingSignature),
			})
		case c.ToolCall != nil:
			args, _ := json.Marshal(c.ToolCall.Arguments)
			out = append(out, BlockSummary{Type: "toolCall", Len: len(args)})
		case c.Server != nil:
			out = append(out, BlockSummary{Type: "server", Len: len(c.Server.Raw)})
		}
	}
	return out
}

func formatBlockSummary(blocks []BlockSummary) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "thinking":
			parts = append(parts, fmt.Sprintf("thinking(th=%d,sig=%d)", b.Len, b.SigLen))
		default:
			parts = append(parts, fmt.Sprintf("%s(len=%d)", b.Type, b.Len))
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// renderSimplePromptContent flattens an assistant message's content blocks
// into a single string suitable for SimplePrompt / SideQuery callers, and
// returns a BlockSummary slice describing each input block.
//
// Text blocks are emitted verbatim. Thinking blocks are surfaced with a
// `[think] ...` prefix (useful signal even though we can't replay them).
// ToolCall blocks become `[tool <name> <compact-json-args>]` markers — the
// caller has no tools to execute, but knowing the advisor *wanted* to run a
// tool is still informative. Blocks are joined with newlines in source order.
//
// Returns an error when there is no usable content at all (no text, no
// thinking, no tool call). The error includes the block summary so callers
// can distinguish "advisor said nothing" from "advisor only emitted a
// redacted thinking block" — see SummarizeBlocks for the shape.
func renderSimplePromptContent(content []ai.AssistantContent) (string, []BlockSummary, error) {
	blocks := SummarizeBlocks(content)
	var parts []string
	for _, c := range content {
		switch {
		case c.Text != nil:
			if c.Text.Text != "" {
				parts = append(parts, c.Text.Text)
			}
		case c.Thinking != nil:
			if c.Thinking.Thinking != "" {
				parts = append(parts, "[think] "+c.Thinking.Thinking)
			}
		case c.ToolCall != nil:
			argsJSON, err := json.Marshal(c.ToolCall.Arguments)
			if err != nil {
				argsJSON = []byte("{}")
			}
			parts = append(parts, fmt.Sprintf("[tool %s %s]", c.ToolCall.Name, string(argsJSON)))
		}
	}
	if len(parts) == 0 {
		return "", blocks, fmt.Errorf("response had no usable content (blocks: %s)", formatBlockSummary(blocks))
	}
	return strings.Join(parts, "\n"), blocks, nil
}

func (a *Agent) dequeueSteeringMessages() []AgentMessage {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.steeringQueue) == 0 {
		return nil
	}

	if a.steeringMode == "one-at-a-time" {
		first := a.steeringQueue[0]
		a.steeringQueue = a.steeringQueue[1:]
		return []AgentMessage{first}
	}

	msgs := a.steeringQueue
	a.steeringQueue = nil
	return msgs
}

func (a *Agent) dequeueFollowUpMessages() []AgentMessage {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.followUpQueue) == 0 {
		return nil
	}

	if a.followUpMode == "one-at-a-time" {
		first := a.followUpQueue[0]
		a.followUpQueue = a.followUpQueue[1:]
		return []AgentMessage{first}
	}

	msgs := a.followUpQueue
	a.followUpQueue = nil
	return msgs
}

// runLoop runs the agent loop in a goroutine.
func (a *Agent) runLoop(messages []AgentMessage, skipInitialSteeringPoll bool) {
	a.mu.Lock()
	model := a.state.Model
	idleCh := make(chan struct{})
	a.idleCh = idleCh

	ctx, cancel := context.WithCancel(context.Background())
	a.abortCancel = cancel
	a.state.IsStreaming = true
	a.state.StreamMessage = nil
	a.state.Error = ""

	var reasoning ai.ThinkingLevel
	if a.state.ThinkingLevel != ThinkingOff {
		reasoning = ToAIThinkingLevel(a.state.ThinkingLevel)
	}

	agentCtx := &AgentContext{
		SystemPrompt: a.state.SystemPrompt,
		Messages:     make([]AgentMessage, len(a.state.Messages)),
		Tools:        a.state.Tools,
	}
	copy(agentCtx.Messages, a.state.Messages)

	streamFn := a.streamFn
	if streamFn == nil {
		streamFn = func(m *ai.Model, c ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			return ai.StreamSimple(ctx, ai.DefaultRegistry, m, c, opts)
		}
	}

	skipSteering := skipInitialSteeringPoll

	config := &AgentLoopConfig{
		Model:            model,
		Reasoning:        reasoning,
		SessionID:        a.sessionID,
		Transport:        a.transport,
		ThinkingBudgets:  a.thinkingBudgets,
		MaxRetryDelayMs:  a.maxRetryDelayMs,
		ServerTools:      a.serverTools,
		Compaction:       a.compaction,
		OnPayload:        a.onPayload,
		ConvertToLLM:     a.convertToLLM,
		TransformContext: a.transformCtx,
		GetApiKey:        a.getApiKey,
		GetSteeringMessages: func() ([]AgentMessage, error) {
			if skipSteering {
				skipSteering = false
				return nil, nil
			}
			return a.dequeueSteeringMessages(), nil
		},
		GetFollowUpMessages: func() ([]AgentMessage, error) {
			return a.dequeueFollowUpMessages(), nil
		},
	}

	a.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			a.mu.Lock()
			a.state.IsStreaming = false
			a.state.StreamMessage = nil
			a.state.PendingToolCalls = make(map[string]bool)
			a.abortCancel = nil
			a.mu.Unlock()
			close(idleCh)
		}()

		events := make(chan AgentEvent, 64)

		go func() {
			if messages != nil {
				AgentLoop(ctx, messages, agentCtx, config, streamFn, events)
			} else {
				AgentLoopContinue(ctx, agentCtx, config, streamFn, events)
			}
			close(events)
		}()

		var partial *AgentMessage

		for event := range events {
			a.mu.Lock()
			switch event.Type {
			case EventMessageStart:
				if event.Message != nil {
					partial = event.Message
					a.state.StreamMessage = event.Message
				}

			case EventMessageUpdate:
				if event.Message != nil {
					partial = event.Message
					a.state.StreamMessage = event.Message
				}

			case EventMessageEnd:
				partial = nil
				a.state.StreamMessage = nil
				if event.Message != nil {
					a.state.Messages = append(a.state.Messages, *event.Message)
				}

			case EventToolExecutionStart:
				if a.state.PendingToolCalls == nil {
					a.state.PendingToolCalls = make(map[string]bool)
				}
				a.state.PendingToolCalls[event.ToolCallID] = true

			case EventToolExecutionEnd:
				delete(a.state.PendingToolCalls, event.ToolCallID)

			case EventTurnEnd:
				if event.TurnMessage != nil {
					msg := event.TurnMessage
					if msg.AsAssistant() != nil && msg.AsAssistant().ErrorMessage != "" {
						a.state.Error = msg.AsAssistant().ErrorMessage
					}
				}

			case EventAgentEnd:
				a.state.IsStreaming = false
				a.state.StreamMessage = nil
			}
			a.mu.Unlock()

			// Emit to listeners outside the lock
			a.emit(event)
		}

		// Handle remaining partial message
		if partial != nil && partial.Role() == "assistant" {
			am := partial.AsAssistant()
			if am != nil && len(am.Content) > 0 {
				hasContent := false
				for _, c := range am.Content {
					if c.IsText() && len(c.Text.Text) > 0 {
						hasContent = true
						break
					}
					if c.IsThinking() && (len(c.Thinking.Thinking) > 0 || c.Thinking.ThinkingSignature != "" || c.Thinking.Redacted) {
						hasContent = true
						break
					}
					if c.IsToolCall() && len(c.ToolCall.Name) > 0 {
						hasContent = true
						break
					}
				}
				if hasContent {
					a.mu.Lock()
					a.state.Messages = append(a.state.Messages, *partial)
					a.mu.Unlock()
				}
			}
		}
	}()
}

func (a *Agent) emit(e AgentEvent) {
	a.mu.Lock()
	// Copy listeners to avoid holding lock during callbacks
	fns := make([]func(AgentEvent), 0, len(a.listeners))
	for _, fn := range a.listeners {
		fns = append(fns, fn)
	}
	a.mu.Unlock()

	for _, fn := range fns {
		fn(e)
	}
}
