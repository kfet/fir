// Ported from: packages/coding-agent/src/core/compaction/compaction.ts
// Upstream hash: 1caadb2e
package compaction

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// ============================================================================
// Summarization prompts
// ============================================================================

const summarizationPromptText = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Note on elided observations: older or large tool outputs in the conversation may appear as ` + "`[entry <id> tool=<name> bytes=<n> ...]`" + ` stubs. Treat these as references — do not invent content for them. If continuing the work needs the actual output, the next agent should re-run the command or re-` + "`read`" + ` the file rather than guess.

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

## Working Set
- ` + "`path/to/file.go`" + ` — [one-line status: what role this file plays now, what was changed, what's pending]
- [Add one bullet per actively-touched file. Pull paths from the conversation. Omit files that are merely referenced but not part of the active change.]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummarizationPromptText = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- BOUND the "Done" list to the most recent ~20 items. Older completed items may be summarized into a single "Earlier (summarized): ..." bullet so the list does not grow without bound across many compactions.
- If something is no longer relevant, you may remove it

Note on elided observations: older or large tool outputs may appear as ` + "`[entry <id> tool=<name> bytes=<n> ...]`" + ` stubs. Treat these as references — do not invent content. To recover, re-run the command or re-` + "`read`" + ` the file.

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Most recent ~20 completed items; older items rolled up into a single "Earlier (summarized)" bullet]

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

## Working Set
- ` + "`path/to/file.go`" + ` — [one-line status: current role, recent change, pending work]
- [Carry forward existing entries; update statuses based on new messages; add bullets for newly-touched files; drop files that are no longer in flight]

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

	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n"
	if previousSummary != "" {
		promptText += "<previous-summary>\n" + previousSummary + "\n</previous-summary>\n\n"
	}
	// Promote user-supplied focus to a first-class section ABOVE the
	// format spec so it carries weight comparable to the spec itself.
	// (Phase 1 #10 of the compaction rework.)
	if customInstructions != "" {
		promptText += "<user-focus>\nThe user supplied the following focus for this compaction. Treat it as a hard requirement; the structured format below is the medium, this focus is the priority.\n\n" + customInstructions + "\n</user-focus>\n\n"
	}
	promptText += basePrompt
	return promptText
}

// ============================================================================
// Summary generation
// ============================================================================

// convertWithIDs is like store.ConvertToLLM but preserves a parallel
// slice of session-store entry IDs (one per output message). Some inputs
// (e.g. BashExecutionMessage with ExcludeFromContext) drop out, so the
// output may be shorter than the input — the returned ID slice stays
// aligned with the messages slice.
func convertWithIDs(messages []agent.AgentMessage, entryIDs []string) ([]ai.Message, []string, error) {
	out := make([]ai.Message, 0, len(messages))
	outIDs := make([]string, 0, len(messages))
	for i, m := range messages {
		converted, err := store.ConvertToLLM([]agent.AgentMessage{m})
		if err != nil {
			return nil, nil, err
		}
		id := ""
		if i < len(entryIDs) {
			id = entryIDs[i]
		}
		for _, x := range converted {
			out = append(out, x)
			outIDs = append(outIDs, id)
		}
	}
	return out, outIDs, nil
}

// GenerateSummary generates a summary of the conversation using the LLM.
// If the context carries a CompactionProgressFunc (via core.WithCompactionProgress),
// it is called with phase="summarizing history" and each text delta as the LLM streams.
func GenerateSummary(
	ctx context.Context,
	registry *ai.Registry,
	currentMessages []agent.AgentMessage,
	entryIDs []string,
	model *ai.Model,
	reserveTokens int,
	apiKey string,
	customInstructions string,
	previousSummary string,
) (string, error) {
	maxTokens := int(0.8 * float64(reserveTokens))

	llmMessages, llmIDs, err := convertWithIDs(currentMessages, entryIDs)
	if err != nil {
		return "", fmt.Errorf("convert messages: %w", err)
	}
	conversationText := SerializeConversationWithIDs(llmMessages, llmIDs, DefaultStubOptions)
	promptText := BuildSummarizationPrompt(conversationText, previousSummary, customInstructions)

	progress := session.CompactionProgressFromCtx(ctx)
	if progress != nil {
		progress("summarizing history", "")
	}

	stream := ai.StreamSimple(ctx, registry, model, ai.Context{
		SystemPrompt: SummarizationSystemPrompt,
		Messages:     []ai.Message{ai.NewUserMsg(promptText, time.Now().UnixMilli())},
	}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			MaxTokens: &maxTokens,
			APIKey:    apiKey,
		},
		Reasoning: ai.ThinkingHigh,
	})

	var prevTextLen int
	for event := range stream.Events {
		if event.Type == ai.EventTextDelta && event.Partial != nil && progress != nil {
			current := extractTextFromResponse(event.Partial)
			if len(current) > prevTextLen {
				progress("summarizing history", current[prevTextLen:])
				prevTextLen = len(current)
			}
		}
	}

	response := stream.Result()
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
	entryIDs []string,
	model *ai.Model,
	reserveTokens int,
	apiKey string,
) (string, error) {
	maxTokens := int(0.5 * float64(reserveTokens))

	llmMessages, llmIDs, err := convertWithIDs(messages, entryIDs)
	if err != nil {
		return "", fmt.Errorf("convert messages: %w", err)
	}
	conversationText := SerializeConversationWithIDs(llmMessages, llmIDs, DefaultStubOptions)
	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n" + turnPrefixSummarizationPromptText

	progress := session.CompactionProgressFromCtx(ctx)
	if progress != nil {
		progress("summarizing turn context", "")
	}

	stream := ai.StreamSimple(ctx, registry, model, ai.Context{
		SystemPrompt: SummarizationSystemPrompt,
		Messages:     []ai.Message{ai.NewUserMsg(promptText, time.Now().UnixMilli())},
	}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			MaxTokens: &maxTokens,
			APIKey:    apiKey,
		},
	})

	var prevTextLen int
	for event := range stream.Events {
		if event.Type == ai.EventTextDelta && event.Partial != nil && progress != nil {
			current := extractTextFromResponse(event.Partial)
			if len(current) > prevTextLen {
				progress("summarizing turn context", current[prevTextLen:])
				prevTextLen = len(current)
			}
		}
	}

	response := stream.Result()
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
