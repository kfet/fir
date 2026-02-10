// Ported from: packages/coding-agent/src/core/compaction/compaction.ts
// Upstream hash: 1caadb2e
package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/core"
)

// ============================================================================
// Types
// ============================================================================

// SessionEntry is the compaction-local representation of a session entry.
type SessionEntry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  string          `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message,omitempty"`

	// compaction fields
	Summary          string          `json:"summary,omitempty"`
	FirstKeptEntryID string          `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int             `json:"tokensBefore,omitempty"`
	Details          json.RawMessage `json:"details,omitempty"`
	FromHook         bool            `json:"fromHook,omitempty"`

	// branch_summary
	FromID string `json:"fromId,omitempty"`

	// custom / custom_message
	CustomType string          `json:"customType,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Display    bool            `json:"display,omitempty"`
}

// CompactionDetails are stored in CompactionEntry.Details for file tracking.
type CompactionDetails struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

// CompactionSettings controls compaction behavior.
type CompactionSettings struct {
	Enabled          bool `json:"enabled"`
	ReserveTokens    int  `json:"reserveTokens"`
	KeepRecentTokens int  `json:"keepRecentTokens"`
}

// DefaultCompactionSettings are the default compaction settings.
var DefaultCompactionSettings = CompactionSettings{
	Enabled:          true,
	ReserveTokens:    16384,
	KeepRecentTokens: 20000,
}

// CompactionResult is the result of a compaction operation.
type CompactionResult struct {
	Summary          string `json:"summary"`
	FirstKeptEntryID string `json:"firstKeptEntryId"`
	TokensBefore     int    `json:"tokensBefore"`
	Details          any    `json:"details,omitempty"`
}

// CompactionPreparation contains pre-calculated data for compaction.
type CompactionPreparation struct {
	FirstKeptEntryID    string
	MessagesToSummarize []agent.AgentMessage
	TurnPrefixMessages  []agent.AgentMessage
	IsSplitTurn         bool
	TokensBefore        int
	PreviousSummary     string
	FileOps             *FileOperations
	Settings            CompactionSettings
}

// CutPointResult describes where to cut the session for compaction.
type CutPointResult struct {
	FirstKeptEntryIndex int
	TurnStartIndex      int
	IsSplitTurn         bool
}

// ContextUsageEstimate holds token estimation results.
type ContextUsageEstimate struct {
	Tokens         int
	UsageTokens    int
	TrailingTokens int
	LastUsageIndex *int
}

// ============================================================================
// Token calculation
// ============================================================================

