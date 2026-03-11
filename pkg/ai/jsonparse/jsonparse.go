// Ported from: packages/ai/src/utils/json-parse.ts
// Upstream hash: 1caadb2e
package jsonparse

import (
	"encoding/json"
	"strings"
)

// ParseStreamingJSON attempts to parse potentially incomplete JSON during streaming.
// Always returns a valid map, even if the JSON is incomplete or invalid.
func ParseStreamingJSON(partial string) map[string]any {
	if partial == "" || strings.TrimSpace(partial) == "" {
		return map[string]any{}
	}

	// Try standard parsing first (fastest for complete JSON)
	var result map[string]any
	if err := json.Unmarshal([]byte(partial), &result); err == nil {
		return result
	}

	// Try to repair and parse
	fixed := repairPartialJSON(partial)
	if err := json.Unmarshal([]byte(fixed), &result); err == nil {
		return result
	}

	return map[string]any{}
}

// repairPartialJSON attempts to make incomplete JSON parseable.
// Strategy: close unclosed strings, remove trailing incomplete tokens,
// and close open brackets/braces.
func repairPartialJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}"
	}

	// Scan to understand structure
	inString := false
	escaped := false
	var stack []byte

	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}

	// Close unclosed string
	result := s
	if inString {
		result += `"`
	}

	// Try closing structures as-is
	attempt := result
	for i := len(stack) - 1; i >= 0; i-- {
		attempt += string(stack[i])
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(attempt), &parsed); err == nil {
		return attempt
	}

	// Failed — progressively strip trailing tokens and retry
	// Common issues: trailing comma, partial key without value
	for {
		trimmed := strings.TrimRight(strings.TrimSpace(result), " \t\n\r")
		if trimmed == "" {
			return "{}"
		}
		last := trimmed[len(trimmed)-1]
		if last == ',' || last == ':' {
			result = trimmed[:len(trimmed)-1]
			continue
		}
		// If last char is a quote that's an orphan key, remove the key
		if last == '"' {
			// Find the opening quote of this string
			pos := strings.LastIndex(trimmed[:len(trimmed)-1], `"`)
			if pos >= 0 {
				// Check if this string is a key (preceded by comma or brace)
				before := strings.TrimRight(trimmed[:pos], " \t\n\r")
				if len(before) > 0 && (before[len(before)-1] == ',' || before[len(before)-1] == '{') {
					result = before
					if result[len(result)-1] == ',' {
						result = result[:len(result)-1]
					}
					continue
				}
			}
		}
		break
	}

	// Re-scan for stack after our modifications
	stack = stack[:0]
	inString = false
	escaped = false
	for i := 0; i < len(result); i++ {
		c := result[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}

	// Close remaining structures
	for i := len(stack) - 1; i >= 0; i-- {
		result += string(stack[i])
	}

	return result
}
