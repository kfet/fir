package declcfg

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSubstituteNilContext pins that a missing context is a named error
// rather than a nil-map panic deep inside variable lookup.
func TestSubstituteNilContext(t *testing.T) {
	_, err := Substitute("${model.id}", nil)
	if err == nil {
		t.Fatal("expected an error for a nil Context")
	}
	if !strings.Contains(err.Error(), "nil Context") {
		t.Errorf("unexpected error: %v", err)
	}

	// Even a template with nothing to substitute must not be silently
	// accepted: the caller has a bug either way.
	if _, err := Substitute("plain text", nil); err == nil {
		t.Error("expected an error for a nil Context even with no expressions")
	}
}

// TestSubstituteEmptyExpression pins that ${} is rejected. It is almost
// always a typo for ${something}, and silently expanding to "" would produce
// a malformed request that fails much later.
func TestSubstituteEmptyExpression(t *testing.T) {
	for _, in := range []string{"${}", "${   }", "a-${}-b"} {
		_, err := Substitute(in, ctxFor(nil, nil))
		if err == nil {
			t.Errorf("Substitute(%q): expected an error", in)
			continue
		}
		if !strings.Contains(err.Error(), "empty expression") {
			t.Errorf("Substitute(%q) error = %v", in, err)
		}
	}
}

// TestEnvAccessDenied pins the security property the Env hook exists for: a
// config that reads env.X against a Context with no Env hook is refused, so
// a provider template cannot exfiltrate arbitrary process environment.
func TestEnvAccessDenied(t *testing.T) {
	ctx := &Context{Vars: map[string]any{"model.id": "m"}} // no Env hook

	_, err := Substitute("${env.HOME}", ctx)
	if err == nil {
		t.Fatal("expected env access to be refused with no Env hook")
	}
	if !strings.Contains(err.Error(), "env access not permitted") {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "env.HOME") {
		t.Errorf("error should name the variable, got: %v", err)
	}

	// Non-env variables still resolve against the same Context.
	if got, err := Substitute("${model.id}", ctx); err != nil || got != "m" {
		t.Errorf("Substitute = %q, %v; want \"m\", nil", got, err)
	}
}

// TestFnArgErrors pins the argument contracts of the built-in functions.
func TestFnArgErrors(t *testing.T) {
	_, err := Substitute("${fn.unix_millis(nope)}", ctxFor(nil, nil))
	if err == nil {
		t.Fatal("expected unix_millis to reject arguments")
	}
	if !strings.Contains(err.Error(), "takes no args") {
		t.Errorf("unexpected error: %v", err)
	}

	// An unterminated quote in an argument list is reported, not silently
	// treated as the rest of the expression.
	_, err = Substitute("${fn.rand_id('oops)}", ctxFor(nil, nil))
	if err == nil {
		t.Fatal("expected an unterminated quote to be rejected")
	}
	if !strings.Contains(err.Error(), "unterminated single-quoted string") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestFnRandIDWithoutPrefix pins the unprefixed shape: "<millis>-<9 chars>",
// with no leading separator. Request ids built from it end up in provider
// logs, so the shape is part of the contract.
func TestFnRandIDWithoutPrefix(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		got, err := Substitute("${fn.rand_id()}", ctxFor(nil, nil))
		if err != nil {
			t.Fatalf("Substitute: %v", err)
		}
		if strings.HasPrefix(got, "-") {
			t.Fatalf("rand_id() = %q, want no leading separator", got)
		}
		millis, suffix, ok := strings.Cut(got, "-")
		if !ok {
			t.Fatalf("rand_id() = %q, want <millis>-<suffix>", got)
		}
		if millis == "" || strings.ContainsFunc(millis, func(r rune) bool { return r < '0' || r > '9' }) {
			t.Fatalf("rand_id() timestamp part = %q, want digits", millis)
		}
		if len(suffix) != 9 {
			t.Fatalf("rand_id() suffix = %q, want 9 characters", suffix)
		}
		for _, r := range suffix {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", r) {
				t.Fatalf("rand_id() suffix %q contains %q, outside the alphabet", suffix, r)
			}
		}
		if seen[suffix] {
			t.Fatalf("rand_id() repeated the suffix %q in %d draws", suffix, i+1)
		}
		seen[suffix] = true
	}
}

