package declcfg

import (
	"encoding/json"
	"strings"
	"testing"
)

func ctxFor(vars map[string]any, env map[string]string) *Context {
	return &Context{
		Vars: vars,
		Env: func(name string) (string, bool) {
			v, ok := env[name]
			return v, ok
		},
	}
}

func TestSubstitute_Basic(t *testing.T) {
	ctx := ctxFor(map[string]any{
		"model.id":           "gemini-3.1-pro",
		"creds.access_token": "tok-xyz",
		"os":                 "darwin",
	}, nil)
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"${model.id}", "gemini-3.1-pro"},
		{"prefix-${model.id}-suffix", "prefix-gemini-3.1-pro-suffix"},
		{"Bearer ${creds.access_token}", "Bearer tok-xyz"},
		{"$$literal", "$literal"},
		{"$ alone", "$ alone"},
		{"two: ${model.id} ${os}", "two: gemini-3.1-pro darwin"},
	}
	for _, c := range cases {
		got, err := Substitute(c.in, ctx)
		if err != nil {
			t.Errorf("Substitute(%q) err=%v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Substitute(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSubstitute_Errors(t *testing.T) {
	ctx := ctxFor(map[string]any{"x": "y"}, nil)
	cases := []struct {
		in      string
		errFrag string
	}{
		{"${unterminated", "unterminated"},
		{"${missing.var}", "undefined variable"},
		{"${env.NOPE}", "env var NOPE"}, // env nil-bridge handled by ctxFor → Env returns ok=false
		{"${fn.unknown()}", "unknown function"},
		{"${fn.rand_id(}", "malformed function call"},
	}
	for _, c := range cases {
		_, err := Substitute(c.in, ctx)
		if err == nil {
			t.Errorf("Substitute(%q) expected error", c.in)
			continue
		}
		if !strings.Contains(err.Error(), c.errFrag) {
			t.Errorf("Substitute(%q) err=%v, want fragment %q", c.in, err, c.errFrag)
		}
	}
}

func TestSubstitute_EnvAllowlist(t *testing.T) {
	allow := map[string]string{"GEMINI_API_KEY": "ak"}
	ctx := ctxFor(nil, allow)
	got, err := Substitute("${env.GEMINI_API_KEY}", ctx)
	if err != nil || got != "ak" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := Substitute("${env.PATH}", ctx); err == nil {
		t.Fatal("expected env.PATH to be denied (not in allowlist)")
	}
}

func TestSubstitute_FnRandID(t *testing.T) {
	ctx := ctxFor(nil, nil)
	got, err := Substitute("${fn.rand_id('sample')}", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "sample-") {
		t.Errorf("got=%q want prefix sample-", got)
	}
	parts := strings.Split(got, "-")
	if len(parts) != 3 {
		t.Errorf("expected 3 dash-parts, got %d in %q", len(parts), got)
	}
	if len(parts[2]) != 9 {
		t.Errorf("expected 9-char suffix, got %q", parts[2])
	}
	// Two consecutive calls should differ (random suffix).
	got2, _ := Substitute("${fn.rand_id('sample')}", ctx)
	if got == got2 {
		t.Errorf("rand_id should differ between calls: %q == %q", got, got2)
	}
}

func TestSubstitute_FnUnixMillis(t *testing.T) {
	ctx := ctxFor(nil, nil)
	got, err := Substitute("${fn.unix_millis()}", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 10 {
		t.Errorf("expected millis-since-epoch, got %q", got)
	}
}

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ", []string{"a", "b"}},
		{"'hello, world',plain", []string{"hello, world", "plain"}},
		{"'sample'", []string{"sample"}},
	}
	for _, c := range cases {
		got, err := splitArgs(c.in)
		if err != nil {
			t.Errorf("splitArgs(%q) err=%v", c.in, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("splitArgs(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitArgs(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestSubstituteJSON_Identity(t *testing.T) {
	ctx := ctxFor(nil, nil)
	in := map[string]any{"a": "b", "n": float64(1), "k": true, "z": nil}
	out, err := SubstituteJSON(in, ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, in, out) {
		t.Errorf("identity walk mutated value: %v vs %v", in, out)
	}
}

func TestSubstituteJSON_StringSubst(t *testing.T) {
	ctx := ctxFor(map[string]any{"model.id": "gemini-3.1-pro", "creds.project_id": "proj-1"}, nil)
	in := map[string]any{
		"model":   "${model.id}",
		"project": "${creds.project_id}",
		"nested":  []any{"a", "${model.id}"},
	}
	out, err := SubstituteJSON(in, ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["model"] != "gemini-3.1-pro" {
		t.Errorf("model: got %v", m["model"])
	}
	if m["project"] != "proj-1" {
		t.Errorf("project: got %v", m["project"])
	}
	arr := m["nested"].([]any)
	if arr[1] != "gemini-3.1-pro" {
		t.Errorf("nested[1]: got %v", arr[1])
	}
}

func TestSubstituteJSON_InnerSentinel(t *testing.T) {
	ctx := ctxFor(map[string]any{"model.id": "x"}, nil)
	envelope := map[string]any{
		"model":   "${model.id}",
		"request": "$inner",
		"static":  "sample",
	}
	inner := map[string]any{"contents": []any{map[string]any{"role": "user"}}}
	out, err := SubstituteJSON(envelope, ctx, inner)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	got, ok := m["request"].(map[string]any)
	if !ok {
		t.Fatalf("request not spliced as object: %T = %v", m["request"], m["request"])
	}
	if got["contents"] == nil {
		t.Errorf("inner.contents missing after splice: %v", got)
	}
	// $inner appearing inside a non-bare string is plain text (not the sentinel).
	out2, err := SubstituteJSON(map[string]any{"k": "before-$inner-after"}, ctx, inner)
	if err != nil {
		t.Fatal(err)
	}
	if v := out2.(map[string]any)["k"]; v != "before-$inner-after" {
		t.Errorf("non-bare $inner should be plain text, got %v", v)
	}
}

func TestSubstituteJSON_RoundTripsThroughJSON(t *testing.T) {
	// What an adapter actually does: Unmarshal config → SubstituteJSON → Marshal.
	rawCfg := `{
		"model":     "${model.id}",
		"project":   "${creds.project_id}",
		"request":   "$inner",
		"userAgent": "sample"
	}`
	var node any
	if err := json.Unmarshal([]byte(rawCfg), &node); err != nil {
		t.Fatal(err)
	}
	ctx := ctxFor(map[string]any{"model.id": "gemini-3.1-pro-high", "creds.project_id": "p"}, nil)
	inner := map[string]any{"contents": "stub"}
	got, err := SubstituteJSON(node, ctx, inner)
	if err != nil {
		t.Fatal(err)
	}
	bs, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	s := string(bs)
	for _, want := range []string{
		`"model":"gemini-3.1-pro-high"`,
		`"project":"p"`,
		`"request":{"contents":"stub"}`,
		`"userAgent":"sample"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("marshalled body missing %q\nfull=%s", want, s)
		}
	}
}

func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}
