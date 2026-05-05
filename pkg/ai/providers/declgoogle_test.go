package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/extension/apikind"
)

// Synthetic test fixtures for the decl-google adapter. They cover the same
// shape range that real ext-shipped Cloud-Code-Assist configs hit: a plain
// envelope (configA) and one with conditional headers + multi-part system
// instruction prefix (configB). Production configs are constructed at
// runtime from extensions' register_api(DeclGoogleApi(...)) calls.
var testDeclGoogleConfigA = &DeclGoogleConfig{
	Endpoints: []string{"https://example-a.test"},
	Headers: map[string]string{
		"User-Agent":        "test-agent-a/1.0 ${os}/${arch}",
		"X-Goog-Api-Client": "gl-test/1.0",
	},
	Envelope: json.RawMessage(`{
		"project":   "${creds.project_id}",
		"model":     "${model.id}",
		"request":   "$inner",
		"userAgent": "fir-coding-agent",
		"requestId": "${fn.rand_id(fir-coding-agent)}"
	}`),
	ReasoningHeaderPrefix: "x-test-thinking-",
}

const testSystemInstructionB = "You are TestAssistantB, a synthetic agent fixture for the decl-google adapter test suite." +
	"**Absolute paths only**" +
	"**Proactiveness**"

var testDeclGoogleConfigB = &DeclGoogleConfig{
	Endpoints: []string{"https://example-b.test"},
	Headers: map[string]string{
		"User-Agent": "test-agent-b/1.0 ${os}/${arch}",
	},
	ConditionalHeaders: []ConditionalHeader{
		{
			When: ConditionalHeaderMatch{ModelIDPrefix: "claude-", RequiresReasoning: true},
			Set:  map[string]string{"anthropic-beta": claudeThinkingBetaHeader},
		},
	},
	Envelope: json.RawMessage(`{
		"project":     "${creds.project_id}",
		"model":       "${model.id}",
		"request":     "$inner",
		"requestType": "agent",
		"userAgent":   "test-b",
		"requestId":   "${fn.rand_id(test-b)}"
	}`),
	SystemInstructionPrefix: []googleSysInstrPart{
		{Text: testSystemInstructionB},
		{Text: "Please ignore following [ignore]" + testSystemInstructionB + "[/ignore]"},
	},
	SystemInstructionRole: "user",
	ReasoningHeaderPrefix: "x-test-thinking-",
}

func init() {
	// Tests dial Stream* helpers directly; ensure the configs are visible
	// via getDeclGoogleConfig() too.
	RegisterDeclGoogleConfig("test-decl-google-a", testDeclGoogleConfigA)
	RegisterDeclGoogleConfig("test-decl-google-b", testDeclGoogleConfigB)
}

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

