// Ported from: packages/coding-agent/src/modes/interactive/components/session-selector-search.ts
// Upstream hash: 1caadb2e
package components

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/tui"
)

// SortMode controls how sessions are sorted.
type SortMode string

const (
	SortThreaded  SortMode = "threaded"
	SortRecent    SortMode = "recent"
	SortRelevance SortMode = "relevance"
)

// NameFilter controls which sessions are shown.
type NameFilter string

const (
	NameFilterAll   NameFilter = "all"
	NameFilterNamed NameFilter = "named"
)

// SearchTokenKind is a token type in a parsed search query.
type SearchTokenKind string

const (
	TokenFuzzy  SearchTokenKind = "fuzzy"
	TokenPhrase SearchTokenKind = "phrase"
)

// SearchToken is a single token in a parsed search query.
type SearchToken struct {
	Kind  SearchTokenKind
	Value string
}

// ParsedSearchQuery is the result of parsing a search query string.
type ParsedSearchQuery struct {
	Mode   string // "tokens" or "regex"
	Tokens []SearchToken
	Regex  *regexp.Regexp
	Error  string // non-empty if parsing failed
}

// MatchResult holds the result of matching a session against a query.
type MatchResult struct {
	Matches bool
	Score   float64 // lower is better
}

func normalizeWhitespaceLower(text string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range strings.ToLower(text) {
		if unicode.IsSpace(r) {
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	return strings.TrimRight(b.String(), " ")
}

func getSessionSearchText(session *session.SessionListInfo) string {
	return session.ID + " " + session.Name + " " + session.AllMessagesText + " " + session.Cwd
}

// HasSessionName returns whether a session has a user-defined name.
func HasSessionName(session *session.SessionListInfo) bool {
	return strings.TrimSpace(session.Name) != ""
}

func matchesNameFilter(session *session.SessionListInfo, filter NameFilter) bool {
	if filter == NameFilterAll {
		return true
	}
	return HasSessionName(session)
}

// ParseSearchQuery parses a search query string.
func ParseSearchQuery(query string) ParsedSearchQuery {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return ParsedSearchQuery{Mode: "tokens"}
	}

	// Regex mode: re:<pattern>
	if strings.HasPrefix(trimmed, "re:") {
		pattern := strings.TrimSpace(trimmed[3:])
		if pattern == "" {
			return ParsedSearchQuery{Mode: "regex", Error: "Empty regex"}
		}
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			return ParsedSearchQuery{Mode: "regex", Error: err.Error()}
		}
		return ParsedSearchQuery{Mode: "regex", Regex: re}
	}

	// Token mode with quote support
	var tokens []SearchToken
	var buf strings.Builder
	inQuote := false
	hadUnclosedQuote := false

	flush := func(kind SearchTokenKind) {
		v := strings.TrimSpace(buf.String())
		buf.Reset()
		if v == "" {
			return
		}
		tokens = append(tokens, SearchToken{Kind: kind, Value: v})
	}

	for _, ch := range trimmed {
		if ch == '"' {
			if inQuote {
				flush(TokenPhrase)
				inQuote = false
			} else {
				flush(TokenFuzzy)
				inQuote = true
			}
			continue
		}

		if !inQuote && unicode.IsSpace(ch) {
			flush(TokenFuzzy)
			continue
		}

		buf.WriteRune(ch)
	}

	if inQuote {
		hadUnclosedQuote = true
	}

	// If quotes were unbalanced, fall back to plain whitespace tokenization
	if hadUnclosedQuote {
		tokens = nil
		for _, word := range strings.Fields(trimmed) {
			tokens = append(tokens, SearchToken{Kind: TokenFuzzy, Value: word})
		}
		return ParsedSearchQuery{Mode: "tokens", Tokens: tokens}
	}

	flush(TokenFuzzy)

	return ParsedSearchQuery{Mode: "tokens", Tokens: tokens}
}

// MatchSession checks if a session matches a parsed search query.
func MatchSession(session *session.SessionListInfo, parsed ParsedSearchQuery) MatchResult {
	text := getSessionSearchText(session)

	if parsed.Mode == "regex" {
		if parsed.Regex == nil {
			return MatchResult{Matches: false}
		}
		loc := parsed.Regex.FindStringIndex(text)
		if loc == nil {
			return MatchResult{Matches: false}
		}
		return MatchResult{Matches: true, Score: float64(loc[0]) * 0.1}
	}

	if len(parsed.Tokens) == 0 {
		return MatchResult{Matches: true}
	}

	var totalScore float64
	normalizedText := ""
	needsNormalized := false

	// Check if we need normalized text
	for _, token := range parsed.Tokens {
		if token.Kind == TokenPhrase {
			needsNormalized = true
			break
		}
	}
	if needsNormalized {
		normalizedText = normalizeWhitespaceLower(text)
	}

	for _, token := range parsed.Tokens {
		if token.Kind == TokenPhrase {
			phrase := normalizeWhitespaceLower(token.Value)
			if phrase == "" {
				continue
			}
			idx := strings.Index(normalizedText, phrase)
			if idx < 0 {
				return MatchResult{Matches: false}
			}
			totalScore += float64(idx) * 0.1
			continue
		}

		m := tui.FuzzyMatchQuery(token.Value, text)
		if !m.Matches {
			return MatchResult{Matches: false}
		}
		totalScore += m.Score
	}

	return MatchResult{Matches: true, Score: totalScore}
}

// FilterAndSortSessions filters and sorts sessions by query and options.
func FilterAndSortSessions(
	sessions []*session.SessionListInfo,
	query string,
	sortMode SortMode,
	nameFilter NameFilter,
) []*session.SessionListInfo {
	// Apply name filter
	var nameFiltered []*session.SessionListInfo
	if nameFilter == NameFilterAll {
		nameFiltered = sessions
	} else {
		for _, s := range sessions {
			if matchesNameFilter(s, nameFilter) {
				nameFiltered = append(nameFiltered, s)
			}
		}
	}

	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nameFiltered
	}

	parsed := ParseSearchQuery(query)
	if parsed.Error != "" {
		return nil
	}

	// Recent mode: filter only, keep incoming order
	if sortMode == SortRecent {
		var filtered []*session.SessionListInfo
		for _, s := range nameFiltered {
			if res := MatchSession(s, parsed); res.Matches {
				filtered = append(filtered, s)
			}
		}
		return filtered
	}

	// Relevance mode: sort by score, tie-break by modified desc
	type scored struct {
		session *session.SessionListInfo
		score   float64
	}
	var results []scored
	for _, s := range nameFiltered {
		res := MatchSession(s, parsed)
		if !res.Matches {
			continue
		}
		results = append(results, scored{session: s, score: res.Score})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score < results[j].score
		}
		return results[i].session.Modified.After(results[j].session.Modified)
	})

	out := make([]*session.SessionListInfo, len(results))
	for i, r := range results {
		out[i] = r.session
	}
	return out
}
