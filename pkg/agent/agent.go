// Ported from: packages/agent/src/agent.ts
// Upstream hash: a1edb8a4
package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

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
		a.transport = ai.TransportSSE
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

// SimplePrompt makes a single-turn LLM call with the given messages.
// It reuses the agent's model, streamFn, api key resolution, and transport
// config but sends no tools, runs no agent loop, and does not modify the
// agent's state. The caller provides the full message list (including system
// prompt via the agent's current state). Returns the assistant's text response.
// Safe to call concurrently while the agent loop is running.
//
// NO-COMPACTION CONTRACT: SimplePrompt MUST NOT trigger auto-compaction, ever.
// This is guaranteed by two design choices that must be preserved:
//  1. The AgentLoopConfig built here intentionally omits the Compaction field,
//     so no server-side compaction is requested.
//  2. The events channel is a private, local channel drained by a throwaway
//     goroutine — events never reach AgentSession.checkAutoCompaction.
//
// Do not forward these events to the session or add Compaction to the config.
func (a *Agent) SimplePrompt(ctx context.Context, messages []AgentMessage) (string, error) {
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

	if model == nil {
		return "", fmt.Errorf("no model selected")
	}

	if streamFn == nil {
		streamFn = func(m *ai.Model, c ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			return ai.StreamSimple(ctx, ai.DefaultRegistry, m, c, opts)
		}
	}

	if convertToLLM == nil {
		return "", fmt.Errorf("no ConvertToLLM function configured")
	}

	agentCtx := &AgentContext{
		SystemPrompt: systemPrompt,
		Messages:     messages,
		Tools:        NewToolSet(), // empty — no tools
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

	// Discard agent events — we don't emit them for simple prompts.
	events := make(chan AgentEvent, 64)
	go func() {
		for range events {
		}
	}()

	msg := streamAssistantResponse(ctx, agentCtx, config, streamFn, events)
	close(events)

	if msg == nil {
		return "", fmt.Errorf("no response from model")
	}
	if msg.ErrorMessage != "" {
		return "", fmt.Errorf("%s", msg.ErrorMessage)
	}
	var sb strings.Builder
	for _, c := range msg.Content {
		if c.Text != nil {
			sb.WriteString(c.Text.Text)
		}
	}
	return sb.String(), nil
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
					if c.IsThinking() && len(c.Thinking.Thinking) > 0 {
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
