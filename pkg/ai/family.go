package ai

import (
	"regexp"
	"strconv"
	"strings"
)

// Model-id lineage heuristics, shared by cmd/generate-models (SWE-score
// inheritance, new-model reporting) and pkg/models (stale default-pin
// detection). One implementation on purpose: two copies of a heuristic this
// fuzzy would drift, and the two callers would then disagree about which
// models are relatives.
//
// Three views of the same tokenisation:
//
//	id                        ExtractFamily     ExtractLineage   GenerationVector
//	claude-opus-4-6           claude-opus-4     claude-opus      [4 6]
//	claude-opus-5             claude-opus-5     claude-opus      [5]
//	gemini-3.1-pro-preview    gemini-3-pro      gemini-pro       [3 1]
//	kimi-k2-thinking          kimi-k2-thinking  kimi-k2-thinking (none)
//
// ExtractFamily keeps the generation, so different generations of the same
// product are DIFFERENT families — that is what makes it right for score
// inheritance and wrong for "is this pin an older sibling of that default".
// ExtractLineage plus GenerationVector answer the latter.

// dateSuffixRe matches date suffixes like -20241022 at end of model IDs.
var dateSuffixRe = regexp.MustCompile(`-\d{8}$`)

// bedrockRevisionRe matches the "-v1" API-revision suffix that Bedrock ids
// carry ahead of their ":N" qualifier.
var bedrockRevisionRe = regexp.MustCompile(`-v\d+$`)

// familyTags are release-channel markers that never distinguish one model
// from another for lineage purposes.
var familyTags = []string{"-latest", "-preview", "-exp", "-free", "-customtools"}

// normaliseModelID strips everything that is packaging rather than identity:
// aggregator prefixes, Bedrock dotted prefixes and version suffixes, date
// stamps and release-channel tags.
func normaliseModelID(modelID string) string {
	id := modelID

	// Strip OpenRouter-style "provider/" prefix.
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		id = id[idx+1:]
	}

	// Strip Bedrock-style dotted prefixes: "us.anthropic.claude-…" → "claude-…"
	// We find the FIRST dot followed by a hyphenated model name (contains "-")
	// to avoid picking version dots like "v3.2".
	// Assumption: dotted prefixes always end with a lowercase letter segment
	// (e.g. ".claude", ".deepseek"). This is correct for all known provider
	// naming conventions as of 2026-03.
	for i := 0; i < len(id); i++ {
		if id[i] == '.' {
			after := id[i+1:]
			if len(after) > 0 && after[0] >= 'a' && after[0] <= 'z' && strings.Contains(after, "-") {
				id = after
				i = -1 // restart scan on the remaining string
			}
		}
	}

	id = strings.ToLower(id)

	// Strip Bedrock version suffixes like "-v1:0", ":0" at end. The "-vN" is
	// an API revision, not a model generation, so it is only stripped as part
	// of the full Bedrock shape (a ":N" was present) — "deepseek-v3" elsewhere
	// really is a generation.
	if idx := strings.LastIndex(id, ":"); idx >= 0 {
		id = bedrockRevisionRe.ReplaceAllString(id[:idx], "")
	}

	// Strip date suffixes: -20241022, -20250929, etc.
	id = dateSuffixRe.ReplaceAllString(id, "")

	for _, tag := range familyTags {
		id = strings.ReplaceAll(id, tag, "")
	}

	// Strip trailing dashes left over from tag removal.
	return strings.TrimRight(id, "-")
}

// modelIDTokens splits a normalised model id into hyphen-delimited tokens,
// expanding embedded version dots:
//
//   - Pure version tokens like "v3.2", "5.4" are kept as-is (isVersionToken
//     handles them).
//   - Tokens with an alpha prefix like "gemini.5" split into "gemini" + "5".
//   - Mixed tokens like "m2.5" get their trailing ".N" stripped → "m2".
//
// The result always holds at least one token: strings.Split never returns an
// empty slice and every loop iteration appends.
func modelIDTokens(modelID string) []string {
	id := normaliseModelID(modelID)
	var expanded []string
	for _, p := range strings.Split(id, "-") {
		if dot := strings.Index(p, "."); dot > 0 && !isVersionToken(p) {
			prefix, suffix := p[:dot], p[dot+1:]
			if isAlpha(prefix) && isVersionToken(suffix) {
				expanded = append(expanded, prefix, suffix)
				continue
			}
			if isVersionToken(suffix) {
				expanded = append(expanded, prefix)
				continue
			}
		}
		expanded = append(expanded, p)
	}
	return expanded
}

