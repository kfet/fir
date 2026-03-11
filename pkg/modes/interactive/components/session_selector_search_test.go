package components

import (
	"testing"
	"time"

	"github.com/kfet/fir/pkg/session/store"
)

func TestParseSearchQuery_Empty(t *testing.T) {
	q := ParseSearchQuery("")
	if q.Mode != "tokens" || len(q.Tokens) != 0 {
		t.Errorf("empty query: mode=%q tokens=%d", q.Mode, len(q.Tokens))
	}
}

func TestParseSearchQuery_Regex(t *testing.T) {
	q := ParseSearchQuery("re:foo.*bar")
	if q.Mode != "regex" || q.Regex == nil {
		t.Fatal("expected regex mode with valid regex")
	}
	if !q.Regex.MatchString("fooXbar") {
		t.Error("expected regex to match fooXbar")
	}
}

func TestParseSearchQuery_BadRegex(t *testing.T) {
	q := ParseSearchQuery("re:[invalid")
	if q.Error == "" {
		t.Error("expected error for bad regex")
	}
}

func TestParseSearchQuery_Tokens(t *testing.T) {
	q := ParseSearchQuery("hello world")
	if q.Mode != "tokens" {
		t.Errorf("mode = %q, want tokens", q.Mode)
	}
	if len(q.Tokens) != 2 {
		t.Fatalf("tokens = %d, want 2", len(q.Tokens))
	}
	if q.Tokens[0].Value != "hello" || q.Tokens[0].Kind != TokenFuzzy {
		t.Errorf("token[0] = %+v", q.Tokens[0])
	}
}

func TestParseSearchQuery_QuotedPhrase(t *testing.T) {
	q := ParseSearchQuery(`foo "bar baz" qux`)
	if len(q.Tokens) != 3 {
		t.Fatalf("tokens = %d, want 3", len(q.Tokens))
	}
	if q.Tokens[1].Kind != TokenPhrase || q.Tokens[1].Value != "bar baz" {
		t.Errorf("token[1] = %+v", q.Tokens[1])
	}
}

func TestParseSearchQuery_UnclosedQuote(t *testing.T) {
	q := ParseSearchQuery(`foo "bar baz`)
	// Should fall back to plain tokens
	if q.Mode != "tokens" {
		t.Errorf("mode = %q, want tokens", q.Mode)
	}
	// All tokens should be fuzzy
	for _, tok := range q.Tokens {
		if tok.Kind != TokenFuzzy {
			t.Errorf("expected fuzzy token, got %+v", tok)
		}
	}
}

func makeSession(id, name, text, cwd string) *store.SessionListInfo {
	return &store.SessionListInfo{
		ID:           id,
		Name:         name,
		FirstMessage: text,
		Cwd:          cwd,
		Created:      time.Now(),
		Modified:     time.Now(),
	}
}

func TestMatchSession_FuzzyMatch(t *testing.T) {
	s := makeSession("sess1", "my session", "hello world", "/home")
	q := ParseSearchQuery("hello")
	res := MatchSession(s, q)
	if !res.Matches {
		t.Error("expected match")
	}
}

func TestMatchSession_NoMatch(t *testing.T) {
	s := makeSession("sess1", "my session", "hello world", "/home")
	q := ParseSearchQuery("zzzzzznotfound")
	res := MatchSession(s, q)
	if res.Matches {
		t.Error("expected no match")
	}
}

func TestMatchSession_PhraseMatch(t *testing.T) {
	s := makeSession("sess1", "", "fix the node cve issue", "/home")
	q := ParseSearchQuery(`"node cve"`)
	res := MatchSession(s, q)
	if !res.Matches {
		t.Error("expected phrase match")
	}
}

func TestMatchSession_RegexMatch(t *testing.T) {
	s := makeSession("sess1", "", "error 404 not found", "/home")
	q := ParseSearchQuery("re:error \\d+")
	res := MatchSession(s, q)
	if !res.Matches {
		t.Error("expected regex match")
	}
}

func TestFilterAndSortSessions_Empty(t *testing.T) {
	result := FilterAndSortSessions(nil, "", SortRecent, NameFilterAll)
	if len(result) != 0 {
		t.Errorf("expected empty, got %d", len(result))
	}
}

func TestFilterAndSortSessions_NameFilter(t *testing.T) {
	sessions := []*store.SessionListInfo{
		makeSession("1", "named", "text", "/"),
		makeSession("2", "", "text", "/"),
		makeSession("3", "also named", "text", "/"),
	}

	result := FilterAndSortSessions(sessions, "", SortRecent, NameFilterNamed)
	if len(result) != 2 {
		t.Errorf("expected 2 named sessions, got %d", len(result))
	}
}

func TestFilterAndSortSessions_Relevance(t *testing.T) {
	sessions := []*store.SessionListInfo{
		makeSession("1", "", "python script", "/home"),
		makeSession("2", "", "golang go module", "/home"),
	}

	result := FilterAndSortSessions(sessions, "golang", SortRelevance, NameFilterAll)
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
	if result[0].ID != "2" {
		t.Errorf("expected session 2, got %s", result[0].ID)
	}
}

func TestHasSessionName(t *testing.T) {
	if HasSessionName(makeSession("1", "", "", "/")) {
		t.Error("empty name should return false")
	}
	if HasSessionName(makeSession("1", "  ", "", "/")) {
		t.Error("whitespace name should return false")
	}
	if !HasSessionName(makeSession("1", "test", "", "/")) {
		t.Error("non-empty name should return true")
	}
}