// CalculateContextTokens calculates total context tokens from usage.
func CalculateContextTokens(usage ai.Usage) int {
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

// getAssistantUsage returns usage from an assistant message if valid.
func getAssistantUsage(msg agent.AgentMessage) *ai.Usage {
	if msg.Role() != "assistant" {
		return nil
	}
	a := msg.Message.AsAssistant()
	if a == nil {
		return nil
	}
	if a.StopReason == ai.StopReasonAborted || a.StopReason == ai.StopReasonError {
		return nil
	}
	return &a.Usage
}

// EstimateContextTokens estimates context tokens from messages.
func EstimateContextTokens(messages []agent.AgentMessage) ContextUsageEstimate {
	var lastUsage *ai.Usage
	var lastIndex *int
	for i := len(messages) - 1; i >= 0; i-- {
		if u := getAssistantUsage(messages[i]); u != nil {
			lastUsage = u
			idx := i
			lastIndex = &idx
			break
		}
	}

	if lastUsage == nil {
		estimated := 0
		for _, msg := range messages {
			estimated += EstimateTokens(msg)
		}
		return ContextUsageEstimate{
			Tokens:         estimated,
			TrailingTokens: estimated,
		}
	}

	usageTokens := CalculateContextTokens(*lastUsage)
	trailingTokens := 0
	for i := *lastIndex + 1; i < len(messages); i++ {
		trailingTokens += EstimateTokens(messages[i])
	}

	return ContextUsageEstimate{
		Tokens:         usageTokens + trailingTokens,
		UsageTokens:    usageTokens,
		TrailingTokens: trailingTokens,
		LastUsageIndex: lastIndex,
	}
}

// ShouldCompact checks if compaction should trigger.
func ShouldCompact(contextTokens, contextWindow int, settings CompactionSettings) bool {
	if !settings.Enabled {
		return false
	}
	return contextTokens > contextWindow-settings.ReserveTokens
}

// ============================================================================
// Token estimation
// ============================================================================

// EstimateTokens estimates token count for a message using chars/4 heuristic.
func EstimateTokens(message agent.AgentMessage) int {
	chars := 0

	// Handle custom message types
	if message.Custom != nil {
		switch msg := message.Custom.(type) {
		case *core.BashExecutionMessage:
			chars = len(msg.Command) + len(msg.Output)
		case *core.BranchSummaryMessage:
			chars = len(msg.Summary)
		case *core.CompactionSummaryMessage:
			chars = len(msg.Summary)
		case *core.CustomMessage:
			if s, ok := msg.Content.(string); ok {
				chars = len(s)
			}
		}
		return int(math.Ceil(float64(chars) / 4))
	}

	role := message.Role()
	switch role {
	case "user":
		u := message.Message.AsUser()
		if u == nil {
			return 0
		}
		switch content := u.Content.(type) {
		case string:
			chars = len(content)
		case []any:
			for _, block := range content {
				if m, ok := block.(map[string]any); ok {
					if m["type"] == "text" {
						if t, ok := m["text"].(string); ok {
							chars += len(t)
						}
					}
				}
			}
		}

	case "assistant":
		a := message.Message.AsAssistant()
		if a == nil {
			return 0
		}
		for _, block := range a.Content {
			if block.Text != nil {
				chars += len(block.Text.Text)
			} else if block.Thinking != nil {
				chars += len(block.Thinking.Thinking)
			} else if block.ToolCall != nil {
				chars += len(block.ToolCall.Name)
				argsJSON, _ := json.Marshal(block.ToolCall.Arguments)
				chars += len(argsJSON)
			}
		}

	case "toolResult":
		tr := message.Message.AsToolResult()
		if tr == nil {
			return 0
		}
		for _, c := range tr.Content {
			if c.IsText() {
				chars += len(c.Text)
			}
			if c.IsImage() {
				chars += 4800
			}
		}
	}

	return int(math.Ceil(float64(chars) / 4))
}

// ============================================================================
// Cut point detection
// ============================================================================

func getMessageRole(entry *core.SessionEntry) string {
	if entry.Type != "message" || len(entry.RawMessage) == 0 {
		return ""
	}
	var probe struct {
		Role string `json:"role"`
	}
	json.Unmarshal(entry.RawMessage, &probe)
	return probe.Role
}

func findValidCutPoints(entries []*core.SessionEntry, startIndex, endIndex int) []int {
	var cutPoints []int
	for i := startIndex; i < endIndex; i++ {
		entry := entries[i]
		switch entry.Type {
		case "message":
			role := getMessageRole(entry)
			switch role {
			case "bashExecution", "custom", "branchSummary", "compactionSummary", "user", "assistant":
				cutPoints = append(cutPoints, i)
			}
		case "branch_summary", "custom_message":
			cutPoints = append(cutPoints, i)
		}
	}
	return cutPoints
}

// FindTurnStartIndex finds the user message that starts the turn containing the given entry.
func FindTurnStartIndex(entries []*core.SessionEntry, entryIndex, startIndex int) int {
	for i := entryIndex; i >= startIndex; i-- {
		entry := entries[i]
		if entry.Type == "branch_summary" || entry.Type == "custom_message" {
			return i
		}
		if entry.Type == "message" {
			role := getMessageRole(entry)
			if role == "user" || role == "bashExecution" {
				return i
			}
		}
	}
	return -1
}

// FindCutPoint finds where to cut the session for compaction.
func FindCutPoint(entries []*core.SessionEntry, startIndex, endIndex, keepRecentTokens int) CutPointResult {
	cutPoints := findValidCutPoints(entries, startIndex, endIndex)

	if len(cutPoints) == 0 {
		return CutPointResult{FirstKeptEntryIndex: startIndex, TurnStartIndex: -1}
	}

	accumulatedTokens := 0
	cutIndex := cutPoints[0]

	for i := endIndex - 1; i >= startIndex; i-- {
		entry := entries[i]
		if entry.Type != "message" || len(entry.RawMessage) == 0 {
			continue
		}
		var msg ai.Message
		if err := json.Unmarshal(entry.RawMessage, &msg); err != nil {
			continue
		}
		messageTokens := EstimateTokens(agent.NewAgentMessage(msg))
		accumulatedTokens += messageTokens

		if accumulatedTokens >= keepRecentTokens {
			for c := 0; c < len(cutPoints); c++ {
				if cutPoints[c] >= i {
					cutIndex = cutPoints[c]
					break
				}
			}
			break
		}
	}

	// Scan backwards to include non-message entries
	for cutIndex > startIndex {
		prev := entries[cutIndex-1]
		if prev.Type == "compaction" || prev.Type == "message" {
			break
		}
		cutIndex--
	}

	isUserMsg := getMessageRole(entries[cutIndex]) == "user"
	turnStartIndex := -1
	if !isUserMsg {
		turnStartIndex = FindTurnStartIndex(entries, cutIndex, startIndex)
	}

	return CutPointResult{
		FirstKeptEntryIndex: cutIndex,
		TurnStartIndex:      turnStartIndex,
		IsSplitTurn:         !isUserMsg && turnStartIndex != -1,
	}
}

// ============================================================================
// Message extraction from entries
// ============================================================================

func getMessageFromEntry(entry *core.SessionEntry) *agent.AgentMessage {
	switch entry.Type {
	case "message":
		if len(entry.RawMessage) == 0 {
			return nil
		}
		var msg ai.Message
		if err := json.Unmarshal(entry.RawMessage, &msg); err != nil {
			return nil
		}
		am := agent.NewAgentMessage(msg)
		return &am

	case "custom_message":
		if len(entry.Content) > 0 && entry.CustomType != "" {
			ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
			var content any
			json.Unmarshal(entry.Content, &content)
			cm := &core.CustomMessage{
				Role:       "custom",
				CustomType: entry.CustomType,
				Content:    content,
				Display:    entry.Display,
				Timestamp:  ts.UnixMilli(),
			}
			am := agent.AgentMessage{Custom: cm}
			return &am
		}

	case "branch_summary":
		if entry.Summary != "" {
			ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
			am := core.CreateBranchSummaryMessage(entry.Summary, entry.FromID, ts)
			return &am
		}

	case "compaction":
		if entry.Summary != "" {
			ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
			am := core.CreateCompactionSummaryMessage(entry.Summary, entry.TokensBefore, ts)
			return &am
		}
	}

	return nil
}

// ============================================================================
// File operation extraction
// ============================================================================

func extractFileOperations(messages []agent.AgentMessage, entries []*core.SessionEntry, prevCompactionIndex int) *FileOperations {
	fileOps := NewFileOperations()

	if prevCompactionIndex >= 0 {
		prev := entries[prevCompactionIndex]
		if prev.Type == "compaction" && !prev.FromHook && len(prev.Details) > 0 {
			var details CompactionDetails
			if err := json.Unmarshal(prev.Details, &details); err == nil {
				for _, f := range details.ReadFiles {
					fileOps.Read[f] = struct{}{}
				}
				for _, f := range details.ModifiedFiles {
					fileOps.Edited[f] = struct{}{}
				}
			}
		}
	}

	for _, msg := range messages {
		ExtractFileOpsFromMessage(msg, fileOps)
	}

	return fileOps
}

// ============================================================================
// Compaction preparation
// ============================================================================

// PrepareCompaction prepares compaction data from session entries.
func PrepareCompaction(pathEntries []*core.SessionEntry, settings CompactionSettings) *CompactionPreparation {
	if len(pathEntries) > 0 && pathEntries[len(pathEntries)-1].Type == "compaction" {
		return nil
	}

	prevCompactionIndex := -1
	for i := len(pathEntries) - 1; i >= 0; i-- {
		if pathEntries[i].Type == "compaction" {
			prevCompactionIndex = i
			break
		}
	}

	boundaryStart := prevCompactionIndex + 1
	boundaryEnd := len(pathEntries)

	usageStart := prevCompactionIndex
	if usageStart < 0 {
		usageStart = 0
	}
	var usageMessages []agent.AgentMessage
	for i := usageStart; i < boundaryEnd; i++ {
		if msg := getMessageFromEntry(pathEntries[i]); msg != nil {
			usageMessages = append(usageMessages, *msg)
		}
	}
	tokensBefore := EstimateContextTokens(usageMessages).Tokens

	cutPoint := FindCutPoint(pathEntries, boundaryStart, boundaryEnd, settings.KeepRecentTokens)

	firstKeptEntry := pathEntries[cutPoint.FirstKeptEntryIndex]
	if firstKeptEntry.ID == "" {
		return nil
	}

	historyEnd := cutPoint.FirstKeptEntryIndex
	if cutPoint.IsSplitTurn {
		historyEnd = cutPoint.TurnStartIndex
	}

	var messagesToSummarize []agent.AgentMessage
	for i := boundaryStart; i < historyEnd; i++ {
		if msg := getMessageFromEntry(pathEntries[i]); msg != nil {
			messagesToSummarize = append(messagesToSummarize, *msg)
		}
	}

	var turnPrefixMessages []agent.AgentMessage
	if cutPoint.IsSplitTurn {
		for i := cutPoint.TurnStartIndex; i < cutPoint.FirstKeptEntryIndex; i++ {
			if msg := getMessageFromEntry(pathEntries[i]); msg != nil {
				turnPrefixMessages = append(turnPrefixMessages, *msg)
			}
		}
	}

	var previousSummary string
	if prevCompactionIndex >= 0 {
		previousSummary = pathEntries[prevCompactionIndex].Summary
	}

	fileOps := extractFileOperations(messagesToSummarize, pathEntries, prevCompactionIndex)
	if cutPoint.IsSplitTurn {
		for _, msg := range turnPrefixMessages {
			ExtractFileOpsFromMessage(msg, fileOps)
		}
	}

	return &CompactionPreparation{
		FirstKeptEntryID:    firstKeptEntry.ID,
		MessagesToSummarize: messagesToSummarize,
		TurnPrefixMessages:  turnPrefixMessages,
		IsSplitTurn:         cutPoint.IsSplitTurn,
		TokensBefore:        tokensBefore,
		PreviousSummary:     previousSummary,
		FileOps:             fileOps,
		Settings:            settings,
	}
}

// ============================================================================
// Summarization prompts
// ============================================================================

const summarizationPromptText = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummarizationPromptText = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const turnPrefixSummarizationPromptText = `This is the PREFIX of a turn that was too large to keep. The SUFFIX (recent work) is retained.

Summarize the prefix to provide context for the retained suffix:

## Original Request
[What did the user ask for in this turn?]

## Early Progress
- [Key decisions and work done in the prefix]

## Context for Suffix
- [Information needed to understand the retained recent work]

Be concise. Focus on what's needed to understand the kept suffix.`

// BuildSummarizationPrompt builds the prompt for summarization.
func BuildSummarizationPrompt(conversationText, previousSummary, customInstructions string) string {
	basePrompt := summarizationPromptText
	if previousSummary != "" {
		basePrompt = updateSummarizationPromptText
	}
	if customInstructions != "" {
		basePrompt = basePrompt + "\n\nAdditional focus: " + customInstructions
	}

	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n"
	if previousSummary != "" {
		promptText += "<previous-summary>\n" + previousSummary + "\n</previous-summary>\n\n"
	}
	promptText += basePrompt
	return promptText
}

// ============================================================================
// Summary generation
// ============================================================================

// GenerateSummary generates a summary of the conversation using the LLM.
func GenerateSummary(
	ctx context.Context,
	registry *ai.Registry,
	currentMessages []agent.AgentMessage,
	model *ai.Model,
	reserveTokens int,
	apiKey string,
	customInstructions string,
	previousSummary string,
) (string, error) {
	maxTokens := int(0.8 * float64(reserveTokens))

	llmMessages, err := core.ConvertToLLM(currentMessages)
	if err != nil {
		return "", fmt.Errorf("convert messages: %w", err)
	}
	conversationText := SerializeConversation(llmMessages)
	promptText := BuildSummarizationPrompt(conversationText, previousSummary, customInstructions)

	response := ai.CompleteSimple(ctx, registry, model, ai.Context{
		SystemPrompt: SummarizationSystemPrompt,
		Messages:     []ai.Message{ai.NewUserMsg(promptText, time.Now().UnixMilli())},
	}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			MaxTokens: &maxTokens,
			ApiKey:    apiKey,
		},
		Reasoning: ai.ThinkingHigh,
	})

	if response == nil {
		return "", fmt.Errorf("no response from summarization")
	}
	if response.StopReason == ai.StopReasonError {
		return "", fmt.Errorf("summarization failed: %s", response.ErrorMessage)
	}

	return extractTextFromResponse(response), nil
}

