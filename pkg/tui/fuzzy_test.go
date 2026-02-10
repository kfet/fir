package tui

import (
	"testing"
)

func TestFuzzyMatch_ExactMatch(t *testing.T) {
	m := FuzzyMatchQuery("hello", "hello")
	if !m.Matches {
		t.Error("expected match")
	}
}

func TestFuzzyMatch_SubsequenceMatch(t *testing.T) {
	m := FuzzyMatchQuery("hlo", "hello")
	if !m.Matches {
		t.Error("expected subsequence match")
	}
}

func TestFuzzyMatch_CaseInsensitive(t *testing.T) {
	m := FuzzyMatchQuery("HELLO", "hello")
	if !m.Matches {
		t.Error("expected case-insensitive match")
	}
}

func TestFuzzyMatch_NoMatch(t *testing.T) {
	m := FuzzyMatchQuery("xyz", "hello")
	if m.Matches {
		t.Error("expected no match")
	}
}

func TestFuzzyMatch_EmptyQuery(t *testing.T) {
	m := FuzzyMatchQuery("", "hello")
	if !m.Matches {
		t.Error("expected empty query to match")
	}
	if m.Score != 0 {
		t.Errorf("expected score 0, got %f", m.Score)
	}
}

func TestFuzzyMatch_QueryLongerThanText(t *testing.T) {
	m := FuzzyMatchQuery("hello world", "hi")
	if m.Matches {
		t.Error("expected no match when query longer than text")
	}
}

func TestFuzzyMatch_ConsecutiveBonus(t *testing.T) {
	// "hel" in "hello" should score better than "h.e.l" spread out
	consecutive := FuzzyMatchQuery("hel", "hello")
	spread := FuzzyMatchQuery("hel", "h_e_l_l_o")
	if !consecutive.Matches || !spread.Matches {
		t.Fatal("both should match")
	}
	if consecutive.Score >= spread.Score {
		t.Errorf("consecutive should score better (lower): %f vs %f", consecutive.Score, spread.Score)
	}
}

func TestFuzzyMatch_WordBoundaryBonus(t *testing.T) {
	// "rm" at word boundary "read-mode" should score better than "rm" in "grammar"
	boundary := FuzzyMatchQuery("rm", "read-mode")
	nonBoundary := FuzzyMatchQuery("rm", "grammar")
	if !boundary.Matches || !nonBoundary.Matches {
		t.Fatal("both should match")
	}
	if boundary.Score >= nonBoundary.Score {
		t.Errorf("boundary match should score better: %f vs %f", boundary.Score, nonBoundary.Score)
	}
}

func TestFuzzyMatch_AlphaNumericSwap(t *testing.T) {
	// "4o" should match "o4" by swapping
	m := FuzzyMatchQuery("4o", "gpt-4o")
	if !m.Matches {
		t.Error("expected match via alpha-numeric swap")
	}
}

func TestFuzzyFilter_EmptyQuery(t *testing.T) {
	items := []string{"apple", "banana", "cherry"}
	result := FuzzyFilter(items, "", func(s string) string { return s })
	if len(result) != 3 {
		t.Errorf("expected all items, got %d", len(result))
	}
}

func TestFuzzyFilter_MatchesSubset(t *testing.T) {
	items := []string{"apple", "banana", "avocado", "antenna"}
	result := FuzzyFilter(items, "an", func(s string) string { return s })
	// "banana" has a,n; "antenna" has a,n; "avocado" has no 'n'
	if len(result) != 2 {
		t.Errorf("expected 2 matches (banana, antenna), got %d: %v", len(result), result)
	}
}

func TestFuzzyFilter_SortsByScore(t *testing.T) {
	items := []string{"x_h_e_l_l_o", "hello_world", "hello"}
	result := FuzzyFilter(items, "hello", func(s string) string { return s })
	if len(result) != 3 {
		t.Fatalf("expected 3 matches, got %d", len(result))
	}
	// "hello" and "hello_world" score the same (both match at position 0)
	// "x_h_e_l_l_o" should be last (worst score due to gaps)
	if result[len(result)-1] != "x_h_e_l_l_o" {
		t.Errorf("expected 'x_h_e_l_l_o' last, got %q", result[len(result)-1])
	}
}

func TestFuzzyFilter_MultipleTokens(t *testing.T) {
	items := []string{"hello world", "hello", "world hello", "goodbye"}
	result := FuzzyFilter(items, "hello world", func(s string) string { return s })
	// Both "hello world" and "world hello" contain both tokens
	if len(result) < 2 {
		t.Errorf("expected at least 2 matches, got %d", len(result))
	}
	// "goodbye" should not match
	for _, r := range result {
		if r == "goodbye" {
			t.Error("'goodbye' should not match")
		}
	}
}

func TestFuzzyFilter_NoMatches(t *testing.T) {
	items := []string{"apple", "banana"}
	result := FuzzyFilter(items, "xyz", func(s string) string { return s })
	if len(result) != 0 {
		t.Errorf("expected 0 matches, got %d", len(result))
	}
}

func TestFuzzyFilter_Struct(t *testing.T) {
	type model struct {
		name string
	}
	items := []model{{name: "claude"}, {name: "gpt-4o"}, {name: "gemini"}}
	result := FuzzyFilter(items, "gpt", func(m model) string { return m.name })
	if len(result) != 1 {
		t.Errorf("expected 1 match, got %d", len(result))
	}
	if result[0].name != "gpt-4o" {
		t.Errorf("expected gpt-4o, got %q", result[0].name)
	}
}
