package extension

// bridgeScopedAPI wraps a shared BridgeAPI and routes set/get_session_data
// calls to the owning Bridge's per-extension store.  All other methods are
// forwarded unchanged via embedding.
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

var _ BridgeAPI = (*bridgeScopedAPI)(nil)
