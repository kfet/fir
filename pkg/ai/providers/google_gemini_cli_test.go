package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kfet/tau/pkg/ai"
)

func TestExtractRetryDelay_Headers(t *testing.T) {
	// Retry-After header (seconds)
	h := http.Header{}
	h.Set("Retry-After", "5")
	got := extractRetryDelay("", h)
	if got != 6000 { // 5000 + 1000
		t.Errorf("expected 6000, got %d", got)
	}

	// x-ratelimit-reset-after header
	h2 := http.Header{}
	h2.Set("x-ratelimit-reset-after", "3.5")
	got2 := extractRetryDelay("", h2)
	if got2 != 4500 { // 3500 + 1000
		t.Errorf("expected 4500, got %d", got2)
	}
}

func TestExtractRetryDelay_ErrorText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"reset after seconds", "Your quota will reset after 39s", 40000},
		{"reset after hours minutes seconds", "Your quota will reset after 1h2m3s", (3600+120+3)*1000 + 1000},
		{"retry in seconds", "Please retry in 5s", 6000},
		{"retry in ms", "Please retry in 500ms", 1500},
		{"retryDelay JSON", `"retryDelay": "10.5s"`, 11500},
		{"no match", "some random error", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRetryDelay(tt.text, nil)
			if got != tt.want {
				t.Errorf("extractRetryDelay(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

func TestIsRetryableError(t *testing.T) {
	if !isRetryableError(429, "") {
		t.Error("429 should be retryable")
	}
	if !isRetryableError(503, "") {
		t.Error("503 should be retryable")
	}
	if !isRetryableError(200, "resource exhausted") {
		t.Error("resource exhausted should be retryable")
	}
	if isRetryableError(400, "bad request") {
		t.Error("400 should not be retryable")
	}
}

func TestIsClaudeThinkingModel(t *testing.T) {
	if !isClaudeThinkingModel("claude-3.5-sonnet-thinking") {
		t.Error("expected true for claude thinking model")
	}
	if isClaudeThinkingModel("gemini-2.0-flash") {
		t.Error("expected false for gemini model")
	}
}

func TestExtractErrorMessage(t *testing.T) {
	jsonErr := `{"error":{"message":"quota exceeded","code":429}}`
	if got := extractErrorMessage(jsonErr); got != "quota exceeded" {
		t.Errorf("expected 'quota exceeded', got %q", got)
	}
	if got := extractErrorMessage("plain text"); got != "plain text" {
		t.Errorf("expected 'plain text', got %q", got)
	}
}

func TestGetGeminiCLIThinkingLevel(t *testing.T) {
	tests := []struct {
		effort  ai.ThinkingLevel
		modelID string
		want    GoogleThinkingLevel
	}{
		{ai.ThinkingLow, "gemini-3-pro-001", ThinkingLevelLow},
		{ai.ThinkingHigh, "gemini-3-pro-001", ThinkingLevelHigh},
		{ai.ThinkingMinimal, "gemini-3-flash-001", ThinkingLevelMinimal},
		{ai.ThinkingMedium, "gemini-3-flash-001", ThinkingLevelMedium},
		{ai.ThinkingHigh, "gemini-2.0-flash", ThinkingLevelHigh},
	}
	for _, tt := range tests {
		got := getGeminiCLIThinkingLevel(tt.effort, tt.modelID)
		if got != tt.want {
			t.Errorf("getGeminiCLIThinkingLevel(%s, %s) = %s, want %s", tt.effort, tt.modelID, got, tt.want)
		}
	}
}

func TestBuildGeminiCLIRequest(t *testing.T) {
	model := &ai.Model{
		ID:        "gemini-2.0-flash",
		Provider:  "google-gemini-cli",
		Api:       "google-gemini-cli",
		MaxTokens: 32000,
	}
	ctx := ai.Context{
		SystemPrompt: "You are helpful.",
		Messages:     []ai.Message{ai.NewUserMsg("hello", 0)},
		Tools: []ai.Tool{
			{Name: "read", Description: "Read file", Parameters: map[string]any{"type": "object"}},
		},
	}

	temp := 0.7
	maxTokens := 8192
	options := &ai.StreamOptions{
		Temperature: &temp,
		MaxTokens:   &maxTokens,
		ToolChoice:  "auto",
	}

	req := buildGeminiCLIRequest(model, ctx, "my-project", options, false)

	if req.Project != "my-project" {
		t.Errorf("expected project 'my-project', got %q", req.Project)
	}
	if req.Model != "gemini-2.0-flash" {
		t.Errorf("expected model 'gemini-2.0-flash', got %q", req.Model)
	}
	if req.Request.SystemInstruction == nil {
		t.Fatal("expected system instruction")
	}
	if req.Request.SystemInstruction.Parts[0].Text != "You are helpful." {
		t.Errorf("wrong system instruction: %q", req.Request.SystemInstruction.Parts[0].Text)
	}
	if req.Request.Tools == nil {
		t.Fatal("expected tools")
	}
	if req.Request.ToolConfig == nil {
		t.Fatal("expected tool config")
	}
	if req.UserAgent != "tau-coding-agent" {
		t.Errorf("expected user agent 'tau-coding-agent', got %q", req.UserAgent)
	}
}

func TestBuildGeminiCLIRequest_Antigravity(t *testing.T) {
	model := &ai.Model{
		ID:        "gemini-2.0-flash",
		Provider:  "google-antigravity",
		Api:       "google-antigravity",
		MaxTokens: 32000,
	}
	ctx := ai.Context{
		SystemPrompt: "Custom prompt.",
		Messages:     []ai.Message{ai.NewUserMsg("hi", 0)},
	}

	req := buildGeminiCLIRequest(model, ctx, "proj", nil, true)

	if req.RequestType != "agent" {
		t.Errorf("expected requestType 'agent', got %q", req.RequestType)
	}
	if req.UserAgent != "antigravity" {
		t.Errorf("expected user agent 'antigravity', got %q", req.UserAgent)
	}
	// Should have antigravity system instruction prepended
	if req.Request.SystemInstruction == nil {
		t.Fatal("expected system instruction")
	}
	if len(req.Request.SystemInstruction.Parts) < 3 {
		t.Fatalf("expected at least 3 parts in system instruction, got %d", len(req.Request.SystemInstruction.Parts))
	}
	if !strings.Contains(req.Request.SystemInstruction.Parts[0].Text, "Antigravity") {
		t.Error("first part should contain Antigravity system instruction")
	}
}

func TestStreamGoogleGeminiCLI_MissingCredentials(t *testing.T) {
	model := &ai.Model{
		ID:       "gemini-2.0-flash",
		Provider: "google-gemini-cli",
		Api:      "google-gemini-cli",
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 0)}}

	stream := StreamGoogleGeminiCLI(context.Background(), model, ctx, &ai.StreamOptions{})
	var lastEvent ai.AssistantMessageEvent
	for ev := range stream.Events {
		lastEvent = ev
	}
	if lastEvent.Type != ai.EventError {
		t.Fatalf("expected error event, got %s", lastEvent.Type)
	}
	if !strings.Contains(lastEvent.Error.ErrorMessage, "OAuth") {
		t.Errorf("expected OAuth error, got %q", lastEvent.Error.ErrorMessage)
	}
}

func TestStreamGoogleGeminiCLI_InvalidCredentials(t *testing.T) {
	model := &ai.Model{
		ID:       "gemini-2.0-flash",
		Provider: "google-gemini-cli",
		Api:      "google-gemini-cli",
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 0)}}

	stream := StreamGoogleGeminiCLI(context.Background(), model, ctx, &ai.StreamOptions{
		ApiKey: "not-json",
	})
	var lastEvent ai.AssistantMessageEvent
	for ev := range stream.Events {
		lastEvent = ev
	}
	if lastEvent.Type != ai.EventError {
		t.Fatalf("expected error event, got %s", lastEvent.Type)
	}
	if !strings.Contains(lastEvent.Error.ErrorMessage, "invalid") {
		t.Errorf("expected invalid credentials error, got %q", lastEvent.Error.ErrorMessage)
	}
}

