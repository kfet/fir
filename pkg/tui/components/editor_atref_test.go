package components

import "testing"

func TestMatchAtFileRef_QuoteAware(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"@", true},
		{"@foo", true},
		{"@foo/bar", true},
		{"see @foo", true},
		{`@"my fi`, true},    // open quote with a space — keep completing
		{`@"my file/`, true}, // open quote, dir traversal
		{"@foo bar", false},  // unquoted space closes the ref
		{`@"a b" `, false},   // closed quote then space closes the ref
		{"plain text", false},
		{"email@host", false}, // not at a token boundary
	}
	for _, c := range cases {
		if got := matchAtFileRef(c.text); got != c.want {
			t.Errorf("matchAtFileRef(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
