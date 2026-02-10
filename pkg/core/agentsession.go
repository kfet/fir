// Ported from: packages/coding-agent/src/core/agent-session.ts
// Upstream hash: 1caadb2e
package core

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
)

// ============================================================================
// Types
// ============================================================================

// ParsedSkillBlock represents a parsed skill block from a user message.
type ParsedSkillBlock struct {
	Name        string
	Location    string
	Content     string
	UserMessage string // empty if no user message after the skill block
}

// skillBlockRe matches a skill block in message text.
var skillBlockRe = regexp.MustCompile(`(?s)^<skill name="([^"]+)" location="([^"]+)">\n(.*?)\n</skill>(?:\n\n([\s\S]+))?$`)

// ParseSkillBlock parses a skill block from message text.
// Returns nil if the text doesn't contain a skill block.
func ParseSkillBlock(text string) *ParsedSkillBlock {
	m := skillBlockRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	userMsg := ""
	if len(m) > 4 {
		userMsg = strings.TrimSpace(m[4])
	}
	return &ParsedSkillBlock{
		Name:        m[1],
		Location:    m[2],
		Content:     m[3],
		UserMessage: userMsg,
	}
}

// CompactionResult is the result of a compaction operation (mirrors compaction.CompactionResult).
type CompactionResultInfo struct {
	Summary          string
	FirstKeptEntryID string
	TokensBefore     int
}

// CompactionRunner handles compaction logic. This decouples agentsession from the compaction package.
type CompactionRunner interface {
	// ShouldCompact checks if compaction should trigger.
	ShouldCompact(contextTokens, contextWindow int) bool
	// RunCompaction performs compaction and returns the result.
	RunCompaction(session *AgentSession) (*CompactionResultInfo, error)
}

// AgentSessionEvent is an event emitted by the AgentSession.
// It wraps the core AgentEvent with session-specific events.
type AgentSessionEvent struct {
	// Embedded agent event (nil for session-only events)
	AgentEvent *agent.AgentEvent

	// Session-specific event types
	Type string // agent event type, or "auto_compaction_start", "auto_compaction_end", etc.

	// Auto-compaction fields
	CompactionResult *CompactionResultInfo
	CompactionReason string // "threshold" or "overflow"
	Aborted          bool
	WillRetry        bool
	ErrorMessage     string
}

// AgentSessionEventListener receives session events.
type AgentSessionEventListener func(AgentSessionEvent)

// PromptOptions configure how a prompt is sent.
type PromptOptions struct {
	Images              []ai.ImageContent
	ExpandTemplates     bool   // default: true
	StreamingBehavior   string // "steer" or "followUp"
}

// AgentSessionOptions configures a new AgentSession.
type AgentSessionOptions struct {
	Agent            *agent.Agent
	SessionManager   *SessionManager
	SettingsManager  *SettingsManager
	ResourceLoader   ResourceLoader
	ModelRegistry    *ModelRegistry
	CompactionRunner CompactionRunner
	Cwd              string
	ScopedModels     []ScopedModel
}

// ============================================================================
// AgentSession
// ============================================================================

// AgentSession is the core abstraction for agent lifecycle and session management.
// It wraps the Agent with session persistence, compaction, tool management, and event routing.
type AgentSession struct {
	Agent           *agent.Agent
	SessionManager  *SessionManager
	SettingsManager *SettingsManager

	resourceLoader   ResourceLoader
	modelRegistry    *ModelRegistry
	compactionRunner CompactionRunner
	cwd              string
	scopedModels     []ScopedModel

	// Event subscription
	mu         sync.RWMutex
	listeners  []AgentSessionEventListener
	unsubAgent func()

	// System prompt
	baseSystemPrompt string
}

// NewAgentSession creates a new AgentSession.
func NewAgentSession(opts AgentSessionOptions) *AgentSession {
	s := &AgentSession{
		Agent:            opts.Agent,
		SessionManager:   opts.SessionManager,
		SettingsManager:  opts.SettingsManager,
		resourceLoader:   opts.ResourceLoader,
		modelRegistry:    opts.ModelRegistry,
		compactionRunner: opts.CompactionRunner,
		cwd:              opts.Cwd,
		scopedModels:     opts.ScopedModels,
	}

	// Subscribe to agent events for internal handling
	s.unsubAgent = s.Agent.Subscribe(s.handleAgentEvent)

	// Build system prompt
	s.buildSystemPrompt()

	return s
}

// ============================================================================
// State accessors
// ============================================================================

// State returns the current agent state.
func (s *AgentSession) State() agent.AgentState {
	return s.Agent.State()
}

