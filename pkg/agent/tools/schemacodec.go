package tools

import "fmt"

// schemacodec implements a reusable auto-schema-compression codec.
//
// Motivation (see docs/design/transcript-footprint.md, P3 + "Generalisation"):
// tools whose arguments are repeated structured records — arrays of small
// objects where the same keys recur many times — pay a large transcript cost in
// repeated keys and verbose enum values. The codec lets a tool advertise a
// *compressed wire schema* (short field names + short enum codes) to the model
// while keeping handlers, stored state, and validation on the *canonical*
// full-name form.
//
// Design constraints (learned the hard way, per the spec):
//   - Aliases are DECLARED explicitly per field, never auto-derived from names.
//     Minimal-unique-prefix schemes collide and shift under schema evolution,
//     silently breaking old transcripts. Declared aliases are stable and
//     reviewable. "Auto" means the codec machinery is generic; the aliases are
//     hand-declared.
//   - Canonical form is the source of truth. Compression is purely an edge
//     transform; everything downstream uses full names.
//   - Decode is TOLERANT: it accepts both the short and the full key, and both
//     the short code and the full enum value, so old (full-keyed) transcripts
//     still decode and render unchanged alongside new (short) ones.
//   - Decode FAILS CLOSED on structural invalidity (e.g. a declared array field
//     whose value is not an array). Callers run their normal validation on the
//     canonical result afterward.

// schemaField declares the aliasing for a single field.
type schemaField struct {
	// Full is the canonical field name (source of truth).
	Full string
	// Short is the declared compact alias emitted on the wire. If empty, the
	// field is not compressed and Full is used in both forms.
	Short string
	// Enum maps each canonical enum value to its short wire code. Optional;
	// only set for enum-valued fields.
	Enum map[string]string
	// Item, when non-nil, describes the schema of nested objects. Combine with
	// IsArray for an array-of-objects field; otherwise it describes a single
	// nested object value.
	Item *schemaCodec
	// IsArray marks a field whose value is an array of objects described by
	// Item.
	IsArray bool
}

// schemaCodec compresses/expands a single object shape, driven by an ordered
// list of explicitly-declared field aliases.
type schemaCodec struct {
	fields  []schemaField
	byFull  map[string]int // canonical name -> index into fields
	byShort map[string]int // short alias -> index into fields
	// enumDecode[fieldIndex] maps any accepted token (short code OR full value)
	// to the canonical full value.
	enumDecode map[int]map[string]string
	// enumEncode[fieldIndex] maps the canonical full value to its short code.
	enumEncode map[int]map[string]string
}

// newSchemaCodec builds a codec from declared fields. It panics on duplicate
// full names or short aliases — a programming error in the schema declaration,
// not a runtime condition.
func newSchemaCodec(fields ...schemaField) *schemaCodec {
	c := &schemaCodec{
		fields:     fields,
		byFull:     make(map[string]int, len(fields)),
		byShort:    make(map[string]int, len(fields)),
		enumDecode: make(map[int]map[string]string),
		enumEncode: make(map[int]map[string]string),
	}
	for i, f := range fields {
		if f.Full == "" {
			panic("schemacodec: field with empty Full name")
		}
		if _, dup := c.byFull[f.Full]; dup {
			panic(fmt.Sprintf("schemacodec: duplicate full name %q", f.Full))
		}
		c.byFull[f.Full] = i
		if f.Short != "" {
			if _, dup := c.byShort[f.Short]; dup {
				panic(fmt.Sprintf("schemacodec: duplicate short alias %q", f.Short))
			}
			c.byShort[f.Short] = i
		}
		if len(f.Enum) > 0 {
			dec := make(map[string]string, len(f.Enum)*2)
			enc := make(map[string]string, len(f.Enum))
			// Build the full-value set first so we can detect a short code that
			// collides with another value's canonical name — that would make
			// decode order-dependent (non-deterministic map iteration). The
			// whole point of declared aliases is to fail loud on bad schemas.
			fullSet := make(map[string]struct{}, len(f.Enum))
			for full := range f.Enum {
				fullSet[full] = struct{}{}
			}
			for full, short := range f.Enum {
				dec[full] = full // full value decodes to itself (tolerant)
				if short != "" {
					if _, clash := fullSet[short]; clash {
						panic(fmt.Sprintf("schemacodec: field %q short code %q collides with a canonical enum value", f.Full, short))
					}
					dec[short] = full // short code decodes to full
					enc[full] = short
				}
			}
			c.enumDecode[i] = dec
			c.enumEncode[i] = enc
		}
	}
	return c
}