func TestDeclGoogle_ConditionalHeaders(t *testing.T) {
	claudeModel := &ai.Model{ID: "claude-3.5-sonnet", Reasoning: true}
	hdrs, err := resolveDeclGoogleHeaders(testDeclGoogleConfigB, claudeModel, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if hdrs["anthropic-beta"] != claudeThinkingBetaHeader {
		t.Errorf("expected anthropic-beta header for claude reasoning model, got %v", hdrs)
	}
	geminiModel := &ai.Model{ID: "gemini-2.0-flash", Reasoning: true}
	hdrs2, _ := resolveDeclGoogleHeaders(testDeclGoogleConfigB, geminiModel, map[string]string{})
	if _, has := hdrs2["anthropic-beta"]; has {
		t.Errorf("expected no anthropic-beta header for gemini model, got %v", hdrs2)
	}
	claudeNoReasoning := &ai.Model{ID: "claude-3.5-sonnet", Reasoning: false}
	hdrs3, _ := resolveDeclGoogleHeaders(testDeclGoogleConfigB, claudeNoReasoning, map[string]string{})
	if _, has := hdrs3["anthropic-beta"]; has {
		t.Errorf("expected no anthropic-beta header for claude without reasoning, got %v", hdrs3)
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

func TestGetDeclGoogleThinkingLevel(t *testing.T) {
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
		got := getDeclGoogleThinkingLevel(tt.effort, tt.modelID)
		if got != tt.want {
			t.Errorf("getDeclGoogleThinkingLevel(%s, %s) = %s, want %s", tt.effort, tt.modelID, got, tt.want)
		}
	}
}

func TestBuildDeclGoogleRequest_ConfigA(t *testing.T) {
	model := &ai.Model{
		ID:        "gemini-2.0-flash",
		Provider:  "test-decl-google-a",
		Api:       "test-decl-google-a",
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

	creds := map[string]string{"access_token": "tok", "project_id": "my-project"}
	body, err := buildDeclGoogleBody(model, ctx, options, testDeclGoogleConfigA, creds)
	if err != nil {
		t.Fatal(err)
	}
	bodyJSON, _ := json.Marshal(body)
	var got map[string]any
	if err := json.Unmarshal(bodyJSON, &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if got["project"] != "my-project" {
		t.Errorf("expected project 'my-project', got %v", got["project"])
	}
	if got["model"] != "gemini-2.0-flash" {
		t.Errorf("expected model 'gemini-2.0-flash', got %v", got["model"])
	}
	if got["userAgent"] != "fir-coding-agent" {
		t.Errorf("expected userAgent 'fir-coding-agent', got %v", got["userAgent"])
	}
	rid, _ := got["requestId"].(string)
	if !strings.HasPrefix(rid, "fir-coding-agent-") {
		t.Errorf("expected requestId to start with 'fir-coding-agent-', got %q", rid)
	}
	inner, ok := got["request"].(map[string]any)
	if !ok {
		t.Fatalf("expected request to be object, got %T", got["request"])
	}
	si, ok := inner["systemInstruction"].(map[string]any)
	if !ok {
		t.Fatal("expected systemInstruction object")
	}
	parts, _ := si["parts"].([]any)
	if len(parts) == 0 {
		t.Fatal("expected systemInstruction parts")
	}
	if parts[0].(map[string]any)["text"] != "You are helpful." {
		t.Errorf("wrong system instruction: %v", parts[0])
	}
	if _, hasTools := inner["tools"]; !hasTools {
		t.Error("expected tools in inner request")
	}
	if _, hasToolCfg := inner["toolConfig"]; !hasToolCfg {
		t.Error("expected toolConfig in inner request")
	}
}

func TestBuildDeclGoogleRequest_ConfigB(t *testing.T) {
	model := &ai.Model{
		ID:        "gemini-2.0-flash",
		Provider:  "test-decl-google-b",
		Api:       "test-decl-google-b",
		MaxTokens: 32000,
	}
	ctx := ai.Context{
		SystemPrompt: "Custom prompt.",
		Messages:     []ai.Message{ai.NewUserMsg("hi", 0)},
	}

	creds := map[string]string{"access_token": "tok", "project_id": "proj"}
	body, err := buildDeclGoogleBody(model, ctx, nil, testDeclGoogleConfigB, creds)
	if err != nil {
		t.Fatal(err)
	}
	bodyJSON, _ := json.Marshal(body)
	var got map[string]any
	if err := json.Unmarshal(bodyJSON, &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if got["requestType"] != "agent" {
		t.Errorf("expected requestType 'agent', got %v", got["requestType"])
	}
	if got["userAgent"] != "test-b" {
		t.Errorf("expected userAgent 'test-b', got %v", got["userAgent"])
	}
	rid, _ := got["requestId"].(string)
	if !strings.HasPrefix(rid, "test-b-") {
		t.Errorf("expected requestId to start with 'test-b-', got %q", rid)
	}
	inner, _ := got["request"].(map[string]any)
	si, _ := inner["systemInstruction"].(map[string]any)
	if si == nil {
		t.Fatal("expected system instruction")
	}
	parts, _ := si["parts"].([]any)
	if len(parts) < 3 {
		t.Fatalf("expected at least 3 parts in system instruction, got %d", len(parts))
	}
	first, _ := parts[0].(map[string]any)["text"].(string)
	if !strings.Contains(first, "TestAssistantB") {
		t.Error("first part should contain configB system instruction prefix")
	}
}

func TestStreamDeclGoogle_MissingCredentials(t *testing.T) {
	model := &ai.Model{
		ID:       "gemini-2.0-flash",
		Provider: "test-decl-google-a",
		Api:      "test-decl-google-a",
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 0)}}

	stream := StreamDeclGoogle(context.Background(), model, ctx, &ai.StreamOptions{})
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

func TestStreamDeclGoogle_InvalidCredentials(t *testing.T) {
	model := &ai.Model{
		ID:       "gemini-2.0-flash",
		Provider: "test-decl-google-a",
		Api:      "test-decl-google-a",
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 0)}}

	stream := StreamDeclGoogle(context.Background(), model, ctx, &ai.StreamOptions{
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

func TestStreamDeclGoogle_Success(t *testing.T) {
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
				UsageMetadata: &declGoogleUsageMetadata{
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
		Provider:  "test-decl-google-a",
		Api:       "test-decl-google-a",
		BaseURL:   server.URL,
		MaxTokens: 32000,
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 0)}}

	stream := StreamDeclGoogle(context.Background(), model, ctx, &ai.StreamOptions{
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

func TestStreamDeclGoogle_ToolCalls(t *testing.T) {
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
				UsageMetadata: &declGoogleUsageMetadata{
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
		ID: "gemini-2.0-flash", Provider: "test-decl-google-a", Api: "test-decl-google-a",
		BaseURL: server.URL, MaxTokens: 32000,
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("read foo.txt", 0)}}

	stream := StreamDeclGoogle(context.Background(), model, ctx, &ai.StreamOptions{
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

func TestStreamSimpleDeclGoogle_NoKey(t *testing.T) {
	model := &ai.Model{
		ID: "gemini-2.0-flash", Provider: "test-decl-google-a", Api: "test-decl-google-a",
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 0)}}

	stream := StreamSimpleDeclGoogle(context.Background(), model, ctx, &ai.SimpleStreamOptions{})
	var lastEvent ai.AssistantMessageEvent
	for ev := range stream.Events {
		lastEvent = ev
	}
	if lastEvent.Type != ai.EventError {
		t.Fatalf("expected error event, got %s", lastEvent.Type)
	}
}

// TestDeclGoogleApiKind_Register exercises the apikind handler that
// pkg/extension routes ApiSpec(kind="decl-google") payloads through.
func TestDeclGoogleApiKind_Register(t *testing.T) {
	h := apikind.Get("decl-google")
	if h == nil {
		t.Fatal("decl-google api kind handler not registered")
	}

	payload, _ := json.Marshal(map[string]any{
		"endpoints":               []string{"https://example.test"},
		"headers":                 map[string]string{"X-Test": "1"},
		"envelope":                `{"model":"${model.id}","request":"$inner"}`,
		"reasoning_header_prefix": "x-test-",
	})

	prevReg := ai.DefaultRegistry.GetApiProvider("test-decl-google")
	if prevReg != nil {
		t.Fatal("test precondition: api id collides with an existing entry")
	}

	if err := h.Register("test-decl-google", payload, "ext-api:test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() {
		h.Unregister("test-decl-google")
		ai.DefaultRegistry.UnregisterApiProviders("ext-api:test")
	})

	// ai.ApiProvider entry installed under the supplied source id.
	if p := ai.DefaultRegistry.GetApiProvider("test-decl-google"); p == nil {
		t.Error("expected ai.ApiProvider for test-decl-google after Register")
	}
	// Per-Api config installed.
	if cfg := getDeclGoogleConfig("test-decl-google"); cfg == nil {
		t.Error("expected DeclGoogleConfig for test-decl-google after Register")
	}

	h.Unregister("test-decl-google")
	if cfg := getDeclGoogleConfig("test-decl-google"); cfg != nil {
		t.Error("Unregister should drop the per-Api config entry")
	}
}

// TestDeclGoogleApiKind_Register_Errors covers the validation paths in the
// kind handler — empty payload, malformed JSON, malformed envelope JSON.
func TestDeclGoogleApiKind_Register_Errors(t *testing.T) {
	h := apikind.Get("decl-google")
	if h == nil {
		t.Fatal("decl-google api kind handler not registered")
	}

	cases := []struct {
		name    string
		payload []byte
		wantSub string
	}{
		{
			name:    "empty payload",
			payload: nil,
			wantSub: "empty payload",
		},
		{
			name:    "malformed payload JSON",
			payload: []byte("not json"),
			wantSub: "parse payload",
		},
		{
			name: "malformed envelope JSON",
			payload: func() []byte {
				b, _ := json.Marshal(map[string]any{
					"endpoints": []string{"https://x"},
					"envelope":  "{this isn't json}",
				})
				return b
			}(),
			wantSub: "envelope is not valid JSON",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := h.Register("test-bad", tc.payload, "ext-api:test")
			if err == nil {
				h.Unregister("test-bad")
				ai.DefaultRegistry.UnregisterApiProviders("ext-api:test")
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err, tc.wantSub)
			}
			// Bad spec must not leave any per-Api state behind.
			if cfg := getDeclGoogleConfig("test-bad"); cfg != nil {
				t.Error("failed Register should not install a config")
			}
			if p := ai.DefaultRegistry.GetApiProvider("test-bad"); p != nil {
				t.Error("failed Register should not install an ApiProvider")
			}
		})
	}
}

// TestEnvelopeHonoured proves the declarative envelope is actually
// substituted: a sentinel field in the envelope template must appear in the
// marshalled body.  Regression test against the adapter ignoring cfg.Envelope
// and falling back to a typed-struct path.
func TestEnvelopeHonoured(t *testing.T) {
	cfg := &DeclGoogleConfig{
		Envelope: json.RawMessage(`{
			"sentinelField": "marker-${model.id}",
			"inner":         "$inner"
		}`),
	}
	model := &ai.Model{ID: "gemini-x", Api: "test", Provider: "test"}
	prompt := ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 0)}}
	body, err := buildDeclGoogleBody(model, prompt, nil, cfg, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(body)
	var got map[string]any
	json.Unmarshal(out, &got)
	if got["sentinelField"] != "marker-gemini-x" {
		t.Errorf("envelope sentinelField not substituted; got %v in %s", got["sentinelField"], out)
	}
	if _, ok := got["inner"].(map[string]any); !ok {
		t.Errorf("$inner sentinel not replaced with inner request object: %s", out)
	}
}

func TestEnvelopeNil_IdentityBody(t *testing.T) {
	cfg := &DeclGoogleConfig{} // no envelope
	model := &ai.Model{ID: "m", Api: "a", Provider: "p"}
	prompt := ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 0)}}
	body, err := buildDeclGoogleBody(model, prompt, nil, cfg, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body.(*cloudCodeAssistInnerReq); !ok {
		t.Errorf("nil envelope should return inner directly; got %T", body)
	}
}