func TestStreamGoogleGeminiCLI_Success(t *testing.T) {
	server := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %q", auth)
		}

		// SSE response
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := cloudCodeAssistChunk{
			Response: &cloudCodeAssistResp{
				Candidates: []cloudCodeAssistCandidate{
					{
						Content: &cloudCodeAssistContent{
							Role: "model",
							Parts: []cloudCodeAssistPart{
								{Text: "Hello "},
							},
						},
					},
				},
			},
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)

		chunk2 := cloudCodeAssistChunk{
			Response: &cloudCodeAssistResp{
				Candidates: []cloudCodeAssistCandidate{
					{
						Content: &cloudCodeAssistContent{
							Role: "model",
							Parts: []cloudCodeAssistPart{
								{Text: "world!"},
							},
						},
						FinishReason: "STOP",
					},
				},
				UsageMetadata: &geminiCLIUsageMetadata{
					PromptTokenCount:     10,
					CandidatesTokenCount: 5,
					TotalTokenCount:      15,
				},
			},
		}
		data2, _ := json.Marshal(chunk2)
		fmt.Fprintf(w, "data: %s\n\n", data2)
	}))
	defer server.Close()

	creds, _ := json.Marshal(map[string]string{"token": "test-token", "projectId": "test-proj"})
	model := &ai.Model{
		ID:        "gemini-2.0-flash",
		Provider:  "google-gemini-cli",
		Api:       "google-gemini-cli",
		BaseURL:   server.URL,
		MaxTokens: 32000,
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 0)}}

	stream := StreamGoogleGeminiCLI(context.Background(), model, ctx, &ai.StreamOptions{
		ApiKey: string(creds),
	})

	var events []ai.AssistantMessageEvent
	for ev := range stream.Events {
		events = append(events, ev)
	}

	// Should have start, text_start, text_delta(s), text_end, done
	hasStart := false
	hasDone := false
	hasTextDelta := false
	var fullText string

	for _, ev := range events {
		switch ev.Type {
		case ai.EventStart:
			hasStart = true
		case ai.EventDone:
			hasDone = true
			if ev.Message != nil {
				for _, c := range ev.Message.Content {
					if c.IsText() {
						fullText += c.Text.Text
					}
				}
			}
		case ai.EventTextDelta:
			hasTextDelta = true
		}
	}

	if !hasStart {
		t.Error("missing start event")
	}
	if !hasDone {
		t.Error("missing done event")
	}
	if !hasTextDelta {
		t.Error("missing text_delta event")
	}
	if fullText != "Hello world!" {
		t.Errorf("expected 'Hello world!', got %q", fullText)
	}
}

