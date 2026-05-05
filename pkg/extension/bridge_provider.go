package extension

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/models"
)

// extProviderRegistration tracks one hosted provider contributed by an
// extension at handshake. The synthetic Api `ext:<id>` is registered in
// ai.DefaultRegistry pointing at an extStreamAdapter so the agent's normal
// Stream(model.Api, ...) path transparently dispatches via JSON-RPC.
//
// When the spec opts in to Api passthrough (ProviderSpec.Api set to a
// built-in wire protocol), the bridge skips synthetic-Api allocation and
// stream-adapter registration — the host's in-process stream function for
// that Api handles streaming directly. Only `synthetic` differs in that
// case; metadata (RegisteredProvider record, models, lister) flows the same
// way for both modes.
type extProviderRegistration struct {
	spec      ProviderSpec
	api       ai.Api
	synthetic bool // true → we registered an ext:<id> Api that needs unregistering
}

// providerStreamEventParams is the wire shape of a provider.stream.event
// notification. The event field carries a fully-formed AssistantMessageEvent.
type providerStreamEventParams struct {
	StreamID string                   `json:"stream_id"`
	Event    ai.AssistantMessageEvent `json:"event"`
}

// extStreamTimeout bounds how long start/cancel/toolResult RPCs may wait.
// The actual streamed response is delivered asynchronously via
// provider.stream.event notifications and isn't bounded by this timeout.
const extStreamTimeout = 30 * time.Second

// providerExtSourceID derives the unregister-source key used in
// ai.DefaultRegistry for a given extension name. Builtin-scope extensions
// get a "builtin-ext:" prefix so IsBuiltInProviderID / IsBuiltInApi
// recognise them as core-equivalent — preventing a project-local
// extension from legitimately claiming an ID owned by a builtin
// extension.
func providerExtSourceID(extName string) string        { return "ext:" + extName }
func builtinProviderExtSourceID(extName string) string { return "builtin-ext:" + extName }

// providerSourceID returns the right source-ID prefix for this bridge
// based on extension scope. Safe to call when proc is nil (tests).
func (b *Bridge) providerSourceID() string {
	if b.proc != nil && b.proc.cfg.Scope == "builtin" {
		return builtinProviderExtSourceID(b.caps.Name)
	}
	return providerExtSourceID(b.caps.Name)
}

// providerSyntheticApi returns the synthetic Api key used to dispatch to an
// ext-shipped provider through ai.DefaultRegistry.
func providerSyntheticApi(providerID string) ai.Api { return ai.Api("ext:" + providerID) }

// newStreamID generates a unique stream identifier.
func newStreamID() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// modelFromSpec converts a ProviderModelSpec into an ai.Model. The Api is
// supplied by the caller — typically either the synthetic “ext:<id>“ (for
// extensions doing their own streaming) or a built-in wire protocol (for
// metadata-only ext-shipped providers riding on a host stream function).
func modelFromSpec(providerID string, api ai.Api, m ProviderModelSpec) *ai.Model {
	inputs := make([]ai.InputModality, 0, len(m.Input))
	for _, s := range m.Input {
		inputs = append(inputs, ai.InputModality(s))
	}
	if len(inputs) == 0 {
		inputs = []ai.InputModality{ai.InputText}
	}
	out := &ai.Model{
		ID:                    m.ID,
		Name:                  m.Name,
		Api:                   api,
		Provider:              providerID,
		BaseURL:               m.BaseURL,
		Reasoning:             m.Reasoning,
		Input:                 inputs,
		ContextWindow:         m.ContextWindow,
		MaxTokens:             m.MaxTokens,
		ServerTools:           m.ServerTools,
		Compaction:            m.Compaction,
		ReasoningEffortValues: m.ReasoningEffortValues,
		SWEScore:              m.SWEScore,
		SWEInferred:           m.SWEInferred,
		Cost: ai.ModelCost{
			Input:      m.CostInput,
			Output:     m.CostOutput,
			CacheRead:  m.CostCacheRead,
			CacheWrite: m.CostCacheWrite,
		},
	}
	if out.Name == "" {
		out.Name = m.ID
	}
	return out
}

