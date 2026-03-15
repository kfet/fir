// Ported from: packages/coding-agent/src/core/agent-session.ts
// Upstream hash: 1caadb2e
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/agent/tools"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/overflow"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/exec"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session/store"
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

// CompactionInfo contains pre-run statistics about what a compaction will process.
// It is returned by GetStats and included in auto_compaction_start events.
type CompactionInfo struct {
	// MessagesToSummarize is the number of messages that will be sent to the LLM.
	MessagesToSummarize int
	// TokensBefore is the estimated context token count before compaction.
	TokensBefore int
}

// CompactionRunner handles compaction logic. This decouples agentsession from the compaction package.
type CompactionRunner interface {
	// IsEnabled reports whether auto-compaction is turned on in settings.
	IsEnabled() bool
	// ShouldCompact checks if compaction should trigger.
	ShouldCompact(contextTokens, contextWindow int) bool
	// GetStats returns pre-run information about what a compaction would process
	// without running any LLM calls. Returns nil if there is nothing to compact.
	GetStats(session *AgentSession) *CompactionInfo
	// RunCompaction performs compaction and returns the result.
	// customInstructions overrides the compaction prompt; pass "" to use settings default.
	// ctx may be cancelled to abort the compaction (e.g. when the user presses Escape).
	// Attach a CompactionProgressFunc via WithCompactionProgress to receive streaming updates.
	RunCompaction(ctx context.Context, session *AgentSession, customInstructions string) (*CompactionResultInfo, error)
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

	// CompactionInfo is set on auto_compaction_start to give the UI
	// upfront visibility into what will be compacted.
	CompactionInfo *CompactionInfo

	// SessionName is set on "session_named" events.
	SessionName string

	// PlanEntries is set on "plan_update" events.
	PlanEntries []agent.PlanEntry
	// PlanTitle is set on "plan_update" events.
	PlanTitle string
	// PlanMetadata is set on "plan_update" events.
	PlanMetadata map[string]string
}

// AgentSessionEventListener receives session events.
type AgentSessionEventListener func(AgentSessionEvent)

// PromptOptions configure how a prompt is sent.
type PromptOptions struct {
	Images            []ai.ImageContent
	ExpandTemplates   bool   // default: true
	StreamingBehavior string // "steer" or "followUp"
}

// AgentSessionOptions configures a new AgentSession.
// ToolCallInterceptor is called before a tool executes.
// Return non-nil to block the tool call.
type ToolCallInterceptor func(toolCallID, toolName string, input map[string]any) *ToolCallBlock

// ToolCallBlock indicates a tool call was blocked.
type ToolCallBlock struct {
	Reason string
}

// ToolResultInterceptor is called after a tool executes.
// Return non-nil to modify the result.
type ToolResultInterceptor func(toolCallID, toolName string, input map[string]any, content []ai.ToolResultContent, details any, isError bool) *ToolResultModification

// ToolResultModification modifies a tool result.
type ToolResultModification struct {
	Content []ai.ToolResultContent
	Details any
	IsError *bool
}

// UsageTracker records feature usage events. Implementations must be safe
// for concurrent use.
type UsageTracker interface {
	RecordToolUse(toolName string)
	RecordSlashCommand(name string)
}

// AgentSessionHooks provides optional hooks for external systems (e.g., extensions).
type AgentSessionHooks struct {
	// OnToolCall is called before each tool execution. Return non-nil to block.
	OnToolCall ToolCallInterceptor
	// OnToolResult is called after each tool execution. Return non-nil to modify result.
	OnToolResult ToolResultInterceptor
}

type AgentSessionOptions struct {
	Agent            *agent.Agent
	SessionManager   *store.SessionManager
	SettingsManager  *config.SettingsManager
	ResourceLoader   resources.ResourceLoader
	ModelRegistry    *models.ModelRegistry
	CompactionRunner CompactionRunner
	Cwd              string
	ScopedModels     []models.ScopedModel
	Hooks            *AgentSessionHooks
	UsageTracker     UsageTracker
}

// ============================================================================
// AgentSession
// ============================================================================

