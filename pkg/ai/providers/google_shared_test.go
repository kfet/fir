package providers

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestIsValidThoughtSignature(t *testing.T) {
	tests := []struct {
		sig  string
		want bool
	}{
		{"", false},
		{"abc", false}, // not multiple of 4
		{"abcd", true}, // valid base64
		{"ab==", true}, // valid with padding
		{"AAAA", true},
		{"A+/B", true},
		{"$$$$", false}, // invalid characters
		{"ab=d", false}, // = in wrong position (still matches pattern though)
	}
	for _, tt := range tests {
		got := IsValidThoughtSignature(tt.sig)
		if got != tt.want {
			t.Errorf("IsValidThoughtSignature(%q) = %v, want %v", tt.sig, got, tt.want)
		}
	}
}

func TestRetainThoughtSignature(t *testing.T) {
	if got := RetainThoughtSignature("old", "new"); got != "new" {
		t.Errorf("expected 'new', got %q", got)
	}
	if got := RetainThoughtSignature("old", ""); got != "old" {
		t.Errorf("expected 'old', got %q", got)
	}
	if got := RetainThoughtSignature("", "new"); got != "new" {
		t.Errorf("expected 'new', got %q", got)
	}
}

func TestRequiresToolCallId(t *testing.T) {
	tests := []struct {
		modelID string
		want    bool
	}{
		{"claude-3-sonnet", true},
		{"gpt-oss-4o", true},
		{"gemini-2.0-flash", false},
		{"gemini-3-pro", false},
	}
	for _, tt := range tests {
		got := RequiresToolCallId(tt.modelID)
		if got != tt.want {
			t.Errorf("RequiresToolCallId(%q) = %v, want %v", tt.modelID, got, tt.want)
		}
	}
}

func TestNormalizeToolCallId(t *testing.T) {
	// For non-requiring models, return as-is
	if got := NormalizeToolCallId("gemini-2.0-flash", "any-id"); got != "any-id" {
		t.Errorf("expected 'any-id', got %q", got)
	}

	// For requiring models, normalize
	if got := NormalizeToolCallId("claude-3-sonnet", "id@with!special"); got != "id_with_special" {
		t.Errorf("expected 'id_with_special', got %q", got)
	}
}

func TestMapGoogleStopReasonShared(t *testing.T) {
	tests := []struct {
		reason string
		want   ai.StopReason
	}{
		{"STOP", ai.StopReasonStop},
		{"MAX_TOKENS", ai.StopReasonLength},
		{"SAFETY", ai.StopReasonError},
		{"RECITATION", ai.StopReasonError},
		{"UNKNOWN", ai.StopReasonStop},
	}
	for _, tt := range tests {
		got := MapGoogleStopReason(tt.reason)
		if got != tt.want {
			t.Errorf("MapGoogleStopReason(%q) = %q, want %q", tt.reason, got, tt.want)
		}
	}
}

func TestMapGoogleToolChoice(t *testing.T) {
	tests := []struct {
		choice string
		want   string
	}{
		{"auto", "AUTO"},
		{"none", "NONE"},
		{"any", "ANY"},
		{"unknown", "AUTO"},
	}
	for _, tt := range tests {
		got := MapGoogleToolChoice(tt.choice)
		if got != tt.want {
			t.Errorf("MapGoogleToolChoice(%q) = %q, want %q", tt.choice, got, tt.want)
		}
	}
}

func TestConvertGoogleTools(t *testing.T) {
	// Empty tools
	if got := ConvertGoogleTools(nil, false); got != nil {
		t.Error("expected nil for empty tools")
	}

	tools := []ai.Tool{
		{Name: "read", Description: "Read a file", Parameters: map[string]any{"type": "object"}},
	}

	// With parametersJsonSchema (default)
	result := ConvertGoogleTools(tools, false)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool group, got %d", len(result))
	}
	decls, ok := result[0]["functionDeclarations"].([]map[string]any)
	if !ok || len(decls) != 1 {
		t.Fatal("expected 1 function declaration")
	}
	if _, ok := decls[0]["parametersJsonSchema"]; !ok {
		t.Error("expected parametersJsonSchema key")
	}

	// With parameters (legacy)
	result2 := ConvertGoogleTools(tools, true)
	decls2, _ := result2[0]["functionDeclarations"].([]map[string]any)
	if _, ok := decls2[0]["parameters"]; !ok {
		t.Error("expected parameters key")
	}
}

