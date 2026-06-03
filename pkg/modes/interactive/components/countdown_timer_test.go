package components

import (
	"sync"
	"testing"
	"time"
)

func init() {
	// Speed up all countdown timer tests: 10ms ticks instead of 1s.
	countdownTickInterval = 10 * time.Millisecond
}

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
	ct := NewCountdownTimer(500, nil, func(s int) {}, func() {
		mu.Lock()
		defer mu.Unlock()
		expired = true
	})
	_ = ct

	// Poll for expiry instead of sleeping a fixed duration.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := expired
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected timer to expire")
		}
		time.Sleep(2 * time.Millisecond)
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

	// Poll until we have at least 2 ticks.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(ticks)
		mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			t.Fatalf("expected at least 2 ticks, got %d", len(ticks))
			mu.Unlock()
		}
		time.Sleep(2 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if ticks[0] != 2 {
		t.Errorf("expected first tick 2, got %d", ticks[0])
	}
}