// AgentSession is the core abstraction for agent lifecycle and session management.
// It wraps the Agent with session persistence, compaction, tool management, and event routing.
type AgentSession struct {
	Agent           *agent.Agent
	SessionManager  *store.SessionManager
	SettingsManager *config.SettingsManager

	resourceLoader   resources.ResourceLoader
	modelRegistry    *models.ModelRegistry
	compactionRunner CompactionRunner
	cwd              string
	scopedModels     []models.ScopedModel

	// Event subscription
	mu             sync.RWMutex
	listeners      map[int]AgentSessionEventListener
	nextListenerID int
	unsubAgent     func()

	// System prompt
	baseSystemPrompt string

	// Bash execution
	bashCancel   context.CancelFunc
	bashCancelMu sync.Mutex

	// Auto-compaction: tracks the last assistant message for checking on agent_end
	lastAssistantMessage *ai.AssistantMessage

	// autoCompactProgressMu guards autoCompactProgress independently of mu
	// so that event listeners can set it without deadlocking inside emit().
	autoCompactProgressMu sync.Mutex
	autoCompactProgress   CompactionProgressFunc

	// Extension hooks
	hooks *AgentSessionHooks

	// Usage tracking (optional; nil disables tracking)
	usageTracker UsageTracker

	// Plan entries (guarded by mu)
	plan         []agent.PlanEntry
	planTitle    string
	planMetadata map[string]string
	planVersion  int64 // incremented on each UpdatePlan call
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
		hooks:            opts.Hooks,
		usageTracker:     opts.UsageTracker,
	}

	// Subscribe to agent events for internal handling
	s.unsubAgent = s.Agent.Subscribe(s.handleAgentEvent)

	// Build system prompt
	s.buildSystemPrompt()

	firlog.Debug("agent session created",
		"sessionID", opts.SessionManager.GetSessionID(),
		"hasCompaction", opts.CompactionRunner != nil,
	)

	return s
}

// UsageTracker returns the session's usage tracker, or nil if not set.
func (s *AgentSession) UsageTracker() UsageTracker {
	return s.usageTracker
}

// UpdatePlan replaces the plan entries and title, emits a "plan_update" event.
// The new state is also persisted to the session file so it survives resume.
func (s *AgentSession) UpdatePlan(title string, entries []agent.PlanEntry, metadata map[string]string) {
	s.mu.Lock()
	s.planTitle = title
	s.planMetadata = metadata
	s.plan = entries
	s.planVersion++
	s.mu.Unlock()
	s.SessionManager.AppendPlanUpdate(title, entries, metadata)
	snapshot := make([]agent.PlanEntry, len(entries))
	copy(snapshot, entries)
	s.emit(AgentSessionEvent{
		Type:         "plan_update",
		PlanEntries:  snapshot,
		PlanTitle:    title,
		PlanMetadata: metadata,
	})
}

// restorePlan sets the in-memory plan and emits a plan_update event without
// writing a new session entry (used when loading an existing session).
func (s *AgentSession) restorePlan(title string, entries []agent.PlanEntry, metadata map[string]string) {
	if len(entries) == 0 && title == "" {
		return
	}
	s.mu.Lock()
	s.planTitle = title
	s.planMetadata = metadata
	s.plan = entries
	s.mu.Unlock()
	snapshot := make([]agent.PlanEntry, len(entries))
	copy(snapshot, entries)
	s.emit(AgentSessionEvent{
		Type:         "plan_update",
		PlanEntries:  snapshot,
		PlanTitle:    title,
		PlanMetadata: metadata,
	})
}

// PlanEntries returns a copy of the current plan entries.
func (s *AgentSession) PlanEntries() []agent.PlanEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]agent.PlanEntry, len(s.plan))
	copy(out, s.plan)
	return out
}

// allPlanEntriesCompleted reports whether every entry has status "completed".
func allPlanEntriesCompleted(entries []agent.PlanEntry) bool {
	if len(entries) == 0 {
		return false
	}
	for _, e := range entries {
		if e.Status != agent.PlanEntryStatusCompleted {
			return false
		}
	}
	return true
}

// PlanTitle returns the current plan title.
func (s *AgentSession) PlanTitle() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.planTitle
}

// PlanMetadata returns a copy of the current plan metadata.
func (s *AgentSession) PlanMetadata() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.planMetadata == nil {
		return nil
	}
	out := make(map[string]string, len(s.planMetadata))
	for k, v := range s.planMetadata {
		out[k] = v
	}
	return out
}

// planVersionNum returns the current plan version counter.
func (s *AgentSession) planVersionNum() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.planVersion
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
func (s *AgentSession) ResourceLoader() resources.ResourceLoader {
	return s.resourceLoader
}

// ModelRegistry returns the model registry.
func (s *AgentSession) ModelRegistryRef() *models.ModelRegistry {
	return s.modelRegistry
}

// ============================================================================
// Event Subscription
// ============================================================================