func generateTurnPrefixSummary(
	ctx context.Context,
	registry *ai.Registry,
	messages []agent.AgentMessage,
	model *ai.Model,
	reserveTokens int,
	apiKey string,
) (string, error) {
	maxTokens := int(0.5 * float64(reserveTokens))

	llmMessages, err := core.ConvertToLLM(messages)
	if err != nil {
		return "", fmt.Errorf("convert messages: %w", err)
	}
	conversationText := SerializeConversation(llmMessages)
	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n" + turnPrefixSummarizationPromptText

	response := ai.CompleteSimple(ctx, registry, model, ai.Context{
		SystemPrompt: SummarizationSystemPrompt,
		Messages:     []ai.Message{ai.NewUserMsg(promptText, time.Now().UnixMilli())},
	}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			MaxTokens: &maxTokens,
			ApiKey:    apiKey,
		},
	})

	if response == nil {
		return "", fmt.Errorf("no response from turn prefix summarization")
	}
	if response.StopReason == ai.StopReasonError {
		return "", fmt.Errorf("turn prefix summarization failed: %s", response.ErrorMessage)
	}

	return extractTextFromResponse(response), nil
}

func extractTextFromResponse(response *ai.AssistantMessage) string {
	var textParts []string
	for _, c := range response.Content {
		if c.Text != nil {
			textParts = append(textParts, c.Text.Text)
		}
	}
	return strings.Join(textParts, "\n")
}

