package tools

// planCodec declares the per-field compression aliases for the plan tool's
// `entries` records (transcript-footprint P3). The canonical full-name form
// (content/priority/status, with full enum values) is the source of truth; the
// model emits the compact wire form (c/p/s with short enum codes) and the codec
// expands it before rendering. Decode is tolerant, so transcripts using full
// keys and full enum values (e.g. those produced by the external
// github.com/kfet/agent plan tool) still decode and render unchanged.
//
// The plan tool itself now lives in github.com/kfet/agent/tools and emits the
// full-name form; this codec is kept in fir so the fir renderers can still
// decode legacy compact-form plan transcripts. The compact wire form is a
// fir-specific transcript-footprint optimisation and intentionally stays out of
// the portable agent module.
var planCodec = newSchemaCodec(
	schemaField{
		Full:    "entries",
		IsArray: true,
		Item: newSchemaCodec(
			schemaField{Full: "content", Short: "c"},
			schemaField{Full: "priority", Short: "p", Enum: map[string]string{
				"high":   "h",
				"medium": "m",
				"low":    "l",
			}},
			schemaField{Full: "status", Short: "s", Enum: map[string]string{
				"pending":     "p",
				"in_progress": "i",
				"completed":   "x",
			}},
		),
	},
)

// DecodePlanParams expands a plan tool-call's (possibly compressed) arguments
// to canonical full-name form. It is tolerant of full-keyed/full-enum input so
// both legacy compact transcripts and full-name transcripts decode. Renderers
// that read raw plan args from the transcript should run them through this
// first. On a structural error the original params are returned unchanged so
// best-effort rendering can proceed.
func DecodePlanParams(params map[string]any) map[string]any {
	decoded, err := planCodec.Decode(params)
	if err != nil {
		return params
	}
	return decoded
}