// TestStringifyValueKinds pins how each JSON-decodable value type reaches a
// header or URL. Numbers are the load-bearing case: JSON decodes every
// number to float64, and a model's max_tokens rendered as "4096" rather than
// "4096.000000" is the difference between a valid request and a rejected one.
func TestStringifyValueKinds(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want string
	}{
		{"nil", nil, ""},
		{"string", "text", "text"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"int", 42, "42"},
		{"int64", int64(-7), "-7"},
		{"whole float64", float64(4096), "4096"},
		{"negative whole float64", float64(-1), "-1"},
		{"fractional float64", 0.25, "0.25"},
		{"json.Number", json.Number("1e309"), "1e309"},
		{"other type", []string{"a", "b"}, "[a b]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ctxFor(map[string]any{"v": tc.val}, nil)
			got, err := Substitute("${v}", ctx)
			if err != nil {
				t.Fatalf("Substitute: %v", err)
			}
			if got != tc.want {
				t.Errorf("Substitute(${v}) with %#v = %q, want %q", tc.val, got, tc.want)
			}
		})
	}
}

// TestStringifyDecodedJSONNumbers pins the same property end to end, through
// a real json.Unmarshal rather than hand-built values — this is how Vars
// actually get populated.
func TestStringifyDecodedJSONNumbers(t *testing.T) {
	var vars map[string]any
	if err := json.Unmarshal([]byte(`{"max_tokens":4096,"temp":0.7}`), &vars); err != nil {
		t.Fatal(err)
	}
	ctx := ctxFor(vars, nil)

	got, err := Substitute("${max_tokens}/${temp}", ctx)
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if want := "4096/0.7"; got != want {
		t.Errorf("Substitute = %q, want %q", got, want)
	}
}

// TestSubstituteJSONErrorPathsNameTheLocation pins that a failure deep in an
// envelope names the path to it. These templates are hand-written provider
// config; "undefined variable" with no location is close to undebuggable.
func TestSubstituteJSONErrorPathsNameTheLocation(t *testing.T) {
	ctx := ctxFor(map[string]any{"known": "ok"}, nil)

	var node any
	if err := json.Unmarshal([]byte(`{
		"request": {"headers": {"x": "${missing}"}}
	}`), &node); err != nil {
		t.Fatal(err)
	}
	_, err := SubstituteJSON(node, ctx, nil)
	if err == nil {
		t.Fatal("expected an error for an undefined variable")
	}
	for _, want := range []string{`at key "request"`, `at key "headers"`, `at key "x"`, "undefined variable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v missing %q", err, want)
		}
	}

	if err := json.Unmarshal([]byte(`{"tools": ["${known}", "${missing}"]}`), &node); err != nil {
		t.Fatal(err)
	}
	_, err = SubstituteJSON(node, ctx, nil)
	if err == nil {
		t.Fatal("expected an error from inside the array")
	}
	if !strings.Contains(err.Error(), "at [1]") {
		t.Errorf("error should name the array index, got: %v", err)
	}
}

// TestSubstituteJSONNilNode pins that a JSON null passes through untouched
// rather than becoming the string "" — a null in an envelope is meaningful
// to providers.
func TestSubstituteJSONNilNode(t *testing.T) {
	got, err := SubstituteJSON(nil, ctxFor(nil, nil), "inner")
	if err != nil {
		t.Fatalf("SubstituteJSON: %v", err)
	}
	if got != nil {
		t.Errorf("SubstituteJSON(nil) = %#v, want nil", got)
	}

	var node any
	if err := json.Unmarshal([]byte(`{"stop": null, "n": 1}`), &node); err != nil {
		t.Fatal(err)
	}
	out, err := SubstituteJSON(node, ctxFor(nil, nil), nil)
	if err != nil {
		t.Fatalf("SubstituteJSON: %v", err)
	}
	m := out.(map[string]any)
	if v, ok := m["stop"]; !ok || v != nil {
		t.Errorf("null value = %#v (present=%v), want a preserved null", v, ok)
	}
}
