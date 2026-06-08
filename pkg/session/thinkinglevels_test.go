package session

import (
	"reflect"
	"testing"

	"github.com/kfet/agent"
	core "github.com/kfet/ai"
)

func TestAvailableThinkingLevelsForModel_Nil(t *testing.T) {
	got := AvailableThinkingLevelsForModel(nil)
	want := []agent.ThinkingLevel{agent.ThinkingOff}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAvailableThinkingLevelsForModel_NonReasoning(t *testing.T) {
	got := AvailableThinkingLevelsForModel(&core.Model{Reasoning: false})
	want := []agent.ThinkingLevel{agent.ThinkingOff}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAvailableThinkingLevelsForModel_Reasoning(t *testing.T) {
	// Plain reasoning model with no xhigh/max.
	got := AvailableThinkingLevelsForModel(&core.Model{Reasoning: true})
	want := []agent.ThinkingLevel{
		agent.ThinkingOff,
		agent.ThinkingMinimal,
		agent.ThinkingLow,
		agent.ThinkingMedium,
		agent.ThinkingHigh,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
