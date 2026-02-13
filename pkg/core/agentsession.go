// Ported from: packages/coding-agent/src/core/agent-session.ts
// Upstream hash: 1caadb2e
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

	// Bash execution
	bashCancel   context.CancelFunc
	bashCancelMu sync.Mutex
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
		if idx < len(s.listeners) {
			s.listeners[idx] = nil
		}
		// Compact: remove trailing nil entries
		for len(s.listeners) > 0 && s.listeners[len(s.listeners)-1] == nil {
			s.listeners = s.listeners[:len(s.listeners)-1]
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

	// Expand skill commands (/skill:name args) and prompt templates (/template args)
	content := s.expandSkillCommand(text)
	if templates, _ := s.resourceLoader.GetPrompts(); len(templates) > 0 {
		content = ExpandPromptTemplate(content, templates)
	}

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

// expandSkillCommand expands /skill:<name> commands into skill XML blocks.
// Returns the original text unchanged if it's not a skill command.
func (s *AgentSession) expandSkillCommand(text string) string {
	if !strings.HasPrefix(text, "/skill:") {
		return text
	}

	spaceIdx := strings.Index(text, " ")
	var skillName, args string
	if spaceIdx == -1 {
		skillName = text[7:]
	} else {
		skillName = text[7:spaceIdx]
		args = strings.TrimSpace(text[spaceIdx+1:])
	}

	skills, _ := s.resourceLoader.GetSkills()
	var found *Skill
	for i := range skills {
		if skills[i].Name == skillName {
			found = &skills[i]
			break
		}
	}
	if found == nil {
		return text
	}

	data, err := os.ReadFile(found.FilePath)
	if err != nil {
		return text
	}

	body := strings.TrimSpace(StripFrontmatter(string(data)))
	skillBlock := fmt.Sprintf("<skill name=%q location=%q>\nReferences are relative to %s.\n\n%s\n</skill>",
		found.Name, found.FilePath, found.BaseDir, body)
	if args != "" {
		return skillBlock + "\n\n" + args
	}
	return skillBlock
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

	// Get the last assistant message and calculate total context tokens
	var lastAssistant *ai.AssistantMessage
	totalContextTokens := 0
	for i := len(state.Messages) - 1; i >= 0; i-- {
		if a := state.Messages[i].Message.AsAssistant(); a != nil {
			if lastAssistant == nil {
				lastAssistant = a
			}
			// Accumulate tokens from all assistant messages
			totalContextTokens += calculateContextTokensFromUsage(a.Usage)
		}
	}
	if lastAssistant == nil {
		return
	}

	model := s.Model()
	if model == nil {
		return
	}

	// Check if context overflowed
	isOverflow := ai.IsContextOverflow(lastAssistant, model.ContextWindow)

	// Check token-based threshold
	shouldCompactNow := s.compactionRunner.ShouldCompact(totalContextTokens, model.ContextWindow)

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

// SetSessionName sets the display name for the current session.
func (s *AgentSession) SetSessionName(name string) {
	s.SessionManager.AppendSessionInfo(name)
}

// GetSessionName returns the display name for the current session.
func (s *AgentSession) GetSessionName() string {
	return s.SessionManager.GetSessionName()
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

// SwitchSession switches to a different session file, reloading messages.
func (s *AgentSession) SwitchSession(sessionPath string) error {
	// Abort any in-progress streaming
	s.Agent.Abort()

	// Switch the session file (loads entries)
	s.SessionManager.SetSessionFile(sessionPath)

	// Rebuild agent messages from session context
	ctx := s.SessionManager.BuildSessionContext()
	s.Agent.ReplaceMessages(ctx.Messages)

	// Apply session thinking level if available
	if ctx.ThinkingLevel != "" {
		s.SetThinkingLevel(ctx.ThinkingLevel)
	}

	// Rebuild system prompt
	s.buildSystemPrompt()
	s.Agent.SetSystemPrompt(s.baseSystemPrompt)

	return nil
}

// Fork creates a branch at the given entry ID.
// Returns the selected user message text and whether it was cancelled.
func (s *AgentSession) Fork(entryID string) (selectedText string, cancelled bool, err error) {
	previousSessionFile := s.SessionManager.GetSessionFile()
	entry := s.SessionManager.GetEntry(entryID)

	if entry == nil || entry.Type != "message" {
		return "", false, fmt.Errorf("invalid entry ID for forking")
	}

	// Verify it's a user message
	var probe struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(entry.RawMessage, &probe) != nil || probe.Role != "user" {
		return "", false, fmt.Errorf("invalid entry ID for forking")
	}

	selectedText = extractUserMessageText(entry.RawMessage)

	// Create the branched session
	if entry.ParentID == "" {
		s.SessionManager.NewSession(&NewSessionOptions{ParentSession: previousSessionFile})
	} else {
		s.SessionManager.CreateBranchedSession(entry.ParentID)
	}
	s.Agent.SetSessionID(s.SessionManager.GetSessionID())

	// Reload messages from entries
	ctx := s.SessionManager.BuildSessionContext()
	s.Agent.ReplaceMessages(ctx.Messages)

	return selectedText, false, nil
}

// ForkMessageEntry represents a user message available for forking.
type ForkMessageEntry struct {
	EntryID string `json:"entryId"`
	Text    string `json:"text"`
}

// GetUserMessagesForForking returns all user messages from the session for the fork selector.
func (s *AgentSession) GetUserMessagesForForking() []ForkMessageEntry {
	entries := s.SessionManager.GetEntries()
	var result []ForkMessageEntry

	for _, entry := range entries {
		if entry.Type != "message" || len(entry.RawMessage) == 0 {
			continue
		}
		var probe struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(entry.RawMessage, &probe) != nil || probe.Role != "user" {
			continue
		}

		text := extractUserMessageText(entry.RawMessage)
		if text != "" {
			result = append(result, ForkMessageEntry{EntryID: entry.ID, Text: text})
		}
	}

	return result
}

// extractUserMessageText extracts the text content from a user message's raw JSON.
func extractUserMessageText(raw json.RawMessage) string {
	// Try string content first
	var strContent string
	if json.Unmarshal(raw, &struct {
		Content *string `json:"content"`
	}{&strContent}) == nil && strContent != "" {
		return strContent
	}

	// Try array content
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil || len(msg.Content) == 0 {
		return ""
	}

	// Check if it's a string
	var s2 string
	if json.Unmarshal(msg.Content, &s2) == nil {
		return s2
	}

	// Try array of content blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(msg.Content, &blocks) != nil {
		return ""
	}

	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
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

// ============================================================================
// Bash execution
// ============================================================================

// ExecuteBash executes a bash command and records the result in session history.
func (s *AgentSession) ExecuteBash(command string, onChunk func(string)) (BashResult, error) {
	return s.ExecuteBashWithOptions(command, onChunk, false)
}

// ExecuteBashWithOptions executes a bash command with optional streaming, cancellation,
// and the option to exclude the result from context.
func (s *AgentSession) ExecuteBashWithOptions(command string, onChunk func(string), excludeFromContext bool) (BashResult, error) {
	ctx, cancel := context.WithCancel(context.Background())
	s.bashCancelMu.Lock()
	s.bashCancel = cancel
	s.bashCancelMu.Unlock()

	defer func() {
		s.bashCancelMu.Lock()
		s.bashCancel = nil
		s.bashCancelMu.Unlock()
		cancel()
	}()

	// Apply command prefix if configured
	prefix := s.SettingsManager.GetShellCommandPrefix()
	resolvedCommand := command
	if prefix != "" {
		resolvedCommand = prefix + "\n" + command
	}

	result, err := ExecuteBash(ctx, resolvedCommand, &BashExecutorOptions{
		OnChunk: onChunk,
	})
	if err != nil {
		return result, err
	}

	// Record in session
	exitCode := result.ExitCode
	bashMsg := &BashExecutionMessage{
		Role:               "bashExecution",
		Command:            command,
		Output:             result.Output,
		ExitCode:           &exitCode,
		Cancelled:          result.Cancelled,
		Truncated:          result.Truncated,
		FullOutputPath:     result.FullOutputPath,
		Timestamp:          time.Now().UnixMilli(),
		ExcludeFromContext: excludeFromContext,
	}

	agentMsg := agent.AgentMessage{Custom: bashMsg}
	s.Agent.AppendMessage(agentMsg)
	s.SessionManager.AppendAgentMessage(agentMsg)

	return result, nil
}

// AbortBash cancels a running bash command.
func (s *AgentSession) AbortBash() {
	s.bashCancelMu.Lock()
	defer s.bashCancelMu.Unlock()
	if s.bashCancel != nil {
		s.bashCancel()
	}
}

// IsBashRunning returns whether a bash command is currently executing.
func (s *AgentSession) IsBashRunning() bool {
	s.bashCancelMu.Lock()
	defer s.bashCancelMu.Unlock()
	return s.bashCancel != nil
}

// SessionStats holds statistics about the current session.
type SessionStats struct {
	SessionFile       string `json:"sessionFile,omitempty"`
	SessionID         string `json:"sessionId"`
	UserMessages      int    `json:"userMessages"`
	AssistantMessages int    `json:"assistantMessages"`
	ToolCalls         int    `json:"toolCalls"`
	ToolResults       int    `json:"toolResults"`
	TotalMessages     int    `json:"totalMessages"`
	Tokens            struct {
		Input      int `json:"input"`
		Output     int `json:"output"`
		CacheRead  int `json:"cacheRead"`
		CacheWrite int `json:"cacheWrite"`
		Total      int `json:"total"`
	} `json:"tokens"`
	Cost float64 `json:"cost"`
}

// GetSessionStats returns statistics about the current session.
func (s *AgentSession) GetSessionStats() SessionStats {
	state := s.State()
	var stats SessionStats
	stats.SessionFile = s.SessionManager.GetSessionFile()
	stats.SessionID = s.SessionManager.GetSessionID()
	stats.TotalMessages = len(state.Messages)

	for _, msg := range state.Messages {
		switch {
		case msg.Message.AsUser() != nil:
			stats.UserMessages++
		case msg.Message.AsAssistant() != nil:
			stats.AssistantMessages++
			a := msg.Message.AsAssistant()
			for _, c := range a.Content {
				if c.ToolCall != nil {
					stats.ToolCalls++
				}
			}
			stats.Tokens.Input += a.Usage.Input
			stats.Tokens.Output += a.Usage.Output
			stats.Tokens.CacheRead += a.Usage.CacheRead
			stats.Tokens.CacheWrite += a.Usage.CacheWrite
			stats.Cost += a.Usage.Cost.Total
		case msg.Message.AsToolResult() != nil:
			stats.ToolResults++
		}
	}
	stats.Tokens.Total = stats.Tokens.Input + stats.Tokens.Output + stats.Tokens.CacheRead + stats.Tokens.CacheWrite
	return stats
}

// GetLastAssistantText returns the text content of the last assistant message, or empty string.
func (s *AgentSession) GetLastAssistantText() string {
	state := s.State()
	for i := len(state.Messages) - 1; i >= 0; i-- {
		if a := state.Messages[i].Message.AsAssistant(); a != nil {
			for _, c := range a.Content {
				if c.Text != nil {
					return c.Text.Text
				}
			}
			return ""
		}
	}
	return ""
}

// NavigateTreeResult is the result of tree navigation.
type NavigateTreeResult struct {
	EditorText string
	Cancelled  bool
	Aborted    bool
}

// NavigateTree navigates to a specific entry in the session tree.
// If summarize is true, it creates a branch summary before navigating.
func (s *AgentSession) NavigateTree(entryID string, summarize bool, customInstructions string) (*NavigateTreeResult, error) {
	leafID := s.SessionManager.GetLeafID()
	if entryID == leafID {
		return &NavigateTreeResult{}, nil
	}

	if summarize {
		// Create branch with summary
		s.SessionManager.BranchWithSummary(leafID, "", nil, false)
	}

	// Branch to the new entry
	s.SessionManager.Branch(entryID)

	// Rebuild messages from the new branch
	ctx := s.SessionManager.BuildSessionContext()
	s.Agent.ReplaceMessages(ctx.Messages)

	// Find user message text at this entry for editor pre-fill
	entry := s.SessionManager.GetEntry(entryID)
	var editorText string
	if entry != nil && entry.Type == "message" {
		editorText = extractUserMessageText(entry.RawMessage)
	}

	return &NavigateTreeResult{EditorText: editorText}, nil
}

// Close cleans up the session.
func (s *AgentSession) Close() {
	if s.unsubAgent != nil {
		s.unsubAgent()
	}
}
