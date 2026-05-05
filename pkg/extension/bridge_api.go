package extension

import (
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/extension/apikind"
	firlog "github.com/kfet/fir/pkg/log"
)

// extApiRegistration tracks one Api spec contributed by this bridge so
// UnregisterApis can call back into the kind handler.
type extApiRegistration struct {
	id   string
	kind string
}

// extApiSourceID derives the unregister-source key for ai.DefaultRegistry
// entries owned by this bridge's Api specs. Distinct from the synthetic
// provider source (providerExtSourceID) so Provider teardown doesn't
// accidentally tear down ext-shipped Apis (and vice versa). Builtin-scope
// extensions get a "builtin-" prefix so IsBuiltInApi recognises them as
// core-equivalent.
func extApiSourceID(extName string) string        { return "ext-api:" + extName }
func builtinExtApiSourceID(extName string) string { return "builtin-ext-api:" + extName }

func (b *Bridge) apiSourceID() string {
	if b.proc != nil && b.proc.cfg.Scope == "builtin" {
		return builtinExtApiSourceID(b.caps.Name)
	}
	return extApiSourceID(b.caps.Name)
}

// RegisterApis wires every ApiSpec from this bridge's InitResult into the
// global ai/providers registries by dispatching to the matching
// apikind.Handler. Idempotent enough to call once per handshake.
func (b *Bridge) RegisterApis() {
	b.providersMu.Lock()
	defer b.providersMu.Unlock()

	srcID := b.apiSourceID()
	for _, as := range b.caps.Apis {
		h := apikind.Get(as.Kind)
		if h == nil {
			firlog.Warn("ext api kind has no handler — spec ignored",
				"ext", b.caps.Name, "id", as.ID, "kind", as.Kind)
			continue
		}
		if err := h.Register(as.ID, as.Payload, srcID); err != nil {
			firlog.Warn("ext api register failed",
				"ext", b.caps.Name, "id", as.ID, "kind", as.Kind, "err", err)
			continue
		}
		firlog.Info("ext api registered",
			"ext", b.caps.Name, "id", as.ID, "kind", as.Kind, "src", srcID)
		b.apis = append(b.apis, &extApiRegistration{id: as.ID, kind: as.Kind})
	}
}

// UnregisterApis rolls back every Api spec this bridge registered.
// Tears down the kind handlers' out-of-registry state (e.g. per-Api
// config maps) and finally removes every ai.ApiProvider entry whose
// sourceID matches this bridge's Api source.
func (b *Bridge) UnregisterApis() {
	b.providersMu.Lock()
	defer b.providersMu.Unlock()

	for _, r := range b.apis {
		if h := apikind.Get(r.kind); h != nil {
			h.Unregister(r.id)
		}
	}
	ai.DefaultRegistry.UnregisterApiProviders(b.apiSourceID())
	b.apis = nil
}