// Model returns the current model.
func (s *AgentSession) Model() *ai.Model {
	return s.State().Model
}

// ThinkingLevel returns the current thinking level.
func (s *AgentSession) ThinkingLevel() string {
	return string(s.State().ThinkingLevel)
}

// IsStreaming returns whether the agent is currently streaming.
func (s *AgentSession) IsStreaming() bool {
	return s.State().IsStreaming
}

// ResourceLoader returns the resource loader.
func (s *AgentSession) ResourceLoader() ResourceLoader {
	return s.resourceLoader
}

// ModelRegistry returns the model registry.
func (s *AgentSession) ModelRegistryRef() *ModelRegistry {
	return s.modelRegistry
}

// ============================================================================
// Event Subscription
// ============================================================================

// Subscribe adds an event listener. Returns an unsubscribe function.
func (s *AgentSession) Subscribe(fn AgentSessionEventListener) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
	idx := len(s.listeners) - 1
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		// Nil out the listener
		if idx < len(s.listeners) {
			s.listeners[idx] = nil
		}
	}
}

func (s *AgentSession) emit(event AgentSessionEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.listeners {
		if l != nil {
			l(event)
		}
	}
}

// ============================================================================
// Internal agent event handling
// ============================================================================

func (s *AgentSession) handleAgentEvent(event agent.AgentEvent) {
	// Wrap and emit
	sessionEvent := AgentSessionEvent{
		AgentEvent: &event,
		Type:       string(event.Type),
	}
	s.emit(sessionEvent)

	// Session persistence on message_end
	if event.Type == agent.EventMessageEnd && event.Message != nil {
		s.persistMessage(*event.Message)
	}

	// Auto-compaction check on agent_end
	if event.Type == agent.EventAgentEnd {
		s.checkAutoCompaction()
	}
}

func (s *AgentSession) persistMessage(msg agent.AgentMessage) {
	role := msg.Role()

	switch role {
	case "user", "assistant", "toolResult":
		s.SessionManager.AppendAgentMessage(msg)
	case "custom":
		if msg.Custom != nil {
			if cm, ok := msg.Custom.(*CustomMessage); ok {
				data, _ := json.Marshal(cm.Content)
				s.SessionManager.AppendCustomEntry(cm.CustomType, data)
			}
		}
	}
}

// ============================================================================
// Prompting
// ============================================================================

// Prompt sends a user message and waits for the agent to complete.
func (s *AgentSession) Prompt(text string, opts ...*PromptOptions) error {
	if s.Model() == nil {
		return fmt.Errorf("no model selected. Use /login or set an API key environment variable")
	}

	// Build system prompt before each turn
	s.buildSystemPrompt()
	s.Agent.SetSystemPrompt(s.baseSystemPrompt)

	// Build user message
	content := text
	ts := time.Now().UnixMilli()
	userMsg := agent.NewAgentMessage(ai.NewUserMsg(content, ts))

	msgs := []agent.AgentMessage{userMsg}

	// Send to agent
	if err := s.Agent.PromptMessages(msgs); err != nil {
		return err
	}

	// Wait for the agent loop to complete (it runs in a goroutine)
	s.Agent.WaitForIdle()
	return nil
}

// ============================================================================
// System prompt
// ============================================================================

func (s *AgentSession) buildSystemPrompt() {
	// Collect skills
	skills, _ := s.resourceLoader.GetSkills()

	// Collect agents files as context files
	agentsFiles := s.resourceLoader.GetAgentsFiles()
	contextFiles := make([]ContextFile, len(agentsFiles))
	for i, f := range agentsFiles {
		contextFiles[i] = ContextFile{Path: f.Path, Content: f.Content}
	}

	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		Skills:       skills,
		ContextFiles: contextFiles,
		Cwd:          s.cwd,
	})

	// Apply custom system prompt override
	if sp := s.resourceLoader.GetSystemPrompt(); sp != "" {
		prompt = sp
	}

	// Append additional instructions
	for _, append := range s.resourceLoader.GetAppendSystemPrompt() {
		prompt += "\n\n" + append
	}

	s.baseSystemPrompt = prompt
}

// ============================================================================
// Compaction
// ============================================================================

