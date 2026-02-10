package components

import (
	"strings"
	"testing"
)

func TestOAuthSelectorComponent_Render(t *testing.T) {
	providers := []OAuthProvider{
		{ID: "github", Name: "GitHub", LoggedIn: true},
		{ID: "google", Name: "Google", LoggedIn: false},
	}
	comp := NewOAuthSelectorComponent("login", providers, func(string) {}, func() {})
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "GitHub") {
		t.Errorf("expected 'GitHub' in output, got %q", joined)
	}
}

func TestOAuthSelectorComponent_EmptyProviders(t *testing.T) {
	comp := NewOAuthSelectorComponent("login", nil, func(string) {}, func() {})
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "No OAuth") {
		t.Errorf("expected empty state message, got %q", joined)
	}
}
