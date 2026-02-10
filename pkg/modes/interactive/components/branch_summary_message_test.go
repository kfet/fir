package components

import (
	"strings"
	"testing"

	"github.com/kfet/pi-go/pkg/core"
)

func TestBranchSummaryMessage_Collapsed(t *testing.T) {
	msg := &core.BranchSummaryMessage{
		Role:    "branchSummary",
		Summary: "This is a branch summary",
		FromID:  "from-123",
	}
	comp := NewBranchSummaryMessageComponent(msg, nil)
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "branch") {
		t.Errorf("expected '[branch]' label in collapsed output, got %q", joined)
	}
	if !strings.Contains(joined, "expand") {
		t.Errorf("expected expand hint in collapsed output, got %q", joined)
	}
}

func TestBranchSummaryMessage_Expanded(t *testing.T) {
	msg := &core.BranchSummaryMessage{
		Role:    "branchSummary",
		Summary: "This is a branch summary",
		FromID:  "from-123",
	}
	comp := NewBranchSummaryMessageComponent(msg, nil)
	comp.SetExpanded(true)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "branch") {
		t.Errorf("expected '[branch]' label in expanded output, got %q", joined)
	}
	if !strings.Contains(joined, "Branch Summary") || !strings.Contains(joined, "branch summary") {
		t.Errorf("expected summary content in expanded output, got %q", joined)
	}
}
