// Decl-Google api kind handler — bridges ext-shipped ApiSpec(kind="decl-google")
// payloads to RegisterDeclGoogleConfig + ApiProvider registration. Lets an
// extension ship Cloud-Code-Assist Gemini wire shapes entirely from data,
// with no provider-specific Go code.

package providers

import (
	"encoding/json"
	"fmt"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/extension/apikind"
)

// declGoogleApiPayload is the JSON shape of an ApiSpec.payload when the
// kind is "decl-google". Field names are snake_case (matching the rest of
// the ext wire protocol). Mirrors the runtime DeclGoogleConfig struct,
// minus the cached parsedEnvelope plumbing.
type declGoogleApiPayload struct {
	Endpoints []string          `json:"endpoints"`
	Headers   map[string]string `json:"headers,omitempty"`

	ConditionalHeaders []declGoogleConditionalHeaderWire `json:"conditional_headers,omitempty"`

	// Envelope is the JSON template for the outer request body (string
	// form on the wire — this handler re-marshals to json.RawMessage).
	Envelope string `json:"envelope,omitempty"`

	SystemInstructionPrefix []declGoogleSysInstrPartWire `json:"system_instruction_prefix,omitempty"`
	SystemInstructionRole   string                       `json:"system_instruction_role,omitempty"`
	ReasoningHeaderPrefix   string                       `json:"reasoning_header_prefix,omitempty"`
}

type declGoogleConditionalHeaderWire struct {
	When struct {
		ModelIDPrefix     string `json:"model_id_prefix,omitempty"`
		RequiresReasoning bool   `json:"requires_reasoning,omitempty"`
	} `json:"when"`
	Set map[string]string `json:"set"`
}

type declGoogleSysInstrPartWire struct {
	Text string `json:"text"`
}

// declGoogleApiKind is registered with the extension package so any
// ext-shipped api spec with kind="decl-google" is handed to it for
// registration into the providers package's runtime maps.
type declGoogleApiKind struct{}

func (declGoogleApiKind) Register(id string, payload json.RawMessage, sourceID string) error {
	if len(payload) == 0 {
		return fmt.Errorf("decl-google: empty payload for api %q", id)
	}
	var p declGoogleApiPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decl-google: parse payload for api %q: %w", id, err)
	}

	cfg := &DeclGoogleConfig{
		Endpoints:             p.Endpoints,
		Headers:               p.Headers,
		SystemInstructionRole: p.SystemInstructionRole,
		ReasoningHeaderPrefix: p.ReasoningHeaderPrefix,
	}
	if p.Envelope != "" {
		// The wire ships the envelope as a raw JSON string. Validate it
		// up-front so a bad envelope from an extension fails the
		// register call (logged at handshake) rather than blowing up
		// the first stream attempt.
		var probe any
		if err := json.Unmarshal([]byte(p.Envelope), &probe); err != nil {
			return fmt.Errorf("decl-google: api %q envelope is not valid JSON: %w", id, err)
		}
		cfg.Envelope = json.RawMessage(p.Envelope)
	}
	for _, ch := range p.ConditionalHeaders {
		cfg.ConditionalHeaders = append(cfg.ConditionalHeaders, ConditionalHeader{
			When: ConditionalHeaderMatch{
				ModelIDPrefix:     ch.When.ModelIDPrefix,
				RequiresReasoning: ch.When.RequiresReasoning,
			},
			Set: ch.Set,
		})
	}
	for _, sp := range p.SystemInstructionPrefix {
		cfg.SystemInstructionPrefix = append(cfg.SystemInstructionPrefix, googleSysInstrPart{Text: sp.Text})
	}

	RegisterDeclGoogleConfig(id, cfg)
	ai.DefaultRegistry.RegisterApiProvider(&ai.ApiProvider{
		Api:          ai.Api(id),
		Stream:       StreamDeclGoogle,
		StreamSimple: StreamSimpleDeclGoogle,
	}, sourceID)
	return nil
}

func (declGoogleApiKind) Unregister(id string) {
	// Drop the per-api config; the ai.ApiProvider entry is torn down by
	// the bridge's source-keyed UnregisterApiProviders call.
	declGoogleConfigs.Delete(id)
}

func init() {
	apikind.Register("decl-google", declGoogleApiKind{})
}
