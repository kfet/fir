package session

import (
	"context"
	"testing"
)

func TestWithCompactionProgress_RoundTrip(t *testing.T) {
	var gotPhase, gotDelta string
	fn := CompactionProgressFunc(func(phase, delta string) {
		gotPhase = phase
		gotDelta = delta
	})

	ctx := WithCompactionProgress(context.Background(), fn)

	extracted := CompactionProgressFromCtx(ctx)
	if extracted == nil {
		t.Fatal("expected progress function to be extractable from context")
	}
	extracted("test-phase", "test-delta")
	if gotPhase != "test-phase" {
		t.Errorf("expected phase %q, got %q", "test-phase", gotPhase)
	}
	if gotDelta != "test-delta" {
		t.Errorf("expected delta %q, got %q", "test-delta", gotDelta)
	}
}

func TestCompactionProgressFromCtx_NilOnEmptyContext(t *testing.T) {
	fn := CompactionProgressFromCtx(context.Background())
	if fn != nil {
		t.Error("expected nil progress function from empty context")
	}
}
