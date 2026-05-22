package history

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatPreamble_Empty(t *testing.T) {
	preamble, latest := FormatPreamble(nil)
	if preamble != "" || latest != "" {
		t.Errorf("nil: preamble=%q latest=%q", preamble, latest)
	}

	preamble, latest = FormatPreamble(json.RawMessage(`[]`))
	if preamble != "" || latest != "" {
		t.Errorf("empty: preamble=%q latest=%q", preamble, latest)
	}
}

func TestFormatPreamble_SingleMessage(t *testing.T) {
	query := `[{"role":"user","content":"hello"}]`
	preamble, latest := FormatPreamble(json.RawMessage(query))
	if preamble != "" {
		t.Errorf("single msg should have no preamble, got: %q", preamble)
	}
	if latest != "hello" {
		t.Errorf("latest: got %q, want %q", latest, "hello")
	}
}

func TestFormatPreamble_WithHistory(t *testing.T) {
	query := `[
		{"role":"user","content":"what is 2+2?"},
		{"role":"assistant","content":"4"},
		{"role":"user","content":"and 3+3?"}
	]`
	preamble, latest := FormatPreamble(json.RawMessage(query))

	if latest != "and 3+3?" {
		t.Errorf("latest: got %q, want %q", latest, "and 3+3?")
	}
	if !strings.Contains(preamble, "[Prior conversation history]") {
		t.Errorf("preamble missing header: %q", preamble)
	}
	if !strings.Contains(preamble, "user: what is 2+2?") {
		t.Errorf("preamble missing user msg: %q", preamble)
	}
	if !strings.Contains(preamble, "assistant: 4") {
		t.Errorf("preamble missing assistant msg: %q", preamble)
	}
	if !strings.Contains(preamble, "[End of history]") {
		t.Errorf("preamble missing footer: %q", preamble)
	}
	// Latest message should NOT be in the preamble.
	if strings.Contains(preamble, "and 3+3?") {
		t.Errorf("preamble should not contain latest msg: %q", preamble)
	}
}

func TestFormatPreamble_RoleMapping(t *testing.T) {
	query := `[
		{"role":"human","content":"hi"},
		{"role":"assistant","content":"hello"},
		{"role":"system","content":"you are helpful"},
		{"role":"user","content":"bye"}
	]`
	preamble, latest := FormatPreamble(json.RawMessage(query))

	if latest != "bye" {
		t.Errorf("latest: %q", latest)
	}
	if !strings.Contains(preamble, "user: hi") {
		t.Errorf("human→user mapping failed: %q", preamble)
	}
	if !strings.Contains(preamble, "assistant: hello") {
		t.Errorf("assistant role mapping failed: %q", preamble)
	}
	if !strings.Contains(preamble, "system: you are helpful") {
		t.Errorf("system role failed: %q", preamble)
	}
}

func TestFormatPreamble_SkipsEmptyContent(t *testing.T) {
	query := `[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":""},
		{"role":"assistant","content":"  "},
		{"role":"user","content":"bye"}
	]`
	preamble, _ := FormatPreamble(json.RawMessage(query))

	// Empty and whitespace-only messages should be skipped.
	lines := strings.Split(strings.TrimSpace(preamble), "\n")
	// Should be: header, "user: hi", footer = 3 lines.
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d: %v", len(lines), lines)
	}
}

func TestFormatPreamble_InvalidJSON(t *testing.T) {
	preamble, latest := FormatPreamble(json.RawMessage(`not json`))
	if preamble != "" || latest != "" {
		t.Errorf("invalid json: preamble=%q latest=%q", preamble, latest)
	}
}

func TestFormatPreamble_LongConversation(t *testing.T) {
	// Simulate a 10-turn conversation.
	var msgs []Message
	for i := 0; i < 20; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, Message{Role: role, Content: "msg " + string(rune('A'+i))})
	}
	data, _ := json.Marshal(msgs)
	preamble, latest := FormatPreamble(json.RawMessage(data))

	// Latest is last message.
	if latest != msgs[19].Content {
		t.Errorf("latest: got %q, want %q", latest, msgs[19].Content)
	}
	// Preamble should have 19 messages (all but last).
	// Count non-header/footer lines.
	lines := strings.Split(strings.TrimSpace(preamble), "\n")
	// header + 19 msgs + footer = 21
	if len(lines) != 21 {
		t.Errorf("expected 21 lines, got %d", len(lines))
	}
}
