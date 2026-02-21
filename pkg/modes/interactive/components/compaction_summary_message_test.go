package components

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/core"
)

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{100, "100"},
		{1000, "1,000"},
		{5000, "5,000"},
		{1000000, "1,000,000"},
		{12345678, "12,345,678"},
	}
	for _, tc := range tests {
		got := formatTokenCount(tc.input)
		if got != tc.expected {
			t.Errorf("formatTokenCount(%d) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestCompactionSummaryMessage_Collapsed(t *testing.T) {
	msg := &core.CompactionSummaryMessage{
		Role:         "compactionSummary",
		Summary:      "compacted context",
		TokensBefore: 5000,
	}
	comp := NewCompactionSummaryMessageComponent(msg, nil)
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "compaction") {
		t.Errorf("expected '[compaction]' label, got %q", joined)
	}
	if !strings.Contains(joined, "5,000") {
		t.Errorf("expected formatted token count '5,000', got %q", joined)
	}
}

func TestCompactionSummaryMessage_Expanded(t *testing.T) {
	msg := &core.CompactionSummaryMessage{
		Role:         "compactionSummary",
		Summary:      "compacted context details",
		TokensBefore: 5000,
	}
	comp := NewCompactionSummaryMessageComponent(msg, nil)
	comp.SetExpanded(true)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "5,000") {
		t.Errorf("expected token count in expanded output, got %q", joined)
	}
	if !strings.Contains(joined, "compacted context details") {
		t.Errorf("expected summary content in expanded output, got %q", joined)
	}
}
