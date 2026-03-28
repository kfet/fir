package oauth

import (
	"strings"
	"testing"
)

func TestRenderAuthPage_BasicRendering(t *testing.T) {
	result := renderAuthPage("Test Title", "✓", "Test Heading", "Test message")

	for _, want := range []string{"Test Title", "Test Heading", "Test message", "✓"} {
		if !strings.Contains(result, want) {
			t.Errorf("rendered page missing expected text %q", want)
		}
	}
}

func TestRenderAuthPage_HTMLEscaping(t *testing.T) {
	result := renderAuthPage("<script>alert(1)</script>", "⚠️", "a&b", "x<y")
	for _, bad := range []string{"<script>", "</script>"} {
		if strings.Contains(result, bad) {
			t.Errorf("rendered page contains unescaped HTML %q", bad)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "a&amp;b", "x&lt;y"} {
		if !strings.Contains(result, want) {
			t.Errorf("rendered page missing escaped text %q", want)
		}
	}
}