// Subscribe adds an event listener. Returns an unsubscribe function.
func (s *AgentSession) Subscribe(fn AgentSessionEventListener) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listeners == nil {
		s.listeners = make(map[int]AgentSessionEventListener)
	}
	id := s.nextListenerID
	s.nextListenerID++
	s.listeners[id] = fn
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.listeners, id)
	}
}

func (s *AgentSession) emit(event AgentSessionEvent) {
	s.mu.RLock()
	listeners := make([]func(AgentSessionEvent), 0, len(s.listeners))
	for _, l := range s.listeners {
		listeners = append(listeners, l)
	}
	s.mu.RUnlock()
	for _, l := range listeners {
		l(event)
	}
}

// PublishEvent emits an event to all subscribers. This is intended for
// testing and for internal components that need to inject synthetic events.
func (s *AgentSession) PublishEvent(event AgentSessionEvent) {
	s.emit(event)
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

	// Track tool use
	if event.Type == agent.EventToolExecutionStart && event.ToolName != "" && s.usageTracker != nil {
		s.usageTracker.RecordToolUse(event.ToolName)
	}

	// Session persistence on message_end
	if event.Type == agent.EventMessageEnd && event.Message != nil {
		s.persistMessage(*event.Message)

		// Track last assistant message for auto-compaction (checked on agent_end)
		if event.Message.Role() == "assistant" {
			s.lastAssistantMessage = event.Message.AsAssistant()
		}
	}

	// Auto-compaction check on agent_end
	if event.Type == agent.EventAgentEnd {
		if s.lastAssistantMessage != nil {
			msg := s.lastAssistantMessage
			s.lastAssistantMessage = nil
			s.checkAutoCompaction(msg)
		}
	}

}

