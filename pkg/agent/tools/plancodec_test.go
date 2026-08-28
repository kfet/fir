package tools

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// nestedCodec exercises the shapes the plan schema does not: a non-array
// nested object field, and a nested object *inside* an array element, so
// error wrapping can be observed at two levels.
func nestedCodec() *schemaCodec {
	return newSchemaCodec(
		schemaField{Full: "meta", Short: "m", Item: newSchemaCodec(
			schemaField{Full: "owner", Short: "o"},
			schemaField{Full: "level", Short: "l", Enum: map[string]string{"high": "h", "low": "l"}},
		)},
		schemaField{Full: "entries", Short: "e", IsArray: true, Item: newSchemaCodec(
			schemaField{Full: "content", Short: "c"},
			schemaField{Full: "meta", Short: "m", Item: newSchemaCodec(
				schemaField{Full: "owner", Short: "o"},
			)},
		)},
	)
}

// mustPanic runs fn and returns the panic value rendered as text, failing if
// fn did not panic. Any value is rendered, so a panic with an unexpected type
// is reported as a mismatch rather than as "no panic".
func mustPanic(t *testing.T, fn func()) (msg string) {
	t.Helper()
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				msg = fmt.Sprint(r)
			}
		}()
		fn()
	}()
	if !panicked {
		t.Fatal("expected a panic, got none")
	}
	return msg
}

