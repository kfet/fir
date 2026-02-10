// Ported from: packages/coding-agent/src/core/event-bus.ts
// Upstream hash: 1caadb2e
package core

import "sync"

// EventHandler is a function that handles an event.
type EventHandler func(data any)

// EventBus provides pub/sub messaging between components.
type EventBus interface {
	Emit(channel string, data any)
	On(channel string, handler EventHandler) func()
}

// EventBusController extends EventBus with lifecycle management.
type EventBusController interface {
	EventBus
	Clear()
}

type eventBus struct {
	mu       sync.RWMutex
	handlers map[string][]handlerEntry
	nextID   int
}

type handlerEntry struct {
	id      int
	handler EventHandler
}

// NewEventBus creates a new EventBusController.
func NewEventBus() EventBusController {
	return &eventBus{
		handlers: make(map[string][]handlerEntry),
	}
}

// Emit sends data to all handlers registered for the channel.
func (b *eventBus) Emit(channel string, data any) {
	b.mu.RLock()
	entries := b.handlers[channel]
	// Copy to release lock before calling handlers
	snapshot := make([]handlerEntry, len(entries))
	copy(snapshot, entries)
	b.mu.RUnlock()

	for _, entry := range snapshot {
		func() {
			defer func() {
				recover() // Swallow handler panics like TS version
			}()
			entry.handler(data)
		}()
	}
}

// On registers a handler for the channel and returns an unsubscribe function.
func (b *eventBus) On(channel string, handler EventHandler) func() {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.handlers[channel] = append(b.handlers[channel], handlerEntry{id: id, handler: handler})
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		entries := b.handlers[channel]
		for i, e := range entries {
			if e.id == id {
				b.handlers[channel] = append(entries[:i], entries[i+1:]...)
				return
			}
		}
	}
}

// Clear removes all registered handlers.
func (b *eventBus) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = make(map[string][]handlerEntry)
}
