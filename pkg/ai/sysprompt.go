package ai

import "strings"

// SystemPromptCacheBoundary separates the stable prefix of a system prompt
// (core instructions, guidelines, skills list — identical across sessions)
// from its volatile suffix (context files, appended prompts, date, cwd,
// host). The Anthropic provider splits on it and caches each half with its
// own breakpoint, so the stable prefix hits the prompt cache across
// sessions even when the suffix differs. Every other provider must flatten
// it away with FlattenSystemPrompt before sending.
//
// The marker is an in-process sentinel and never reaches any model.
const SystemPromptCacheBoundary = "\x00fir:system-cache-boundary\x00"

// SplitSystemPrompt splits a system prompt at the first cache boundary.
// Without a boundary the whole prompt is returned as volatile. Any further
// boundaries in the suffix are flattened.
func SplitSystemPrompt(s string) (stable, volatile string) {
	stable, volatile, ok := strings.Cut(s, SystemPromptCacheBoundary)
	if !ok {
		return "", s
	}
	return stable, FlattenSystemPrompt(volatile)
}

// FlattenSystemPrompt removes cache boundaries, yielding the plain prompt.
func FlattenSystemPrompt(s string) string {
	return strings.ReplaceAll(s, SystemPromptCacheBoundary, "")
}