// recordFromSpec converts a ProviderSpec into the ai.RegisteredProvider
// metadata record consumed by cross-cutting code (envkeys, UI, defaulting).
// source identifies the owning bridge; built-in extensions pass a
// "builtin-ext:<name>" source so IsBuiltInProviderID treats the ID as
// core-equivalent.
func recordFromSpec(source string, ps ProviderSpec) *ai.RegisteredProvider {
	return &ai.RegisteredProvider{
		ID:             ai.Provider(ps.ID),
		DisplayName:    ps.DisplayName,
		ShortName:      ps.ShortName,
		Priority:       ps.Priority,
		DefaultModelID: ps.DefaultModelID,
		KeyLink:        ps.KeyLink,
		EnvKeys: ai.EnvKeySpec{
			Primary:       ps.EnvKeys.Primary,
			Fallbacks:     ps.EnvKeys.Fallbacks,
			Authenticated: ps.EnvKeys.Authenticated,
		},
		OAuthProviderID:    ps.OAuthProviderID,
		ClaimsModelIDGlobs: ps.ClaimsModelIDGlobs,
		RefuseFuzzyMatch:   ps.RefuseFuzzyMatch,
		Source:             source,
	}
}

// RegisterProviders wires every ProviderSpec from this bridge's InitResult
// into the global ai/models registries. Call once per handshake.
func (b *Bridge) RegisterProviders() {
	b.providersMu.Lock()
	defer b.providersMu.Unlock()

	srcID := b.providerSourceID()
	for _, ps := range b.caps.Providers {
		// Decide dispatch mode: passthrough to a built-in wire protocol
		// (when ps.Api is set) vs. full Python streaming (synthetic Api).
		var (
			api       ai.Api
			synthetic bool
		)
		if ps.Api != "" {
			api = ai.Api(ps.Api)
			synthetic = false
		} else {
			api = providerSyntheticApi(ps.ID)
			synthetic = true

			// Stream adapter: bound to (bridge, providerID).
			adapter := newExtStreamAdapter(b, ps.ID)
			// SimpleStream embeds StreamOptions, so we can downgrade
			// to the Stream path. Reasoning/thinking-budget fields are
			// already serialised into the wire model+context so the
			// extension sees them through the provider/stream/start RPC.
			simpleAdapter := func(ctx context.Context, model *ai.Model, prompt ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
				var streamOpts *ai.StreamOptions
				if opts != nil {
					so := opts.StreamOptions
					streamOpts = &so
				}
				return adapter(ctx, model, prompt, streamOpts)
			}
			ai.DefaultRegistry.RegisterApiProvider(&ai.ApiProvider{
				Api:          api,
				Stream:       adapter,
				StreamSimple: simpleAdapter,
			}, srcID)
		}

		// Provider metadata record.
		ai.RegisterProvider(recordFromSpec(srcID, ps))

		// Static model catalogue (any models the spec ships explicitly).
		// Metadata-only ext providers may omit Models entirely and rely
		// on the existing models_generated.go catalogue.
		for _, m := range ps.Models {
			ai.RegisterModel(modelFromSpec(ps.ID, api, m))
		}

		// Optional live-listing.
		if ps.SupportsLiveList {
			models.RegisterModelLister(ps.ID, &extModelLister{bridge: b, providerID: ps.ID})
		}

		b.providers = append(b.providers, &extProviderRegistration{
			spec: ps, api: api, synthetic: synthetic,
		})
	}
}