// ============================================================================
// Main compaction function
// ============================================================================

// Compact generates summaries for compaction using prepared data.
func Compact(
	ctx context.Context,
	registry *ai.Registry,
	preparation *CompactionPreparation,
	model *ai.Model,
	apiKey string,
	customInstructions string,
) (*CompactionResult, error) {
	var summary string

	if preparation.IsSplitTurn && len(preparation.TurnPrefixMessages) > 0 {
		var historyResult string
		var err error
		if len(preparation.MessagesToSummarize) > 0 {
			historyResult, err = GenerateSummary(
				ctx, registry, preparation.MessagesToSummarize, model,
				preparation.Settings.ReserveTokens, apiKey,
				customInstructions, preparation.PreviousSummary,
			)
			if err != nil {
				return nil, err
			}
		} else {
			historyResult = "No prior history."
		}

		turnPrefixResult, err := generateTurnPrefixSummary(
			ctx, registry, preparation.TurnPrefixMessages, model,
			preparation.Settings.ReserveTokens, apiKey,
		)
		if err != nil {
			return nil, err
		}

		summary = historyResult + "\n\n---\n\n**Turn Context (split turn):**\n\n" + turnPrefixResult
	} else {
		var err error
		summary, err = GenerateSummary(
			ctx, registry, preparation.MessagesToSummarize, model,
			preparation.Settings.ReserveTokens, apiKey,
			customInstructions, preparation.PreviousSummary,
		)
		if err != nil {
			return nil, err
		}
	}

	readFiles, modifiedFiles := ComputeFileLists(preparation.FileOps)
	summary += FormatFileOperations(readFiles, modifiedFiles)

	return &CompactionResult{
		Summary:          summary,
		FirstKeptEntryID: preparation.FirstKeptEntryID,
		TokensBefore:     preparation.TokensBefore,
		Details:          &CompactionDetails{ReadFiles: readFiles, ModifiedFiles: modifiedFiles},
	}, nil
}