// TestNewSchemaCodecRejectsBadSchemas pins that a broken schema declaration
// fails at construction — these codecs are built in package initialisers, so
// a panic here is a build-time-ish failure rather than a runtime surprise in
// front of a user.
func TestNewSchemaCodecRejectsBadSchemas(t *testing.T) {
	tests := []struct {
		name   string
		fields []schemaField
		want   string
	}{
		{
			name:   "empty full name",
			fields: []schemaField{{Full: "", Short: "x"}},
			want:   "field with empty Full name",
		},
		{
			name:   "duplicate full name",
			fields: []schemaField{{Full: "status", Short: "s"}, {Full: "status", Short: "t"}},
			want:   `duplicate full name "status"`,
		},
		{
			name:   "duplicate short alias",
			fields: []schemaField{{Full: "status", Short: "s"}, {Full: "size", Short: "s"}},
			want:   `duplicate short alias "s"`,
		},
		{
			name: "short code collides with another canonical value",
			// "l" is both the short code for "low" and the canonical value
			// "l" would decode to — order-dependent, so it must be refused.
			fields: []schemaField{{Full: "priority", Enum: map[string]string{"low": "l", "l": "z"}}},
			want:   "collides with a canonical enum value",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mustPanic(t, func() { newSchemaCodec(tc.fields...) })
			if !strings.Contains(got, tc.want) {
				t.Errorf("panic = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// TestNilRoundTrip pins that a nil object stays nil in both directions — a
// tool call with no arguments must not become an empty object.
func TestNilRoundTrip(t *testing.T) {
	c := nestedCodec()

	got, err := c.Decode(nil)
	if err != nil || got != nil {
		t.Errorf("Decode(nil) = %#v, %v; want nil, nil", got, err)
	}
	got, err = c.Encode(nil)
	if err != nil || got != nil {
		t.Errorf("Encode(nil) = %#v, %v; want nil, nil", got, err)
	}
}

// TestNestedObjectRoundTrip pins the non-array Item path in both directions,
// including enum compression inside the nested object.
func TestNestedObjectRoundTrip(t *testing.T) {
	c := nestedCodec()

	canonical := map[string]any{
		"meta": map[string]any{"owner": "kfet", "level": "high"},
	}
	compact := map[string]any{
		"m": map[string]any{"o": "kfet", "l": "h"},
	}

	gotCompact, err := c.Encode(canonical)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !reflect.DeepEqual(gotCompact, compact) {
		t.Fatalf("Encode\n got=%#v\nwant=%#v", gotCompact, compact)
	}

	gotCanonical, err := c.Decode(compact)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(gotCanonical, canonical) {
		t.Fatalf("Decode\n got=%#v\nwant=%#v", gotCanonical, canonical)
	}
}

// TestUndeclaredKeysPassThrough pins tolerance in both directions: a key the
// schema does not know about survives untouched, so a newer model emitting an
// extra field does not lose it.
func TestUndeclaredKeysPassThrough(t *testing.T) {
	c := nestedCodec()

	in := map[string]any{"unknown": 42, "m": map[string]any{"o": "kfet"}}
	dec, err := c.Decode(in)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if dec["unknown"] != 42 {
		t.Errorf("Decode dropped the undeclared key: %#v", dec)
	}

	enc, err := c.Encode(map[string]any{"unknown": 42, "meta": map[string]any{"owner": "kfet"}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if enc["unknown"] != 42 {
		t.Errorf("Encode dropped the undeclared key: %#v", enc)
	}
}

// TestEnumTolerance pins the two tolerant enum paths: a value that is not a
// string, and a token the schema has never heard of. Both pass through so
// downstream validation — not the codec — decides what to do.
func TestEnumTolerance(t *testing.T) {
	c := nestedCodec()

	dec, err := c.Decode(map[string]any{"m": map[string]any{"l": 3}})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got := dec["meta"].(map[string]any)["level"]; got != 3 {
		t.Errorf("non-string enum decoded to %#v, want 3 unchanged", got)
	}

	dec, err = c.Decode(map[string]any{"m": map[string]any{"l": "urgent"}})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got := dec["meta"].(map[string]any)["level"]; got != "urgent" {
		t.Errorf("unknown enum token decoded to %#v, want it unchanged", got)
	}

	enc, err := c.Encode(map[string]any{"meta": map[string]any{"level": 3}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got := enc["m"].(map[string]any)["l"]; got != 3 {
		t.Errorf("non-string enum encoded to %#v, want 3 unchanged", got)
	}

	enc, err = c.Encode(map[string]any{"meta": map[string]any{"level": "urgent"}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got := enc["m"].(map[string]any)["l"]; got != "urgent" {
		t.Errorf("unknown enum value encoded to %#v, want it unchanged", got)
	}
}

// TestStructuralErrorsFailClosed pins the fail-closed contract and, just as
// importantly, that the error names the field (and index) at fault. Both
// directions are covered because Encode is used to build wire payloads.
func TestStructuralErrorsFailClosed(t *testing.T) {
	c := nestedCodec()

	tests := []struct {
		name    string
		decode  bool
		in      map[string]any
		wantErr string
	}{
		{
			name:    "decode: array field is not an array",
			decode:  true,
			in:      map[string]any{"e": "not an array"},
			wantErr: `field "entries": expected array, got string`,
		},
		{
			name:    "decode: array element is not an object",
			decode:  true,
			in:      map[string]any{"e": []any{map[string]any{"c": "ok"}, 7}},
			wantErr: `field "entries"[1]: expected object, got int`,
		},
		{
			name:    "decode: nested object field is not an object",
			decode:  true,
			in:      map[string]any{"m": "not an object"},
			wantErr: `field "meta": expected object, got string`,
		},
		{
			name:    "decode: error inside an array element is located",
			decode:  true,
			in:      map[string]any{"e": []any{map[string]any{"m": 5}}},
			wantErr: `field "entries"[0]: field "meta": expected object, got int`,
		},
		{
			name:    "encode: array field is not an array",
			in:      map[string]any{"entries": "not an array"},
			wantErr: `field "entries": expected array, got string`,
		},
		{
			name:    "encode: array element is not an object",
			in:      map[string]any{"entries": []any{7}},
			wantErr: `field "entries"[0]: expected object, got int`,
		},
		{
			name:    "encode: nested object field is not an object",
			in:      map[string]any{"meta": 1.5},
			wantErr: `field "meta": expected object, got float64`,
		},
		{
			name:    "encode: error inside an array element is located",
			in:      map[string]any{"entries": []any{map[string]any{"meta": "nope"}}},
			wantErr: `field "entries"[0]: field "meta": expected object, got string`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			var out map[string]any
			if tc.decode {
				out, err = c.Decode(tc.in)
			} else {
				out, err = c.Encode(tc.in)
			}
			if err == nil {
				t.Fatalf("expected an error, got %#v", out)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), tc.wantErr)
			}
			if out != nil {
				t.Errorf("a failed conversion must return no partial result, got %#v", out)
			}
		})
	}
}

// TestDecodePlanParams pins the renderer-facing entry point: compact plan
// arguments expand, full-name arguments (what the external agent plan tool
// emits) survive unchanged, and a structurally broken payload is returned
// as-is so best-effort rendering can still show something.
func TestDecodePlanParams(t *testing.T) {
	compact := map[string]any{
		"entries": []any{
			map[string]any{"c": "write tests", "p": "h", "s": "i"},
		},
	}
	want := map[string]any{
		"entries": []any{
			map[string]any{"content": "write tests", "priority": "high", "status": "in_progress"},
		},
	}
	if got := DecodePlanParams(compact); !reflect.DeepEqual(got, want) {
		t.Errorf("DecodePlanParams(compact)\n got=%#v\nwant=%#v", got, want)
	}

	full := map[string]any{
		"entries": []any{
			map[string]any{"content": "write tests", "priority": "high", "status": "in_progress"},
		},
	}
	if got := DecodePlanParams(full); !reflect.DeepEqual(got, want) {
		t.Errorf("DecodePlanParams(full-name)\n got=%#v\nwant=%#v", got, want)
	}

	// Structurally invalid: the codec fails closed, and the caller gets the
	// original params back rather than nil.
	broken := map[string]any{"entries": "not an array"}
	got := DecodePlanParams(broken)
	if !reflect.DeepEqual(got, broken) {
		t.Errorf("DecodePlanParams(broken) = %#v, want the input unchanged", got)
	}

	if got := DecodePlanParams(nil); got != nil {
		t.Errorf("DecodePlanParams(nil) = %#v, want nil", got)
	}
}
