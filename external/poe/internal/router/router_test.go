package router

import (
	"sync"
	"testing"
	"time"
)

func TestRouter_RegisterPushReceive(t *testing.T) {
	r := New()
	ch := r.Register("m-1")
	if r.Len() != 1 {
		t.Errorf("Len: got %d, want 1", r.Len())
	}
	if err := r.Push("m-1", Chunk{Text: "hello", Final: false}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	select {
	case c := <-ch:
		if c.Text != "hello" || c.Final {
			t.Errorf("chunk: got %+v", c)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("no chunk received")
	}
}

func TestRouter_PushUnknown(t *testing.T) {
	r := New()
	if err := r.Push("m-nope", Chunk{Text: "x"}); err != ErrUnknownMessage {
		t.Errorf("err: got %v, want ErrUnknownMessage", err)
	}
}

func TestRouter_Unregister(t *testing.T) {
	r := New()
	_ = r.Register("m-2")
	r.Unregister("m-2")
	if r.Len() != 0 {
		t.Errorf("Len after unregister: got %d, want 0", r.Len())
	}
	// Pushing to an unregistered id returns ErrUnknownMessage.
	if err := r.Push("m-2", Chunk{}); err != ErrUnknownMessage {
		t.Errorf("post-unregister push: got %v, want ErrUnknownMessage", err)
	}
}

func TestRouter_UnregisterNoop(t *testing.T) {
	r := New()
	r.Unregister("never-registered") // must not panic
}

func TestRouter_RegisterTwicePanics(t *testing.T) {
	r := New()
	r.Register("m-3")
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate Register")
		}
	}()
	r.Register("m-3")
}

func TestRouter_PushAfterCloseReturnsErrClosed(t *testing.T) {
	// Race Unregister and Push: set up the channel, start Push but simulate
	// the race by closing between the map lookup and the send. We exercise
	// the recover path by closing the channel manually via a racing
	// goroutine and blocking Push on an unbuffered-enough channel.
	//
	// Simplest reliable reproduction: fill the buffer, then Unregister,
	// then Push (which will block then hit a closed channel via recover).
	r := New()
	ch := r.Register("m-4")

	// Fill the buffer (capacity 16 — implementation detail of Register).
	for i := 0; i < cap(ch); i++ {
		if err := r.Push("m-4", Chunk{Text: "fill"}); err != nil {
			t.Fatalf("fill push %d: %v", i, err)
		}
	}

	// Now a Push in a goroutine that will block; concurrently Unregister
	// to close the channel out from under it.
	var wg sync.WaitGroup
	var pushErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		pushErr = r.Push("m-4", Chunk{Text: "blocked"})
	}()

	// Give the goroutine a moment to reach the blocking send.
	time.Sleep(50 * time.Millisecond)
	r.Unregister("m-4")

	wg.Wait()
	if pushErr != ErrClosed {
		t.Errorf("pushErr: got %v, want ErrClosed", pushErr)
	}
}

func TestRouter_ConcurrentPushes(t *testing.T) {
	r := New()
	ch := r.Register("m-concurrent")
	defer r.Unregister("m-concurrent")

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_ = r.Push("m-concurrent", Chunk{Text: "x"})
		}(i)
	}

	// Drain concurrently.
	received := 0
	done := make(chan struct{})
	go func() {
		for range ch {
			received++
			if received == N {
				close(done)
				return
			}
		}
	}()

	wg.Wait()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("received only %d of %d", received, N)
	}
}