// ExtractFamily returns a normalised "base family" string for lineage grouping.
//
// The goal is to group models that are close iterations of each other (same
// generation, same class) while keeping distinct generations apart. We preserve
// the first version-like token as the "generation" to avoid grouping Claude 3
// with Claude 4, or GPT-5.1 with GPT-5.4.
//
// Examples:
//
//	claude-opus-4-6              → claude-opus-4
//	claude-opus-4-5-20251101     → claude-opus-4
//	claude-sonnet-4-5            → claude-sonnet-4
//	claude-3-5-sonnet-20241022   → claude-3-sonnet
//	claude-3-7-sonnet-20250219   → claude-3-sonnet
//	claude-3-opus-20240229       → claude-3-opus
//	gemini-3.1-pro-preview       → gemini-3-pro
//	gemini-2.5-flash             → gemini-2-flash
//	gpt-5.4-pro                  → gpt-5-pro
//	gpt-5.2-codex                → gpt-5-codex
//	gpt-5.2                      → gpt-5
//	deepseek/deepseek-v3.2       → deepseek-3
//	google/gemini-2.5-pro        → gemini-2-pro
//	minimax-m2.5                 → minimax-m2
//	grok-4-1-fast                → grok-4-fast
//	kimi-k2-thinking             → kimi-k2-thinking
//	k2p5                         → k2p5
func ExtractFamily(modelID string) string {
	tokens := modelIDTokens(modelID)

	// Classify tokens: keep word tokens and the FIRST version token (as
	// the generation marker). Drop subsequent version tokens.
	var family []string
	seenGeneration := false
	for _, p := range tokens {
		if isVersionToken(p) {
			if !seenGeneration {
				// Keep the major part of the first version as the generation.
				// "4.6" → "4", "3.1" → "3", "5.4" → "5", "v3.2" → "3"
				gen := strings.TrimPrefix(p, "v")
				if dot := strings.Index(gen, "."); dot >= 0 {
					gen = gen[:dot]
				}
				family = append(family, gen)
				seenGeneration = true
			}
			continue
		}
		family = append(family, p)
	}

	return strings.Join(family, "-")
}

// ExtractLineage is ExtractFamily with every version token dropped, so that
// successive generations of one product line share a lineage:
//
//	claude-opus-4-6 → claude-opus       claude-opus-5 → claude-opus
//	gpt-5.4         → gpt               gpt-5.5       → gpt
//	claude-3-5-sonnet-20241022 → claude-sonnet
//
// Returns "" when the id is nothing but version tokens, which callers must
// treat as "no comparable lineage".
func ExtractLineage(modelID string) string {
	tokens := modelIDTokens(modelID)
	var lineage []string
	for _, p := range tokens {
		if !isVersionToken(p) {
			lineage = append(lineage, p)
		}
	}
	return strings.Join(lineage, "-")
}

// GenerationVector returns the version numbers embedded in a model id, in
// order, as a comparable vector: claude-opus-4-6 → [4 6], gpt-5.4 → [5 4],
// gemini-3.1-pro-preview → [3 1].
//
// ok is false when the id carries no version token at all (kimi-k2-thinking,
// k2p5, deepseek-r1). Callers that use the vector to decide whether one model
// supersedes another MUST stay silent in that case rather than guess.
func GenerationVector(modelID string) (vec []int, ok bool) {
	tokens := modelIDTokens(modelID)
	for _, p := range tokens {
		if !isVersionToken(p) {
			continue
		}
		for _, part := range strings.Split(strings.TrimPrefix(p, "v"), ".") {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, false
			}
			vec = append(vec, n)
		}
	}
	return vec, len(vec) > 0
}

// CompareGenerations orders two version vectors element-wise, treating a
// missing element as 0 so that [4 6] < [5] and [5] < [5 1]. Returns -1, 0 or 1.
func CompareGenerations(a, b []int) int {
	n := max(len(a), len(b))
	at := func(v []int, i int) int {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	for i := 0; i < n; i++ {
		switch x, y := at(a, i), at(b, i); {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

// isAlpha returns true if s is non-empty and contains only lowercase ASCII letters.
func isAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// isVersionToken returns true if a hyphen-delimited token looks like a version
// number or date: pure digits, dotted digits (3.1, 4.5), "v3", "v3.2", single
// digit, etc.
func isVersionToken(s string) bool {
	if s == "" {
		return false
	}
	// "v3", "v3.2"
	stripped := s
	if stripped[0] == 'v' && len(stripped) > 1 {
		stripped = stripped[1:]
	}
	// Check if all remaining chars are digits or dots.
	for _, c := range stripped {
		if c != '.' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