func (s *AgentSession) persistMessage(msg agent.AgentMessage) {
	role := msg.Role()
	firlog.Debug("persisting message", "role", role)

	switch role {
	case "user", "assistant", "toolResult":
		s.SessionManager.AppendAgentMessage(msg)
	case "custom":
		if msg.Custom != nil {
			if cm, ok := msg.Custom.(*store.CustomMessage); ok {
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
	firlog.Debug("prompt received", "len", len(text))

	if s.Model() == nil {
		return fmt.Errorf("no model selected. Use /login or set an API key environment variable")
	}

	// If a fully-completed plan was left over from a previous turn, clear it
	// immediately — there's no value showing a stale "done" plan. For
	// in-progress plans, remember to clear after this turn finishes so the
	// model can still reference it during the turn.
	preTurnPlan := s.PlanEntries()
	hadPlanBeforeTurn := len(preTurnPlan) > 0
	planVersionBefore := s.planVersionNum()

	if hadPlanBeforeTurn && allPlanEntriesCompleted(preTurnPlan) {
		s.UpdatePlan("", nil, nil)
		hadPlanBeforeTurn = false
		planVersionBefore = s.planVersionNum()
	}

	// Expand skill commands (/skill:name args) and prompt templates (/template args)
	content := s.expandSkillCommand(text)
	if templates, _ := s.resourceLoader.GetPrompts(); len(templates) > 0 {
		content = resources.ExpandPromptTemplate(content, templates)
	}

	ts := time.Now().UnixMilli()

	var images []ai.ImageContent
	if len(opts) > 0 && opts[0] != nil {
		images = opts[0].Images
	}

	var userMsgContent any
	if len(images) > 0 {
		// Build content as array of content blocks (text + images)
		blocks := make([]any, 0, 1+len(images))
		if content != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": content})
		}
		for _, img := range images {
			blocks = append(blocks, map[string]any{
				"type":     "image",
				"data":     img.Data,
				"mimeType": img.MimeType,
			})
		}
		userMsgContent = blocks
	} else {
		userMsgContent = content
	}

	userMsg := agent.NewAgentMessage(ai.NewUserMsg(userMsgContent, ts))

	// If streaming, queue the message as a follow-up. The agent drains the
	// queue automatically when the current turn ends. This applies both to
	// explicit follow-up requests (Alt+Enter) and normal submissions (Enter)
	// — without this, normal submissions would be silently dropped.
	if s.IsStreaming() {
		s.Agent.FollowUp(userMsg)
		return nil
	}

	msgs := []agent.AgentMessage{userMsg}

	// Send to agent
	if err := s.Agent.PromptMessages(msgs); err != nil {
		return err
	}

	// Wait for the agent loop to complete (it runs in a goroutine)
	s.Agent.WaitForIdle()

	// Clear a stale plan from a previous turn. If the model created or
	// updated the plan during *this* turn, keep it.
	if hadPlanBeforeTurn && s.planVersionNum() == planVersionBefore {
		s.UpdatePlan("", nil, nil)
	}

	return nil
}

// ClearFollowUpQueue clears and returns all queued follow-up message texts.
// Used by the /dequeue command to restore queued messages to the editor.
func (s *AgentSession) ClearFollowUpQueue() []string {
	queued := s.Agent.GetAndClearFollowUpQueue()
	texts := make([]string, 0, len(queued))
	for _, msg := range queued {
		if u := msg.Message.AsUser(); u != nil {
			if t, ok := u.Content.(string); ok && t != "" {
				texts = append(texts, t)
			}
		}
	}
	return texts
}

// PeekFollowUpQueue returns a snapshot of the queued follow-up message texts
// without clearing the queue.
func (s *AgentSession) PeekFollowUpQueue() []string {
	queued := s.Agent.PeekFollowUpQueue()
	texts := make([]string, 0, len(queued))
	for _, msg := range queued {
		if u := msg.Message.AsUser(); u != nil {
			if t, ok := u.Content.(string); ok && t != "" {
				texts = append(texts, t)
			}
		}
	}
	return texts
}

// RemoveFollowUp removes and returns the queued follow-up message at the given
// 1-based position. Returns the text and true if found and the content is a
// plain string; empty string and false if the index is out of range or the
// message content is non-text (e.g. image blocks).
func (s *AgentSession) RemoveFollowUp(oneBasedIndex int) (string, bool) {
	msg, ok := s.Agent.RemoveFollowUp(oneBasedIndex - 1)
	if !ok {
		return "", false
	}
	if u := msg.Message.AsUser(); u != nil {
		if t, ok2 := u.Content.(string); ok2 {
			return t, true
		}
	}
	return "", false
}

// ============================================================================
// System prompt
// ============================================================================

func (s *AgentSession) buildSystemPrompt() {
	// Collect skills
	skills, _ := s.resourceLoader.GetSkills()

	// Collect agents files as context files
	agentsFiles := s.resourceLoader.GetAgentsFiles()
	contextFiles := make([]resources.ContextFile, len(agentsFiles))
	for i, f := range agentsFiles {
		contextFiles[i] = resources.ContextFile{Path: f.Path, Content: f.Content}
	}

	prompt := resources.BuildSystemPrompt(resources.BuildSystemPromptOptions{
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

	// If skill commands are disabled, don't expand
	if s.SettingsManager != nil && !s.SettingsManager.GetEnableSkillCommands() {
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
	var found *resources.Skill
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

	body := strings.TrimSpace(resources.StripFrontmatter(string(data)))
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

func (s *AgentSession) checkAutoCompaction(assistantMessage *ai.AssistantMessage) {
	if s.compactionRunner == nil {
		return
	}

	// Skip if message was aborted (user cancelled)
	if assistantMessage.StopReason == ai.StopReasonAborted {
		return
	}

	model := s.Model()
	if model == nil {
		return
	}
	contextWindow := model.ContextWindow

	// Skip overflow check if the message came from a different model than currently selected.
	// This handles switching from a smaller-context model to a larger-context model —
	// the overflow from the old model shouldn't trigger compaction for the new model.
	sameModel := assistantMessage.Provider == model.Provider && assistantMessage.Model == model.ID

	// Skip overflow check if the error is from before a compaction in the current path.
	// This handles the case where an error was kept after compaction (in the "kept" region).
	errorIsFromBeforeCompaction := false
	compactionEntry := GetLatestCompactionEntry(s.SessionManager.GetBranch(""))
	if compactionEntry != nil {
		if ts, err := time.Parse(time.RFC3339Nano, compactionEntry.Timestamp); err == nil {
			errorIsFromBeforeCompaction = assistantMessage.Timestamp < ts.UnixMilli()
		}
	}

	// Case 1: Overflow — LLM returned context overflow error
	if sameModel && !errorIsFromBeforeCompaction && overflow.IsContextOverflow(assistantMessage, contextWindow) {
		// Respect the Enabled setting even for overflow-triggered compaction.
		if !s.compactionRunner.IsEnabled() {
			return
		}
		// Remove the error message from agent state before compaction
		// (it IS saved to session for history, but we don't want it in context for the retry)
		state := s.State()
		msgs := state.Messages
		if len(msgs) > 0 && msgs[len(msgs)-1].Role() == "assistant" {
			s.Agent.ReplaceMessages(msgs[:len(msgs)-1])
		}
		s.runAutoCompaction("overflow", true)
		return
	}

	// Case 2: Threshold — turn succeeded but context is getting large.
	// Skip if this was an error (non-overflow errors don't have valid usage data).
	if assistantMessage.StopReason == ai.StopReasonError {
		return
	}

	contextTokens := calculateContextTokens(assistantMessage.Usage)
	shouldCompact := s.compactionRunner.ShouldCompact(contextTokens, contextWindow)
	firlog.Debug("compaction check",
		"contextTokens", contextTokens,
		"contextWindow", contextWindow,
		"shouldCompact", shouldCompact,
	)
	if shouldCompact {
		s.runAutoCompaction("threshold", false)
	}
}

// SetAutoCompactionProgress sets a progress callback that will be used during
// the next auto-compaction run. It is safe to call from inside an event
// listener (uses a separate mutex from the listener list). Pass nil to clear.
func (s *AgentSession) SetAutoCompactionProgress(fn CompactionProgressFunc) {
	s.autoCompactProgressMu.Lock()
	s.autoCompactProgress = fn
	s.autoCompactProgressMu.Unlock()
}

// runAutoCompaction runs auto-compaction and emits the appropriate events.
func (s *AgentSession) runAutoCompaction(reason string, willRetry bool) {
	firlog.Info("auto-compaction triggered", "reason", reason, "willRetry", willRetry)
	info := s.compactionRunner.GetStats(s)

	s.emit(AgentSessionEvent{
		Type:             "auto_compaction_start",
		CompactionReason: reason,
		CompactionInfo:   info,
	})

	// Pick up any progress function set by the auto_compaction_start listener.
	s.autoCompactProgressMu.Lock()
	progressFn := s.autoCompactProgress
	s.autoCompactProgress = nil
	s.autoCompactProgressMu.Unlock()

	ctx := context.Background()
	if progressFn != nil {
		ctx = WithCompactionProgress(ctx, progressFn)
	}

	result, err := s.compactionRunner.RunCompaction(ctx, s, "")
	if err != nil {
		s.emit(AgentSessionEvent{
			Type:         "auto_compaction_end",
			ErrorMessage: err.Error(),
			Aborted:      false,
			WillRetry:    willRetry,
		})
		return
	}

	s.emit(AgentSessionEvent{
		Type:             "auto_compaction_end",
		CompactionResult: result,
		WillRetry:        willRetry,
	})

	// Resume the agent if there's pending work.
	// For overflow: the error message is stripped before retry.
	// For threshold: just continue from where we left off.
	if result != nil {
		state := s.State()
		msgs := state.Messages

		// If overflow, strip trailing error messages before retry
		if willRetry {
			for len(msgs) > 0 {
				last := msgs[len(msgs)-1]
				if last.Role() == "assistant" {
					if a := last.Message.AsAssistant(); a != nil && a.StopReason == ai.StopReasonError {
						msgs = msgs[:len(msgs)-1]
						continue
					}
				}
				break
			}
		}

		// Resume if there's pending work (user message or tool result waiting for response,
		// or pending tool calls that were in flight when compaction triggered).
		if len(state.PendingToolCalls) > 0 || (len(msgs) > 0 && (msgs[len(msgs)-1].Role() == "user" || msgs[len(msgs)-1].Role() == "toolResult")) {
			s.Agent.ReplaceMessages(msgs)
			go func() { _ = s.Agent.Continue() }()
		}
	}
}

// calculateContextTokens returns the total context tokens from a usage report.
// The last assistant message's usage.input represents the total context that was sent to the model.
func calculateContextTokens(usage ai.Usage) int {
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

// GetLatestCompactionEntry returns the most recent compaction entry in a session branch,
// or nil if none exists.
func GetLatestCompactionEntry(entries []*store.SessionEntry) *store.SessionEntry {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "compaction" {
			return entries[i]
		}
	}
	return nil
}

// RunCompaction runs manual compaction via the CompactionRunner.
// ctx may be cancelled to abort the in-flight LLM summarization request.
func (s *AgentSession) RunCompaction(ctx context.Context, customInstructions string) (*CompactionResultInfo, error) {
	if s.compactionRunner == nil {
		return nil, fmt.Errorf("compaction not configured")
	}
	return s.compactionRunner.RunCompaction(ctx, s, customInstructions)
}

// HasPendingWork reports whether the agent's current message list ends with
// an unanswered user message or an unprocessed tool result, meaning the agent
// was interrupted mid-task and can safely be resumed via Agent.Continue().
//
// The resumable cases are:
//   - "user"       — Escape was pressed before the first LLM response of that turn.
//   - "toolResult" — the agent had completed a tool call (persisted) but was then
//     interrupted while generating the follow-up LLM response.
//   - PendingToolCalls — the agent is executing tools and compaction happened mid-execution.
//
// "assistant" means the agent finished normally; "" means the last message is a
// custom type such as a compaction summary — neither warrants auto-resume.
func (s *AgentSession) HasPendingWork() bool {
	state := s.State()

	// Check for pending tool executions (agent is waiting for tool results)
	if len(state.PendingToolCalls) > 0 {
		return true
	}

	// Check for unanswered message waiting for response
	msgs := state.Messages
	if len(msgs) == 0 {
		return false
	}
	role := msgs[len(msgs)-1].Role()
	return role == "user" || role == "toolResult"
}

// GetCompactionStats returns pre-run statistics about what a compaction would
// process, without making any LLM calls. Returns nil if compaction is not
// configured or there is nothing to compact.
func (s *AgentSession) GetCompactionStats() *CompactionInfo {
	if s.compactionRunner == nil {
		return nil
	}
	return s.compactionRunner.GetStats(s)
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

// RecordCommand records a user-initiated command for audit/metering purposes.
// command is the command name without a leading slash (e.g. "model", "compact").
// args captures any relevant argument for metering (may be empty).
// These entries are never included in the LLM context.
func (s *AgentSession) RecordCommand(command, args string) {
	s.SessionManager.AppendCommandEntry(command, args)
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
func (s *AgentSession) ScopedModelsRef() []models.ScopedModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scopedModels
}

// SetScopedModels updates the session-only scoped model list used for Ctrl+P
// cycling. A nil or empty slice clears the filter (all available models cycle).
func (s *AgentSession) SetScopedModels(models []models.ScopedModel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopedModels = models
}

// SetHooks sets the extension hooks and wraps the agent's tools with hook interception.
// Can be called after creation. When hooks include OnToolCall or OnToolResult,
// the agent's current tools are automatically wrapped so the hooks fire during execution.
func (s *AgentSession) SetHooks(hooks *AgentSessionHooks) {
	s.hooks = hooks
	if hooks != nil && (hooks.OnToolCall != nil || hooks.OnToolResult != nil) {
		state := s.Agent.State()
		wrapped := s.WrapToolsWithHooks(state.Tools.Slice())
		s.Agent.SetTools(wrapped)
	}
}

// Hooks returns the current hooks (may be nil).
func (s *AgentSession) Hooks() *AgentSessionHooks {
	return s.hooks
}

// GetSystemPrompt returns the current base system prompt.
func (s *AgentSession) GetSystemPrompt() string {
	return s.baseSystemPrompt
}

// GetCwd returns the working directory.
func (s *AgentSession) GetCwd() string {
	return s.cwd
}

// WrapToolsWithHooks wraps the given tools with hook interception.
// Tool calls go through OnToolCall (which can block) and OnToolResult (which can modify results).
func (s *AgentSession) WrapToolsWithHooks(tools []agent.AgentTool) []agent.AgentTool {
	if s.hooks == nil {
		return tools
	}

	wrapped := make([]agent.AgentTool, len(tools))
	for i, t := range tools {
		wrapped[i] = s.wrapTool(t)
	}
	return wrapped
}

func (s *AgentSession) wrapTool(t agent.AgentTool) agent.AgentTool {
	origExecute := t.Execute
	if origExecute == nil {
		return t
	}

	t.Execute = func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
		// Pre-execution hook: tool_call interception
		if s.hooks != nil && s.hooks.OnToolCall != nil {
			block := s.hooks.OnToolCall(toolCallID, t.Name, params)
			if block != nil {
				reason := block.Reason
				if reason == "" {
					reason = "Blocked by extension"
				}
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: reason}},
				}, nil
			}
		}

		// Execute the original tool
		result, err := origExecute(ctx, toolCallID, params, onUpdate)

		// Post-execution hook: tool_result interception
		if err == nil && s.hooks != nil && s.hooks.OnToolResult != nil {
			mod := s.hooks.OnToolResult(toolCallID, t.Name, params, result.Content, result.Details, result.IsError)
			if mod != nil {
				if mod.Content != nil {
					result.Content = mod.Content
				}
				if mod.Details != nil {
					result.Details = mod.Details
				}
				if mod.IsError != nil {
					result.IsError = *mod.IsError
				}
			}
		}

		return result, err
	}

	return t
}

// SetSessionName sets the display name for the current session.
func (s *AgentSession) SetSessionName(name string) {
	s.SessionManager.AppendSessionInfo(name)
	s.emit(AgentSessionEvent{
		Type:        "session_named",
		SessionName: name,
	})
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
	// Clear plan state so stale plans don't persist across sessions.
	s.UpdatePlan("", nil, nil)
	// Clear the session name so extensions (e.g. tmuxspinner) reset the window title.
	s.emit(AgentSessionEvent{
		Type:        "session_named",
		SessionName: "",
	})
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

	// Restore session model if recorded and still available.
	// Use Agent.SetModel directly (not s.SetModel) to avoid writing a
	// redundant model_change entry back into the session we just loaded.
	if ctx.Model != nil && s.modelRegistry != nil {
		if restored := s.modelRegistry.Find(ctx.Model.Provider, ctx.Model.ModelID); restored != nil {
			s.Agent.SetModel(restored)
		}
	}

	// Restore thinking level from the session without writing a new
	// thinking_level_change entry (we are loading, not changing).
	if ctx.ThinkingLevel != "" {
		s.Agent.SetThinkingLevel(agent.ThinkingLevel(ctx.ThinkingLevel))
	}

	// Restore plan state from session without writing a new entry.
	s.restorePlan(ctx.PlanTitle, ctx.PlanEntries, ctx.PlanMetadata)

	// Rebuild system prompt
	s.buildSystemPrompt()
	s.Agent.SetSystemPrompt(s.baseSystemPrompt)

	// Emit session_named so extensions (e.g. tmuxspinner) update the window title.
	// Always emit, even with an empty name, so the old name is cleared.
	s.emit(AgentSessionEvent{
		Type:        "session_named",
		SessionName: s.SessionManager.GetSessionName(),
	})

	return nil
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
	// Re-read settings from disk first so that resource loader and
	// callers see updated values (e.g., changed extension lists).
	if s.SettingsManager != nil {
		s.SettingsManager.Reload()
	}

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
func (s *AgentSession) ExecuteBash(command string, onChunk func(string)) (exec.BashResult, error) {
	return s.ExecuteBashWithOptions(command, onChunk, false)
}

// ExecuteBashWithOptions executes a bash command with optional streaming, cancellation,
// and the option to exclude the result from context.
func (s *AgentSession) ExecuteBashWithOptions(command string, onChunk func(string), excludeFromContext bool) (exec.BashResult, error) {
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

	result, err := exec.ExecuteBash(ctx, resolvedCommand, &exec.BashExecutorOptions{
		OnChunk: onChunk,
	})
	if err != nil {
		return result, err
	}

	// Record in session
	exitCode := result.ExitCode
	bashMsg := &store.BashExecutionMessage{
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

// ContextUsage holds the current context usage information.
type ContextUsage struct {
	// Tokens is the estimated context tokens, or -1 if unknown (e.g. right after compaction).
	Tokens int
	// ContextWindow is the model's context window size.
	ContextWindow int
	// Percent is the usage percentage (0-100+), or -1 if unknown.
	Percent float64
}

// GetContextUsage returns the current context usage estimate.
// Returns nil if no model is selected.
func (s *AgentSession) GetContextUsage() *ContextUsage {
	model := s.Model()
	if model == nil {
		return nil
	}

	contextWindow := model.ContextWindow
	if contextWindow <= 0 {
		return nil
	}

	// After compaction, the last assistant usage reflects pre-compaction context size.
	// We can only trust usage from an assistant that responded after the latest compaction.
	// If no such assistant exists, context token count is unknown until the next LLM response.
	branchEntries := s.SessionManager.GetBranch("")
	latestCompaction := GetLatestCompactionEntry(branchEntries)

	if latestCompaction != nil {
		hasPostCompactionUsage := false

		// Find compaction index in branch entries
		compactionIndex := -1
		for i := len(branchEntries) - 1; i >= 0; i-- {
			if branchEntries[i] == latestCompaction {
				compactionIndex = i
				break
			}
		}

		// Look for a valid assistant usage after the compaction entry
		for i := len(branchEntries) - 1; i > compactionIndex; i-- {
			entry := branchEntries[i]
			if entry.Type == "message" && len(entry.RawMessage) > 0 {
				var probe struct {
					Role       string   `json:"role"`
					StopReason string   `json:"stopReason"`
					Usage      ai.Usage `json:"usage"`
				}
				if json.Unmarshal(entry.RawMessage, &probe) == nil && probe.Role == "assistant" {
					if probe.StopReason != string(ai.StopReasonAborted) && probe.StopReason != string(ai.StopReasonError) {
						if calculateContextTokens(probe.Usage) > 0 {
							hasPostCompactionUsage = true
						}
					}
					break
				}
			}
		}

		if !hasPostCompactionUsage {
			return &ContextUsage{Tokens: -1, ContextWindow: contextWindow, Percent: -1}
		}
	}

	state := s.State()
	tokens := estimateContextTokensFromMessages(state.Messages)
	percent := float64(tokens) / float64(contextWindow) * 100

	return &ContextUsage{
		Tokens:        tokens,
		ContextWindow: contextWindow,
		Percent:       percent,
	}
}

// estimateContextTokensFromMessages estimates the total context tokens from messages.
// Uses the last valid assistant message's usage data plus trailing message estimates.
// This matches the TS estimateContextTokens function.
func estimateContextTokensFromMessages(messages []agent.AgentMessage) int {
	// Find last assistant message with valid usage
	var lastUsage *ai.Usage
	lastIndex := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role() == "assistant" {
			a := messages[i].Message.AsAssistant()
			if a != nil && a.StopReason != ai.StopReasonAborted && a.StopReason != ai.StopReasonError {
				lastUsage = &a.Usage
				lastIndex = i
				break
			}
		}
	}

	if lastUsage == nil {
		// No valid assistant usage — estimate all messages via chars/4
		total := 0
		for _, msg := range messages {
			total += estimateMessageTokens(msg)
		}
		return total
	}

	usageTokens := calculateContextTokens(*lastUsage)
	trailingTokens := 0
	for i := lastIndex + 1; i < len(messages); i++ {
		trailingTokens += estimateMessageTokens(messages[i])
	}
	return usageTokens + trailingTokens
}

// estimateMessageTokens estimates token count for a single message using chars/4 heuristic.
func estimateMessageTokens(msg agent.AgentMessage) int {
	chars := 0
	switch {
	case msg.Message.AsUser() != nil:
		u := msg.Message.AsUser()
		if s, ok := u.Content.(string); ok {
			chars = len(s)
		}
	case msg.Message.AsAssistant() != nil:
		a := msg.Message.AsAssistant()
		for _, c := range a.Content {
			if c.Text != nil {
				chars += len(c.Text.Text)
			}
			if c.Thinking != nil {
				chars += len(c.Thinking.Thinking)
			}
			if c.ToolCall != nil {
				// Estimate by serializing arguments
				if argBytes, err := json.Marshal(c.ToolCall.Arguments); err == nil {
					chars += len(argBytes)
				}
				chars += len(c.ToolCall.Name)
			}
		}
	case msg.Message.AsToolResult() != nil:
		tr := msg.Message.AsToolResult()
		for _, c := range tr.Content {
			chars += len(c.Text)
		}
	}
	return (chars + 3) / 4 // chars/4, rounded up
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
	s.restorePlan(ctx.PlanTitle, ctx.PlanEntries, ctx.PlanMetadata)

	// Find user message text at this entry for editor pre-fill
	entry := s.SessionManager.GetEntry(entryID)
	var editorText string
	if entry != nil && entry.Type == "message" {
		editorText = extractUserMessageText(entry.RawMessage)
	}

	return &NavigateTreeResult{EditorText: editorText}, nil
}

// SideQuery makes a one-shot, ephemeral LLM call using the current session
// context plus the given question. No tools are provided and nothing is added
// to the session history. Delegates to Agent.SimplePrompt which reuses the
// agent's streamFn, api key resolution, and transport config.
func (s *AgentSession) SideQuery(ctx context.Context, question string) (string, error) {
	// Snapshot current messages.
	state := s.Agent.State()
	msgs := make([]agent.AgentMessage, len(state.Messages))
	copy(msgs, state.Messages)

	// Append the side question.
	msgs = append(msgs, agent.NewAgentMessage(ai.NewUserMsg(question, time.Now().UnixMilli())))

	return s.Agent.SimplePrompt(ctx, msgs)
}

// Close cleans up the session.
func (s *AgentSession) Close() {
	if s.unsubAgent != nil {
		s.unsubAgent()
	}
}

// RegisterSessionTools appends tools that require a session reference
// (e.g. the plan tool) to the agent's current tool set.
func (s *AgentSession) RegisterSessionTools() {
	state := s.Agent.State()
	existing := state.Tools.Slice()
	allTools := make([]agent.AgentTool, len(existing)+1)
	copy(allTools, existing)
	allTools[len(existing)] = tools.NewPlanTool(s)
	s.Agent.SetTools(allTools)
}
