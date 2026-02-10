// Ported from: packages/coding-agent/src/core/event-bus.ts
// Upstream hash: 1caadb2e
package core

import (
	"sync"
	"testing"
)

func TestEventBus_EmitAndReceive(t *testing.T) {
	bus := NewEventBus()
	var received any
	bus.On("test", func(data any) {
		received = data
	})
	bus.Emit("test", "hello")
	if received != "hello" {
		t.Errorf("expected 'hello', got %v", received)
	}
}

func TestEventBus_MultipleHandlers(t *testing.T) {
	bus := NewEventBus()
	var count int
	bus.On("test", func(data any) { count++ })
	bus.On("test", func(data any) { count++ })
	bus.Emit("test", nil)
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := NewEventBus()
	var count int
	unsub := bus.On("test", func(data any) { count++ })
	bus.Emit("test", nil)
	if count != 1 {
		t.Fatalf("expected 1 after first emit, got %d", count)
	}
	unsub()
	bus.Emit("test", nil)
	if count != 1 {
		t.Errorf("expected still 1 after unsub, got %d", count)
	}
}

func TestEventBus_DifferentChannels(t *testing.T) {
	bus := NewEventBus()
	var ch1, ch2 int
	bus.On("ch1", func(data any) { ch1++ })
	bus.On("ch2", func(data any) { ch2++ })
	bus.Emit("ch1", nil)
	if ch1 != 1 || ch2 != 0 {
		t.Errorf("expected ch1=1, ch2=0, got ch1=%d, ch2=%d", ch1, ch2)
	}
}

func TestEventBus_Clear(t *testing.T) {
	bus := NewEventBus()
	var count int
	bus.On("test", func(data any) { count++ })
	bus.Clear()
	bus.Emit("test", nil)
	if count != 0 {
		t.Errorf("expected 0 after clear, got %d", count)
	}
}

func TestEventBus_HandlerPanicRecovery(t *testing.T) {
	bus := NewEventBus()
	var second bool
	bus.On("test", func(data any) {
		panic("handler panic")
	})
	bus.On("test", func(data any) {
		second = true
	})
	// Should not panic; second handler should still run
	bus.Emit("test", nil)
	if !second {
		t.Error("expected second handler to run after first panicked")
	}
}

func TestEventBus_EmitNoHandlers(t *testing.T) {
	bus := NewEventBus()
	// Should not panic
	bus.Emit("nonexistent", "data")
}

func TestEventBus_ConcurrentAccess(t *testing.T) {
	bus := NewEventBus()
	var mu sync.Mutex
	count := 0

	for i := 0; i < 10; i++ {
		bus.On("test", func(data any) {
			mu.Lock()
			count++
			mu.Unlock()
		})
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Emit("test", nil)
		}()
	}
	wg.Wait()

	if count != 1000 {
		t.Errorf("expected 1000, got %d", count)
	}
}

func TestEventBus_DataPassthrough(t *testing.T) {
	bus := NewEventBus()
	type Payload struct {
		Name string
		Val  int
	}
	var received Payload
	bus.On("test", func(data any) {
		if p, ok := data.(Payload); ok {
			received = p
		}
	})
	bus.Emit("test", Payload{Name: "test", Val: 42})
	if received.Name != "test" || received.Val != 42 {
		t.Errorf("unexpected payload: %+v", received)
	}
}
