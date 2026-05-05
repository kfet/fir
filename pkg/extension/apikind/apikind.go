// Package apikind defines a tiny shared seam between pkg/extension (which
// receives ApiSpec wire payloads at handshake) and the wire-protocol
// implementation packages (pkg/ai/providers etc.) that handle them. Both
// sides depend on this package, neither depends on the other for this
// purpose — avoiding an import cycle.
package apikind

import (
	"encoding/json"
	"sync"
)

// Handler dispatches ApiSpec registration based on ApiSpec.Kind.
// Implementations live alongside the wire-protocol code they support
// (e.g. pkg/ai/providers registers a "decl-google" handler that builds a
// DeclGoogleConfig from the spec's payload).
type Handler interface {
	// Register binds api id → spec for this kind. sourceID identifies the
	// owning extension; the handler MUST register its ai.ApiProvider with
	// this sourceID so the bridge can later tear it down via
	// source-keyed UnregisterApiProviders.
	Register(id string, payload json.RawMessage, sourceID string) error
	// Unregister rolls back any state outside ai.DefaultRegistry that
	// Register installed (e.g. a per-Api config map). The ai.Registry
	// entry itself is removed by the bridge — handlers don't touch it.
	Unregister(id string)
}

var (
	mu       sync.RWMutex
	handlers = map[string]Handler{}
)

// Register installs h for the given kind. Called from init() of provider
// packages that ship a wire-protocol family. Re-registering the same kind
// replaces the previous handler.
func Register(kind string, h Handler) {
	mu.Lock()
	defer mu.Unlock()
	handlers[kind] = h
}

// Get returns the handler for kind, or nil.
func Get(kind string) Handler {
	mu.RLock()
	defer mu.RUnlock()
	return handlers[kind]
}
