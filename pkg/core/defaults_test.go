// Ported from: packages/coding-agent/src/core/defaults.ts
// Upstream hash: 1caadb2e
package core

import (
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
)

func TestDefaultThinkingLevel(t *testing.T) {
	if DefaultThinkingLevel != ai.ThinkingMedium {
		t.Errorf("expected 'medium', got %q", DefaultThinkingLevel)
	}
}