func (s *AgentSession) checkAutoCompaction() {
	if s.compactionRunner == nil {
		return
	}

	state := s.State()
	if len(state.Messages) == 0 {
		return
	}

	// Get the last assistant message
	var lastAssistant *ai.AssistantMessage
	for i := len(state.Messages) - 1; i >= 0; i-- {
		if a := state.Messages[i].Message.AsAssistant(); a != nil {
			lastAssistant = a
			break
		}
	}
	if lastAssistant == nil {
		return
	}

	model := s.Model()
	if model == nil {
		return
	}

	// Calculate context tokens from usage
	contextTokens := calculateContextTokensFromUsage(lastAssistant.Usage)

	// Check if context overflowed
	isOverflow := ai.IsContextOverflow(lastAssistant, model.ContextWindow)

	// Check token-based threshold
	shouldCompactNow := s.compactionRunner.ShouldCompact(contextTokens, model.ContextWindow)

	if !isOverflow && !shouldCompactNow {
		return
	}

	reason := "threshold"
	if isOverflow {
		reason = "overflow"
	}

	s.emit(AgentSessionEvent{Type: "auto_compaction_start", CompactionReason: reason})

	result, err := s.compactionRunner.RunCompaction(s)
	if err != nil {
		s.emit(AgentSessionEvent{
			Type:         "auto_compaction_end",
			ErrorMessage: err.Error(),
			Aborted:      false,
			WillRetry:    isOverflow,
		})
		return
	}

	s.emit(AgentSessionEvent{
		Type:             "auto_compaction_end",
		CompactionResult: result,
		WillRetry:        isOverflow,
	})

	// If overflow, retry the prompt with last user message
	if isOverflow && result != nil {
		var lastUserText string
		for i := len(state.Messages) - 1; i >= 0; i-- {
			if u := state.Messages[i].Message.AsUser(); u != nil {
				if txt, ok := u.Content.(string); ok {
					lastUserText = txt
				}
				break
			}
		}
		if lastUserText != "" {
			go func() { _ = s.Prompt(lastUserText) }()
		}
	}
}

// calculateContextTokensFromUsage is a simple token calculation for compaction checks.
func calculateContextTokensFromUsage(usage ai.Usage) int {
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

// RunCompaction runs manual compaction via the CompactionRunner.
func (s *AgentSession) RunCompaction() (*CompactionResultInfo, error) {
	if s.compactionRunner == nil {
		return nil, fmt.Errorf("compaction not configured")
	}
	return s.compactionRunner.RunCompaction(s)
}

// ============================================================================
// Model management
// ============================================================================

// SetModel changes the current model.
func (s *AgentSession) SetModel(model *ai.Model) {
	s.Agent.SetModel(model)
	s.SessionManager.AppendModelChange(model.Provider, model.ID)
}

// SetThinkingLevel changes the thinking level.
func (s *AgentSession) SetThinkingLevel(level string) {
	s.Agent.SetThinkingLevel(agent.ThinkingLevel(level))
	s.SessionManager.AppendThinkingLevelChange(level)
}

// GetAvailableThinkingLevels returns the thinking levels available for the current model.
func (s *AgentSession) GetAvailableThinkingLevels() []agent.ThinkingLevel {
	model := s.Model()
	if model == nil || !model.Reasoning {
		return []agent.ThinkingLevel{agent.ThinkingOff}
	}
	levels := []agent.ThinkingLevel{
		agent.ThinkingOff,
		agent.ThinkingMinimal,
		agent.ThinkingLow,
		agent.ThinkingMedium,
		agent.ThinkingHigh,
	}
	// Check if model supports xhigh
	if ai.SupportsXhigh(model) {
		levels = append(levels, agent.ThinkingXHigh)
	}
	return levels
}

// ScopedModelsRef returns the scoped models for this session.
func (s *AgentSession) ScopedModelsRef() []ScopedModel {
	return s.scopedModels
}

// ============================================================================
// Session management
// ============================================================================

// NewSession creates a new session.
func (s *AgentSession) NewSessionCmd() (bool, error) {
	s.SessionManager.NewSession(nil)
	s.Agent.ReplaceMessages(nil)
	s.buildSystemPrompt()
	s.Agent.SetSystemPrompt(s.baseSystemPrompt)
	return true, nil
}

// Fork creates a branch at the given entry ID.
// TODO: implement when SessionManager.Fork is available
func (s *AgentSession) Fork(entryID string) error {
	return fmt.Errorf("fork not yet implemented")
}

// Reload reloads resources and rebuilds the system prompt.
func (s *AgentSession) Reload() error {
	if err := s.resourceLoader.Reload(); err != nil {
		return err
	}
	s.buildSystemPrompt()
	s.Agent.SetSystemPrompt(s.baseSystemPrompt)
	return nil
}

// Close cleans up the session.
func (s *AgentSession) Close() {
	if s.unsubAgent != nil {
		s.unsubAgent()
	}
}