// UnregisterProviders rolls back every provider registration this bridge
// made at handshake. Idempotent.
func (b *Bridge) UnregisterProviders() {
	b.providersMu.Lock()
	defer b.providersMu.Unlock()

	srcID := b.providerSourceID()
	// Only synthetic ``ext:<id>`` Api providers are owned by this bridge —
	// passthrough specs ride on a built-in Api whose ApiProvider lives in
	// core and must NOT be torn down here. UnregisterApiProviders is
	// keyed by source so it only touches our synthetic registrations
	// (which were added with this bridge's source id).
	ai.DefaultRegistry.UnregisterApiProviders(srcID)
	for _, r := range b.providers {
		ai.UnregisterProvider(ai.Provider(r.spec.ID))
		ai.UnregisterProviderModels(ai.Provider(r.spec.ID))
		if r.spec.SupportsLiveList {
			models.UnregisterModelLister(r.spec.ID)
		}
	}
	b.providers = nil

	// Cancel any in-flight streams owned by this bridge — there's nobody
	// left to deliver events. Push an error and close.
	b.activeStreams.Range(func(k, v any) bool {
		s := v.(*ai.AssistantMessageEventStream)
		s.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError})
		s.End(nil)
		b.activeStreams.Delete(k)
		return true
	})
}

// newExtStreamAdapter returns an ai.StreamFunction that forwards a streaming
// completion request to this bridge's extension via JSON-RPC.
func newExtStreamAdapter(b *Bridge, providerID string) ai.StreamFunction {
	return func(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
		out := ai.NewAssistantMessageEventStream()
		streamID := newStreamID()
		b.activeStreams.Store(streamID, out)

		// Marshal model+prompt+options as opaque JSON — the extension is free
		// to interpret only the bits it cares about.
		params := map[string]any{
			"provider_id": providerID,
			"stream_id":   streamID,
			"model":       model,
			"prompt":      prompt,
			"options":     options,
		}

		go func() {
			defer b.activeStreams.Delete(streamID)
			done := make(chan struct{})
			defer close(done)

			// On caller ctx cancellation, fire a best-effort cancel RPC.
			go func() {
				select {
				case <-ctx.Done():
					_, _ = b.CallHook(context.Background(),
						"provider/stream/cancel",
						map[string]string{"stream_id": streamID},
						extStreamTimeout)
				case <-done:
				}
			}()

			if _, err := b.CallHook(ctx, "provider/stream/start", params, extStreamTimeout); err != nil {
				out.Push(ai.AssistantMessageEvent{
					Type:   ai.EventError,
					Reason: ai.StopReasonError,
					Error: &ai.AssistantMessage{
						Role:         ai.RoleAssistant,
						Model:        model.ID,
						Api:          model.Api,
						Provider:     model.Provider,
						StopReason:   ai.StopReasonError,
						ErrorMessage: fmt.Sprintf("ext provider %q: %v", providerID, err),
					},
				})
				out.End(nil)
				return
			}
			// Wait for a terminal event on `out`. Result() blocks until a
			// done/error event is pushed by handleProviderStreamEvent.
			_ = out.Result()
			out.End(nil)
		}()
		return out
	}
}

// handleProviderStreamEvent dispatches an inbound provider.stream.event
// notification to the destination stream registered under StreamID.
func (b *Bridge) handleProviderStreamEvent(raw *json.RawMessage) {
	if raw == nil {
		return
	}
	var p providerStreamEventParams
	if err := json.Unmarshal(*raw, &p); err != nil {
		return
	}
	v, ok := b.activeStreams.Load(p.StreamID)
	if !ok {
		return
	}
	stream := v.(*ai.AssistantMessageEventStream)
	stream.Push(p.Event)
}

// extModelLister adapts an ext-shipped provider's listModels capability to
// the models.ModelLister interface used by --list-models / live model
// resolution.
type extModelLister struct {
	bridge     *Bridge
	providerID string
}

// ListModels asks the extension for a fresh list of model IDs. baseURL and
// apiKey are passed through verbatim — extensions decide whether they're
// relevant.
func (l *extModelLister) ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	params := map[string]any{
		"provider_id": l.providerID,
		"base_url":    baseURL,
		"api_key":     apiKey,
	}
	raw, err := l.bridge.CallHook(ctx, "provider/listModels", params, extStreamTimeout)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ModelIDs []string `json:"model_ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("provider/listModels: invalid response: %w", err)
	}
	return resp.ModelIDs, nil
}