func TestStreamGoogleGeminiCLI_ToolCalls(t *testing.T) {
	server := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		b := true
		chunk := cloudCodeAssistChunk{
			Response: &cloudCodeAssistResp{
				Candidates: []cloudCodeAssistCandidate{
					{
						Content: &cloudCodeAssistContent{
							Role: "model",
							Parts: []cloudCodeAssistPart{
								{Text: "Let me think...", Thought: &b},
								{FunctionCall: &cloudCodeAssistFuncCall{
									Name: "read",
									Args: map[string]any{"path": "foo.txt"},
									ID:   "call_1",
								}},
							},
						},
						FinishReason: "STOP",
					},
				},
				UsageMetadata: &geminiCLIUsageMetadata{
					PromptTokenCount:     10,
					CandidatesTokenCount: 5,
					ThoughtsTokenCount:   20,
					TotalTokenCount:      35,
				},
			},
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}))
	defer server.Close()

	creds, _ := json.Marshal(map[string]string{"token": "tok", "projectId": "proj"})
	model := &ai.Model{
		ID: "gemini-2.0-flash", Provider: "google-gemini-cli", Api: "google-gemini-cli",
		BaseURL: server.URL, MaxTokens: 32000,
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("read foo.txt", 0)}}

	stream := StreamGoogleGeminiCLI(context.Background(), model, ctx, &ai.StreamOptions{
		ApiKey: string(creds),
	})

	var events []ai.AssistantMessageEvent
	for ev := range stream.Events {
		events = append(events, ev)
	}

	hasThinking := false
	hasToolCall := false
	for _, ev := range events {
		if ev.Type == ai.EventThinkingDelta {
			hasThinking = true
		}
		if ev.Type == ai.EventToolcallEnd {
			hasToolCall = true
			if ev.ToolCall == nil {
				t.Error("expected tool call in event")
			} else if ev.ToolCall.Name != "read" {
				t.Errorf("expected tool name 'read', got %q", ev.ToolCall.Name)
			}
		}
	}

	if !hasThinking {
		t.Error("missing thinking event")
	}
	if !hasToolCall {
		t.Error("missing tool call event")
	}
}

func TestStreamSimpleGoogleGeminiCLI_NoKey(t *testing.T) {
	model := &ai.Model{
		ID: "gemini-2.0-flash", Provider: "google-gemini-cli", Api: "google-gemini-cli",
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 0)}}

	stream := StreamSimpleGoogleGeminiCLI(context.Background(), model, ctx, &ai.SimpleStreamOptions{})
	var lastEvent ai.AssistantMessageEvent
	for ev := range stream.Events {
		lastEvent = ev
	}
	if lastEvent.Type != ai.EventError {
		t.Fatalf("expected error event, got %s", lastEvent.Type)
	}
}

func TestRegisterGoogleGeminiCLI(t *testing.T) {
	r := ai.NewRegistry()
	RegisterGoogleGeminiCLI(r)

	// Check both providers are registered
	if p := r.GetApiProvider("google-gemini-cli"); p == nil {
		t.Error("google-gemini-cli not registered")
	}
	if p := r.GetApiProvider("google-antigravity"); p == nil {
		t.Error("google-antigravity not registered")
	}
}
