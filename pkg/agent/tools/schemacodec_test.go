package tools

import (
	"reflect"
	"testing"
)

// testCodec mirrors the plan schema shape (array of records with two enum
// fields) so the generic codec is exercised independently of the plan tool.
func testCodec() *schemaCodec {
	return newSchemaCodec(
		schemaField{
			Full:    "entries",
			IsArray: true,
			Item: newSchemaCodec(
				schemaField{Full: "content", Short: "c"},
				schemaField{Full: "priority", Short: "p", Enum: map[string]string{
					"high": "h", "medium": "m", "low": "l",
				}},
				schemaField{Full: "status", Short: "s", Enum: map[string]string{
					"pending": "p", "in_progress": "i", "completed": "x",
				}},
			),
		},
	)
}

func TestSchemaCodec_DecodeCompact(t *testing.T) {
	c := testCodec()
	in := map[string]any{
		"entries": []any{
			map[string]any{"c": "step 1", "p": "h", "s": "i"},
			map[string]any{"c": "step 2", "p": "l", "s": "x"},
		},
	}
	got, err := c.Decode(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"entries": []any{
			map[string]any{"content": "step 1", "priority": "high", "status": "in_progress"},
			map[string]any{"content": "step 2", "priority": "low", "status": "completed"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decode compact\n got=%#v\nwant=%#v", got, want)
	}
}

func TestSchemaCodec_DecodeFullKeysTolerant(t *testing.T) {
	c := testCodec()
	// Old-transcript shape: full keys + full enum values must decode unchanged.
	in := map[string]any{
		"entries": []any{
			map[string]any{"content": "a", "priority": "medium", "status": "pending"},
		},
	}
	got, err := c.Decode(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"entries": []any{
			map[string]any{"content": "a", "priority": "medium", "status": "pending"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decode full keys\n got=%#v\nwant=%#v", got, want)
	}
}

func TestSchemaCodec_DecodeMixed(t *testing.T) {
	c := testCodec()
	// A mix of short and full within the same call must all expand.
	in := map[string]any{
		"entries": []any{
			map[string]any{"c": "a", "priority": "high", "s": "p"},
		},
	}
	got, err := c.Decode(in)
	if err != nil {
		t.Fatal(err)
	}
	e := got["entries"].([]any)[0].(map[string]any)
	if e["content"] != "a" || e["priority"] != "high" || e["status"] != "pending" {
		t.Fatalf("mixed decode = %#v", e)
	}
}

func TestSchemaCodec_RoundTrip(t *testing.T) {
	c := testCodec()
	canonical := map[string]any{
		"entries": []any{
			map[string]any{"content": "x", "priority": "high", "status": "completed"},
		},
	}
	compact, err := c.Encode(canonical)
	if err != nil {
		t.Fatal(err)
	}
	e := compact["entries"].([]any)[0].(map[string]any)
	if e["c"] != "x" || e["p"] != "h" || e["s"] != "x" {
		t.Fatalf("encode = %#v", e)
	}
	back, err := c.Decode(compact)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, canonical) {
		t.Fatalf("round trip\n got=%#v\nwant=%#v", back, canonical)
	}
}

func TestSchemaCodec_UndeclaredKeysPassThrough(t *testing.T) {
	c := testCodec()
	in := map[string]any{
		"title":    "keep me",
		"metadata": map[string]any{"k": "v"},
	}
	got, err := c.Decode(in)
	if err != nil {
		t.Fatal(err)
	}
	if got["title"] != "keep me" {
		t.Fatalf("title dropped: %#v", got)
	}
	if md, ok := got["metadata"].(map[string]any); !ok || md["k"] != "v" {
		t.Fatalf("metadata dropped: %#v", got)
	}
}

func TestSchemaCodec_UnknownEnumPassThrough(t *testing.T) {
	c := testCodec()
	in := map[string]any{
		"entries": []any{map[string]any{"c": "a", "p": "bogus", "s": "??"}},
	}
	got, err := c.Decode(in)
	if err != nil {
		t.Fatal(err)
	}
	e := got["entries"].([]any)[0].(map[string]any)
	// Unknown tokens are passed through verbatim for downstream validation.
	if e["priority"] != "bogus" || e["status"] != "??" {
		t.Fatalf("unknown enum not passed through: %#v", e)
	}
}

func TestSchemaCodec_FailClosedOnStructuralError(t *testing.T) {
	c := testCodec()
	// entries declared as array but given a string -> fail closed.
	if _, err := c.Decode(map[string]any{"entries": "not an array"}); err == nil {
		t.Fatal("expected error for non-array entries")
	}
	// element declared as object but given a string -> fail closed.
	if _, err := c.Decode(map[string]any{"entries": []any{"nope"}}); err == nil {
		t.Fatal("expected error for non-object entry")
	}
}

func TestSchemaCodec_DuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate short alias")
		}
	}()
	newSchemaCodec(
		schemaField{Full: "a", Short: "x"},
		schemaField{Full: "b", Short: "x"},
	)
}

func TestSchemaCodec_EnumCollisionPanics(t *testing.T) {
	// A short code equal to another value's canonical name would make decode
	// order-dependent; the schema builder must reject it.
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on enum short/full collision")
		}
	}()
	newSchemaCodec(
		schemaField{Full: "s", Short: "s", Enum: map[string]string{
			"a": "b",
			"b": "c", // short "b" collides with canonical "b"
		}},
	)
}
