// Ported from: packages/tui/src/fuzzy.ts
// Upstream hash: 1caadb2e
package tui

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// FuzzyMatch holds the result of a fuzzy match.
type FuzzyMatch struct {
	Matches bool
	Score   float64
}

// wordBoundaryChars matches characters that precede word boundaries.
var wordBoundaryRe = regexp.MustCompile(`[\s\-_./:]`)

// FuzzyMatchQuery performs fuzzy matching of query against text.
// All query characters must appear in text in order (not necessarily consecutive).
// Lower score = better match.
func FuzzyMatchQuery(query, text string) FuzzyMatch {
	queryLower := strings.ToLower(query)
	textLower := strings.ToLower(text)

	primaryMatch := matchQuery(queryLower, textLower)
	if primaryMatch.Matches {
		return primaryMatch
	}

	// Try swapping alpha/numeric segments (e.g., "4o" → "o4")
	swapped := swapAlphaNumeric(queryLower)
	if swapped == "" {
		return primaryMatch
	}

	swappedMatch := matchQuery(swapped, textLower)
	if !swappedMatch.Matches {
		return primaryMatch
	}

	return FuzzyMatch{Matches: true, Score: swappedMatch.Score + 5}
}

func matchQuery(normalizedQuery, textLower string) FuzzyMatch {
	if len(normalizedQuery) == 0 {
		return FuzzyMatch{Matches: true, Score: 0}
	}
	if len(normalizedQuery) > len(textLower) {
		return FuzzyMatch{Matches: false, Score: 0}
	}

	queryRunes := []rune(normalizedQuery)
	textRunes := []rune(textLower)
	queryIndex := 0
	var score float64
	lastMatchIndex := -1
	consecutiveMatches := 0

	for i := 0; i < len(textRunes) && queryIndex < len(queryRunes); i++ {
		if textRunes[i] == queryRunes[queryIndex] {
			isWordBoundary := i == 0 || isWordBoundaryChar(textRunes[i-1])

			if lastMatchIndex == i-1 {
				consecutiveMatches++
				score -= float64(consecutiveMatches) * 5
			} else {
				consecutiveMatches = 0
				if lastMatchIndex >= 0 {
					score += float64(i-lastMatchIndex-1) * 2
				}
			}

			if isWordBoundary {
				score -= 10
			}

			score += float64(i) * 0.1

			lastMatchIndex = i
			queryIndex++
		}
	}

	if queryIndex < len(queryRunes) {
		return FuzzyMatch{Matches: false, Score: 0}
	}

	return FuzzyMatch{Matches: true, Score: score}
}

func isWordBoundaryChar(r rune) bool {
	return unicode.IsSpace(r) || r == '-' || r == '_' || r == '.' || r == '/' || r == ':'
}

// swapAlphaNumeric swaps alpha and numeric segments.
// "abc123" → "123abc", "123abc" → "abc123", otherwise "".
var alphaNumericRe = regexp.MustCompile(`^([a-z]+)([0-9]+)$`)
var numericAlphaRe = regexp.MustCompile(`^([0-9]+)([a-z]+)$`)

func swapAlphaNumeric(query string) string {
	if m := alphaNumericRe.FindStringSubmatch(query); m != nil {
		return m[2] + m[1]
	}
	if m := numericAlphaRe.FindStringSubmatch(query); m != nil {
		return m[2] + m[1]
	}
	return ""
}

// FuzzyFilter filters and sorts items by fuzzy match quality (best matches first).
// Supports space-separated tokens: all tokens must match.
func FuzzyFilter[T any](items []T, query string, getText func(T) string) []T {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		result := make([]T, len(items))
		copy(result, items)
		return result
	}

	tokens := strings.Fields(trimmed)
	if len(tokens) == 0 {
		result := make([]T, len(items))
		copy(result, items)
		return result
	}

	type scored struct {
		item       T
		totalScore float64
	}

	var results []scored

	for _, item := range items {
		text := getText(item)
		var totalScore float64
		allMatch := true

		for _, token := range tokens {
			m := FuzzyMatchQuery(token, text)
			if m.Matches {
				totalScore += m.Score
			} else {
				allMatch = false
				break
			}
		}

		if allMatch {
			results = append(results, scored{item: item, totalScore: totalScore})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].totalScore < results[j].totalScore
	})

	out := make([]T, len(results))
	for i, r := range results {
		out[i] = r.item
	}
	return out
}