// Decode expands a (possibly compressed) wire object to canonical full-name
// form. It is tolerant of full-keyed input so old transcripts still decode.
// Keys not declared in the schema pass through unchanged. It fails closed on
// structural type mismatches (e.g. a declared array field that is not an
// array).
func (c *schemaCodec) Decode(raw map[string]any) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		idx, known := c.fieldIndex(k)
		if !known {
			// Undeclared key: pass through unchanged (tolerant).
			out[k] = v
			continue
		}
		f := c.fields[idx]
		expanded, err := c.decodeValue(idx, f, v)
		if err != nil {
			return nil, err
		}
		out[f.Full] = expanded
	}
	return out, nil
}

// Encode compresses a canonical full-name object to its compact wire form.
// Used for tests and for any caller that wants to emit the compressed shape.
func (c *schemaCodec) Encode(canonical map[string]any) (map[string]any, error) {
	if canonical == nil {
		return nil, nil
	}
	out := make(map[string]any, len(canonical))
	for k, v := range canonical {
		idx, known := c.byFull[k]
		if !known {
			out[k] = v
			continue
		}
		f := c.fields[idx]
		compact, err := c.encodeValue(idx, f, v)
		if err != nil {
			return nil, err
		}
		key := f.Full
		if f.Short != "" {
			key = f.Short
		}
		out[key] = compact
	}
	return out, nil
}

// fieldIndex resolves a wire key (short alias preferred, then full name) to a
// field index.
func (c *schemaCodec) fieldIndex(key string) (int, bool) {
	if idx, ok := c.byShort[key]; ok {
		return idx, true
	}
	if idx, ok := c.byFull[key]; ok {
		return idx, true
	}
	return 0, false
}

func (c *schemaCodec) decodeValue(idx int, f schemaField, v any) (any, error) {
	switch {
	case f.IsArray && f.Item != nil:
		arr, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("field %q: expected array, got %T", f.Full, v)
		}
		decoded := make([]any, len(arr))
		for i, el := range arr {
			obj, ok := el.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("field %q[%d]: expected object, got %T", f.Full, i, el)
			}
			d, err := f.Item.Decode(obj)
			if err != nil {
				return nil, fmt.Errorf("field %q[%d]: %w", f.Full, i, err)
			}
			decoded[i] = d
		}
		return decoded, nil
	case f.Item != nil:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field %q: expected object, got %T", f.Full, v)
		}
		return f.Item.Decode(obj)
	case len(f.Enum) > 0:
		s, ok := v.(string)
		if !ok {
			// Non-string enum value: leave as-is, let downstream validate.
			return v, nil
		}
		if full, ok := c.enumDecode[idx][s]; ok {
			return full, nil
		}
		// Unknown token: pass through unchanged (tolerant); downstream
		// validation decides whether to reject or coerce.
		return s, nil
	default:
		return v, nil
	}
}

func (c *schemaCodec) encodeValue(idx int, f schemaField, v any) (any, error) {
	switch {
	case f.IsArray && f.Item != nil:
		arr, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("field %q: expected array, got %T", f.Full, v)
		}
		encoded := make([]any, len(arr))
		for i, el := range arr {
			obj, ok := el.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("field %q[%d]: expected object, got %T", f.Full, i, el)
			}
			e, err := f.Item.Encode(obj)
			if err != nil {
				return nil, fmt.Errorf("field %q[%d]: %w", f.Full, i, err)
			}
			encoded[i] = e
		}
		return encoded, nil
	case f.Item != nil:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field %q: expected object, got %T", f.Full, v)
		}
		return f.Item.Encode(obj)
	case len(f.Enum) > 0:
		s, ok := v.(string)
		if !ok {
			return v, nil
		}
		if short, ok := c.enumEncode[idx][s]; ok {
			return short, nil
		}
		return s, nil
	default:
		return v, nil
	}
}
