package ai

import "testing"

func TestSplitSystemPrompt(t *testing.T) {
	stable, volatile := SplitSystemPrompt("A" + SystemPromptCacheBoundary + "B" + SystemPromptCacheBoundary + "C")
	if stable != "A" || volatile != "BC" {
		t.Fatalf("got %q / %q", stable, volatile)
	}
	stable, volatile = SplitSystemPrompt("plain")
	if stable != "" || volatile != "plain" {
		t.Fatalf("no boundary: got %q / %q", stable, volatile)
	}
}

func TestFlattenSystemPrompt(t *testing.T) {
	if got := FlattenSystemPrompt("A" + SystemPromptCacheBoundary + "B"); got != "AB" {
		t.Fatalf("got %q", got)
	}
}
