package extension

import "github.com/kfet/fir/pkg/ai"

// bridgeScopedAPI wraps a shared BridgeAPI and routes set/get_session_data
// calls to the owning Bridge's per-extension store.  All other methods are
// forwarded unchanged.
type bridgeScopedAPI struct {
	BridgeAPI
	b *Bridge
}

func (w *bridgeScopedAPI) SetSessionData(key, value string) {
	w.b.SetSessionData(key, value)
}

func (w *bridgeScopedAPI) GetSessionData(key string) (string, bool) {
	return w.b.GetSessionData(key)
}

// Ensure the remaining BridgeAPI methods are forwarded by embedding, and that
// compile-time interface satisfaction is checked.
var _ BridgeAPI = (*bridgeScopedAPI)(nil)

// ---------------------------------------------------------------------------
// Compile-time check: SessionBridge must implement the full BridgeAPI.
// (The two new methods are added in session_bridge.go.)
// ---------------------------------------------------------------------------

// noopSessionData is a convenience zero-value implementation used by the
// shared SessionBridge for methods that should never be reached via the
// un-wrapped api path (they are intercepted by bridgeScopedAPI first).
type noopSessionData struct{}

func (noopSessionData) SetSessionData(_, _ string)             {}
func (noopSessionData) GetSessionData(_ string) (string, bool) { return "", false }

// Ensure noopSessionData satisfies the two new interface methods.
var _ interface {
	SetSessionData(key, value string)
	GetSessionData(key string) (string, bool)
} = noopSessionData{}

// modelSetter is a small helper so bridge_api.go can forward SetModel without
// importing ai directly beyond what is already available via the interface.
var _ interface{ SetModel(*ai.Model) bool } = (*bridgeScopedAPI)(nil)
