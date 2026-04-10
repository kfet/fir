// Package router brokers in-flight Poe query requests to the fir MCP side.
//
// When a `/poe` POST arrives, the HTTP handler calls Register to allocate a
// per-message channel keyed by the Poe message_id. It then emits an MCP
// channel notification to fir and loops draining chunks from the channel
// into SSE text events. fir produces chunks by calling the `reply` MCP tool,
// whose handler calls Push on the router. A chunk with Final=true closes the
// exchange.
//
// The router is concurrency-safe: Register/Push/Unregister may be called
// from any goroutine. Push to an unknown (or already-unregistered) message
// returns ErrUnknownMessage so callers can surface the mismatch rather
// than blocking.
package router

import (
	"errors"
	"sync"
)

// Chunk is a single unit of reply text from fir.
type Chunk struct {
	Text  string
	Final bool
}

// ErrUnknownMessage is returned by Push when no handler is waiting for
// chunks on the given message_id.
var ErrUnknownMessage = errors.New("router: unknown message_id")

// ErrClosed is returned by Push when the message's channel has been
// unregistered (for example because the SSE handler already returned).
var ErrClosed = errors.New("router: message channel closed")

// entry is the internal per-message state held in Router.pending. `ch`
// carries chunks from Push to the SSE-side receiver; `done` is closed by
// Unregister to unblock any pending Push that has filled the channel
// buffer, without ever closing `ch` itself (which would race with Push).
type entry struct {
	ch   chan Chunk
	done chan struct{}
}

// Router brokers chunks between the reply MCP tool and the SSE handler.
// The zero value is not ready for use; call New.
type Router struct {
	mu      sync.Mutex
	pending map[string]*entry
}

// New returns a ready-to-use Router.
func New() *Router {
	return &Router{pending: make(map[string]*entry)}
}

// Register allocates a chunk channel for the given message_id. The returned
// channel is buffered (capacity 16) so a burst of quick chunks from fir
// doesn't block the reply tool handler while the SSE writer is flushing.
// Register panics if a channel already exists for the id — callers are
// responsible for ensuring message_ids are unique.
//
// The returned channel is never closed; the SSE-side receiver should
// select on it together with its request context, and call Unregister
// when it's done. A pending Push that is blocked on a full buffer will
// be unblocked (and returned ErrClosed) by Unregister.
func (r *Router) Register(msgID string) <-chan Chunk {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.pending[msgID]; exists {
		panic("router: Register called twice for message_id " + msgID)
	}
	e := &entry{
		ch:   make(chan Chunk, 16),
		done: make(chan struct{}),
	}
	r.pending[msgID] = e
	return e.ch
}

// Unregister removes the entry for msgID and unblocks any in-flight Push
// by closing the entry's done channel. The chunk channel itself is NOT
// closed — doing so would race with a concurrent Push. Safe to call on
// an id that was never registered (no-op).
func (r *Router) Unregister(msgID string) {
	r.mu.Lock()
	e, ok := r.pending[msgID]
	if ok {
		delete(r.pending, msgID)
	}
	r.mu.Unlock()
	if ok {
		close(e.done)
	}
}

// Push sends a chunk to the handler waiting on the given message_id.
// Returns ErrUnknownMessage if no entry exists, or ErrClosed if the entry
// was unregistered before the chunk could be delivered.
func (r *Router) Push(msgID string, c Chunk) error {
	r.mu.Lock()
	e, ok := r.pending[msgID]
	r.mu.Unlock()
	if !ok {
		return ErrUnknownMessage
	}
	select {
	case e.ch <- c:
		return nil
	case <-e.done:
		return ErrClosed
	}
}

// Len returns the number of currently registered message_ids. Intended
// for tests and diagnostics.
func (r *Router) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}