func TestParseGoogleCreds_Object(t *testing.T) {
	c := parseGoogleCreds(`{"token":"T","projectId":"P","extraField":"E"}`)
	if c["token"] != "T" {
		t.Errorf("token: %q", c["token"])
	}
	if c["project_id"] != "P" {
		t.Errorf("project_id (snake): %q", c["project_id"])
	}
	if c["extra_field"] != "E" {
		t.Errorf("extra_field: %q", c["extra_field"])
	}
	if c["access_token"] != "T" {
		t.Errorf("access_token alias from token: %q", c["access_token"])
	}
}

func TestParseGoogleCreds_String(t *testing.T) {
	c := parseGoogleCreds("plain-key")
	if c["api_key"] != "plain-key" || c["access_token"] != "plain-key" {
		t.Errorf("plain-string creds: %v", c)
	}
}

func TestSnakeCase(t *testing.T) {
	cases := map[string]string{
		"projectId":     "project_id",
		"accessToken":   "access_token",
		"X":             "x",
		"already_snake": "already_snake",
		"":              "",
	}
	for in, want := range cases {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveHeaders_OSArchSubst(t *testing.T) {
	cfg := &DeclGoogleConfig{
		Headers: map[string]string{"User-Agent": "ua/${os}/${arch}"},
	}
	model := &ai.Model{ID: "m", Api: "a", Provider: "p"}
	hdrs, err := resolveDeclGoogleHeaders(cfg, model, nil)
	if err != nil {
		t.Fatal(err)
	}
	ua := hdrs["User-Agent"]
	if !strings.Contains(ua, "/") || strings.Contains(ua, "${") {
		t.Errorf("expected substituted UA, got %q", ua)
	}
}