// TestConvertGoogleTools_SanitizesJSONSchemaMeta verifies that JSON Schema
// meta-declarations are stripped from tool parameters when using the legacy
// OpenAPI-style parameters format (Gemini rejects $schema, $defs, etc.).
func TestConvertGoogleTools_SanitizesJSONSchemaMeta(t *testing.T) {
	tools := []ai.Tool{{
		Name:        "complex",
		Description: "A tool with JSON Schema meta keys",
		Parameters: map[string]any{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$id":     "https://example.com/schema.json",
			"$defs": map[string]any{
				"Foo": map[string]any{"type": "string"},
			},
			"definitions": map[string]any{},
			"$comment":    "ignore me",
			"type":        "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
	}}
	result := ConvertGoogleTools(tools, true)
	decls, _ := result[0]["functionDeclarations"].([]map[string]any)
	params, _ := decls[0]["parameters"].(map[string]any)
	for _, key := range []string{"$schema", "$id", "$defs", "definitions", "$comment"} {
		if _, has := params[key]; has {
			t.Errorf("expected %q to be stripped from sanitized parameters, got %#v", key, params)
		}
	}
	if params["type"] != "object" {
		t.Errorf("expected type=object preserved, got %#v", params["type"])
	}
	if _, has := params["properties"]; !has {
		t.Errorf("expected properties preserved, got %#v", params)
	}
}

// TestSanitizeForOpenAPI_NestedAndArrays verifies recursion through nested
// objects and arrays.
func TestSanitizeForOpenAPI_NestedAndArrays(t *testing.T) {
	in := map[string]any{
		"$schema": "drop me",
		"type":    "object",
		"properties": map[string]any{
			"items": map[string]any{
				"type": "array",
				"items": map[string]any{
					"$id":  "drop nested",
					"type": "string",
				},
			},
		},
		"oneOf": []any{
			map[string]any{"$schema": "drop", "type": "string"},
			map[string]any{"type": "number"},
		},
	}
	out, ok := sanitizeForOpenAPI(in).(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", sanitizeForOpenAPI(in))
	}
	if _, has := out["$schema"]; has {
		t.Error("top-level $schema not stripped")
	}
	props, _ := out["properties"].(map[string]any)
	items, _ := props["items"].(map[string]any)
	itemsItems, _ := items["items"].(map[string]any)
	if _, has := itemsItems["$id"]; has {
		t.Error("nested $id not stripped")
	}
	if itemsItems["type"] != "string" {
		t.Errorf("nested type not preserved: %#v", itemsItems)
	}
	oneOf, _ := out["oneOf"].([]any)
	first, _ := oneOf[0].(map[string]any)
	if _, has := first["$schema"]; has {
		t.Error("array element $schema not stripped")
	}
}

func TestConvertGoogleMessages_UserText(t *testing.T) {
	model := &ai.Model{ID: "gemini-2.0-flash", Provider: "google"}
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("hello", 0)},
	}
	contents := ConvertGoogleMessages(model, ctx)
	if len(contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(contents))
	}
	if contents[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", contents[0].Role)
	}
	if len(contents[0].Parts) != 1 || contents[0].Parts[0].Text != "hello" {
		t.Error("expected text part 'hello'")
	}
}

func TestConvertGoogleMessages_AssistantWithToolCall(t *testing.T) {
	model := &ai.Model{ID: "gemini-2.0-flash", Provider: "google"}
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.NewAssistantMsg(ai.AssistantMessage{
				Provider: "google",
				Model:    "gemini-2.0-flash",
				Content: []ai.AssistantContent{
					ai.NewTextContent("thinking..."),
					ai.NewToolCallContent("tc1", "read", map[string]any{"path": "foo.txt"}),
				},
			}),
		},
	}
	contents := ConvertGoogleMessages(model, ctx)
	if len(contents) != 2 {
		t.Fatalf("expected 2 contents (model + synthetic tool result), got %d", len(contents))
	}
	if contents[0].Role != "model" {
		t.Errorf("expected role 'model', got %q", contents[0].Role)
	}
	if len(contents[0].Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(contents[0].Parts))
	}
	if contents[0].Parts[0].Text != "thinking..." {
		t.Errorf("expected text 'thinking...', got %q", contents[0].Parts[0].Text)
	}
	if contents[0].Parts[1].FunctionCall == nil {
		t.Error("expected function call part")
	}
}

func TestSanitizeSurrogates(t *testing.T) {
	// Normal strings pass through unchanged
	if got := SanitizeSurrogates("hello world"); got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
	// Empty string
	if got := SanitizeSurrogates(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
