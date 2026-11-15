package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
)

// Client-provided system prompt (stable ACP capability
// agentCapabilities.sessionCapabilities.systemPrompt). The client sends
// params.systemPrompt = {mode, content} on session/new and re-sends it on
// session/load, session/resume and session/fork. fir supports mode
// "append" only (no mode.override advertised) and applies the text as the
// session's appended system prompt, replacing any earlier client prompt.

const systemPromptModeAppend = "append"

// systemPromptParam is params.systemPrompt. The SDK has no type for it yet.
type systemPromptParam struct {
	Mode    string                `json:"mode"`
	Content []acpsdk.ContentBlock `json:"content"`
}

// systemPromptCapability is advertised under sessionCapabilities.systemPrompt.
func systemPromptCapability() map[string]any { return map[string]any{} }

// parseSystemPrompt extracts params.systemPrompt from raw request params.
// present is false when the field is absent or null. The prompt text is the
// content's text blocks joined by blank lines.
func parseSystemPrompt(params json.RawMessage) (text string, present bool, err error) {
	var env struct {
		SystemPrompt *systemPromptParam `json:"systemPrompt"`
	}
	if err := json.Unmarshal(params, &env); err != nil {
		return "", false, err
	}
	sp := env.SystemPrompt
	if sp == nil {
		return "", false, nil
	}
	if sp.Mode != systemPromptModeAppend {
		return "", false, fmt.Errorf("systemPrompt.mode %q not supported (only %q)", sp.Mode, systemPromptModeAppend)
	}
	if len(sp.Content) == 0 {
		return "", false, fmt.Errorf("systemPrompt.content must not be empty")
	}
	var parts []string
	for _, b := range sp.Content {
		if b.Text == nil {
			return "", false, fmt.Errorf("systemPrompt.content: only text blocks are supported")
		}
		parts = append(parts, b.Text.Text)
	}
	return strings.Join(parts, "\n\n"), true, nil
}

// applySystemPrompt sets the client system prompt on a live session.
func (pa *firAgent) applySystemPrompt(sessionID, text string) {
	pa.mu.Lock()
	entry, ok := pa.sessions[sessionID]
	pa.mu.Unlock()
	if ok && entry.session != nil {
		entry.session.SetClientSystemPrompt(text)
	}
}
