package agent

import "testing"

func TestValidStatus(t *testing.T) {
	for _, s := range []string{"pending", "in_progress", "completed"} {
		if !ValidStatus(s) {
			t.Errorf("ValidStatus(%q) = false, want true", s)
		}
	}
	if ValidStatus("unknown") {
		t.Error("ValidStatus(\"unknown\") = true, want false")
	}
}

func TestValidPriority(t *testing.T) {
	for _, p := range []string{"high", "medium", "low"} {
		if !ValidPriority(p) {
			t.Errorf("ValidPriority(%q) = false, want true", p)
		}
	}
	if ValidPriority("critical") {
		t.Error("ValidPriority(\"critical\") = true, want false")
	}
}

func TestPlanEntryDefaults(t *testing.T) {
	e := PlanEntry{Content: "do stuff"}
	if e.Status != "" {
		t.Errorf("zero Status = %q, want empty", e.Status)
	}
	if e.Priority != "" {
		t.Errorf("zero Priority = %q, want empty", e.Priority)
	}
}
