package components

import (
	"sync"
	"testing"
	"time"
)

func TestCountdownTimer_InitialTick(t *testing.T) {
	var mu sync.Mutex
	var ticks []int
	ct := NewCountdownTimer(3000, nil, func(s int) {
		mu.Lock()
		defer mu.Unlock()
		ticks = append(ticks, s)
	}, func() {})
	defer ct.Dispose()

	mu.Lock()
	// Initial tick should be 3 (ceil(3000/1000))
	if len(ticks) == 0 {
		mu.Unlock()
		t.Fatal("expected initial tick")
	}
	if ticks[0] != 3 {
		mu.Unlock()
		t.Errorf("expected initial tick of 3, got %d", ticks[0])
	}
	mu.Unlock()
}

func TestCountdownTimer_Dispose(t *testing.T) {
	ct := NewCountdownTimer(10000, nil, func(s int) {}, func() {})
	ct.Dispose()
	// Double dispose should not panic
	ct.Dispose()
}

func TestCountdownTimer_Expiry(t *testing.T) {
	var mu sync.Mutex
	expired := false
	ct := NewCountdownTimer(1500, nil, func(s int) {}, func() {
		mu.Lock()
		defer mu.Unlock()
		expired = true
	})
	_ = ct

	// Wait for expiry (1.5s timeout → ceil = 2 seconds until expire)
	time.Sleep(3 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if !expired {
		t.Error("expected timer to expire")
	}
}

func TestCountdownTimer_TickSequence(t *testing.T) {
	var mu sync.Mutex
	var ticks []int
	ct := NewCountdownTimer(2000, nil, func(s int) {
		mu.Lock()
		defer mu.Unlock()
		ticks = append(ticks, s)
	}, func() {})
	_ = ct

	// Wait for all ticks (2s timeout → initial tick of 2, then 1, then 0 + expire)
	time.Sleep(3 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(ticks) < 2 {
		t.Errorf("expected at least 2 ticks, got %d", len(ticks))
	}
	if ticks[0] != 2 {
		t.Errorf("expected first tick 2, got %d", ticks[0])
	}
}
