package ai

import (
	"strings"
	"testing"
)

func TestParseStreamingJSON_Empty(t *testing.T) {
	result := ParseStreamingJSON("")
	if len(result) != 0 {
		t.Errorf("empty string: got %v, want empty map", result)
	}
	result = ParseStreamingJSON("   ")
	if len(result) != 0 {
		t.Errorf("whitespace: got %v, want empty map", result)
	}
}

func TestParseStreamingJSON_Complete(t *testing.T) {
	result := ParseStreamingJSON(`{"path": "foo.txt", "offset": 10}`)
	if result["path"] != "foo.txt" {
		t.Errorf("path: got %v, want foo.txt", result["path"])
	}
	if result["offset"] != float64(10) {
		t.Errorf("offset: got %v, want 10", result["offset"])
	}
}

func TestParseStreamingJSON_PartialString(t *testing.T) {
	result := ParseStreamingJSON(`{"path": "foo`)
	if result["path"] != "foo" {
		t.Errorf("got %v, want foo", result["path"])
	}
}

func TestParseStreamingJSON_PartialObject(t *testing.T) {
	result := ParseStreamingJSON(`{"path": "foo.txt"`)
	if result["path"] != "foo.txt" {
		t.Errorf("got %v, want foo.txt", result["path"])
	}
}

func TestParseStreamingJSON_UnclosedNestedObject(t *testing.T) {
	result := ParseStreamingJSON(`{"a": {"b": "c"`)
	if a, ok := result["a"].(map[string]any); ok {
		if a["b"] != "c" {
			t.Errorf("a.b: got %v, want c", a["b"])
		}
	} else {
		t.Errorf("a is not a nested object: %v", result["a"])
	}
}

func TestParseStreamingJSON_TrailingComma(t *testing.T) {
	result := ParseStreamingJSON(`{"path": "foo.txt",`)
	if result["path"] != "foo.txt" {
		t.Errorf("got %v, want foo.txt", result["path"])
	}
}

func TestParseStreamingJSON_InvalidJSON(t *testing.T) {
	result := ParseStreamingJSON(`not json at all`)
	if len(result) != 0 {
		t.Errorf("got %v, want empty map", result)
	}
}

func TestParseStreamingJSON_PartialArray(t *testing.T) {
	result := ParseStreamingJSON(`{"items": ["a", "b"`)
	items, ok := result["items"].([]any)
	if !ok {
		// items may not be parseable as array from partial JSON
		return
	}
	// Should have at least some items
	if len(items) == 0 {
		t.Error("expected at least 1 item")
	}
	if len(items) >= 1 && items[0] != "a" {
		t.Errorf("items[0]: got %v, want a", items[0])
	}
	if len(items) >= 2 && items[1] != "b" {
		t.Errorf("items[1]: got %v, want b", items[1])
	}
}

func TestParseStreamingJSON_EscapedQuote(t *testing.T) {
	result := ParseStreamingJSON(`{"text": "hello \"world"}`)
	if result["text"] != `hello "world` {
		t.Errorf("got %v, want hello \"world", result["text"])
	}
}

func TestParseStreamingJSON_DeeplyNested(t *testing.T) {
	// 20 levels of nesting
	input := `{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":{"j":{"k":{"l":{"m":{"n":{"o":{"p":{"q":{"r":{"s":{"t":"deep"`
	result := ParseStreamingJSON(input)
	// Should parse without panic; may or may not extract the deepest value
	if result == nil {
		t.Error("expected non-nil result")
	}
}

func TestParseStreamingJSON_VeryLongString(t *testing.T) {
	// 10KB string value
	longVal := strings.Repeat("x", 10000)
	input := `{"data":"` + longVal + `"}`
	result := ParseStreamingJSON(input)
	if result["data"] != longVal {
		t.Errorf("expected 10000 char string, got length %d", len(result["data"].(string)))
	}
}

func TestParseStreamingJSON_VeryLongPartialString(t *testing.T) {
	// Partial 10KB string (no closing quote)
	longVal := strings.Repeat("y", 10000)
	input := `{"data":"` + longVal
	result := ParseStreamingJSON(input)
	if val, ok := result["data"].(string); ok {
		if len(val) < 10000 {
			t.Errorf("expected at least 10000 chars, got %d", len(val))
		}
	}
}

func TestParseStreamingJSON_EscapeSequences(t *testing.T) {
	input := `{"text":"line1\nline2\ttab\\backslash"}`
	result := ParseStreamingJSON(input)
	expected := "line1\nline2\ttab\\backslash"
	if result["text"] != expected {
		t.Errorf("got %q, want %q", result["text"], expected)
	}
}

func TestParseStreamingJSON_UnicodeEscape(t *testing.T) {
	input := `{"emoji":"\u0048\u0065\u006c\u006c\u006f"}`
	result := ParseStreamingJSON(input)
	if result["emoji"] != "Hello" {
		t.Errorf("got %q, want 'Hello'", result["emoji"])
	}
}

func TestParseStreamingJSON_BoolAndNull(t *testing.T) {
	input := `{"flag":true,"nothing":null,"off":false}`
	result := ParseStreamingJSON(input)
	if result["flag"] != true {
		t.Errorf("flag: got %v", result["flag"])
	}
	if result["nothing"] != nil {
		t.Errorf("nothing: got %v", result["nothing"])
	}
	if result["off"] != false {
		t.Errorf("off: got %v", result["off"])
	}
}

func TestParseStreamingJSON_NestedArray(t *testing.T) {
	input := `{"matrix":[[1,2],[3,4]]}`
	result := ParseStreamingJSON(input)
	matrix, ok := result["matrix"].([]any)
	if !ok {
		t.Fatalf("matrix is not array: %T", result["matrix"])
	}
	if len(matrix) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(matrix))
	}
}

func TestParseStreamingJSON_OnlyOpenBrace(t *testing.T) {
	result := ParseStreamingJSON(`{`)
	if result == nil {
		t.Error("expected non-nil")
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestParseStreamingJSON_MaliciousInput(t *testing.T) {
	// Should not panic on garbage input
	inputs := []string{
		`}}}`,
		`]]]`,
		`"just a string"`,
		`123`,
		`null`,
		`true`,
		`{"a":"b"`,
		`{"a":`,
		`{"a":{"b":`,
		`{,,,,}`,
		`{"key":}`,
	}
	for _, input := range inputs {
		// Just ensure no panic
		_ = ParseStreamingJSON(input)
	}
}

func TestParseStreamingJSON_PartialKey(t *testing.T) {
	result := ParseStreamingJSON(`{"name":"test","val`)
	if result["name"] != "test" {
		t.Errorf("got %v, want test", result["name"])
	}
}
