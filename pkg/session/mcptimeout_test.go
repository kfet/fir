package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
)

// blockingTool returns an AgentTool whose Execute blocks until either its
// context is cancelled (returning ctx.Err()) or the release channel is closed
// (returning an "ok" success result). It records the context it was called
// with so tests can inspect the deadline / cancellation cause.
func blockingTool(name string, release <-chan struct{}, gotCtx *context.Context) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{Name: name},
		Execute: func(ctx context.Context, _ string, _ map[string]any, _ agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			if gotCtx != nil {
				*gotCtx = ctx
			}
			select {
			case <-ctx.Done():
				return agent.AgentToolResult{}, ctx.Err()
			case <-release:
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "ok"}},
				}, nil
			}
		},
	}
}

// TestWrapMCPToolTimeout_EnforcesAndCancels proves that on the model-dispatch
// path the wrapper (a) bounds the call by N with a real deadline propagated
// into the tool, (b) genuinely cancels the tool (DeadlineExceeded), and (c)
// returns a clean, model-actionable timeout result rather than a raw ctx error.
func TestWrapMCPToolTimeout_EnforcesAndCancels(t *testing.T) {
	var innerCtx context.Context
	// release never fires: the only way out is the deadline.
	tool := wrapMCPToolTimeout(blockingTool("mcp__srv__slow", make(chan struct{}), &innerCtx), 30*time.Millisecond)

	start := time.Now()
	res, err := tool.Execute(context.Background(), "call-1", nil, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected clean result, got err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError result on timeout, got %+v", res)
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "timed out") {
		t.Fatalf("expected a clean timeout message, got %+v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "mcp__srv__slow") {
		t.Fatalf("timeout message should name the tool, got %q", res.Content[0].Text)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("wrapper did not bound the call: took %s", elapsed)
	}
	// True cancellation: the tool's own ctx was deadline-cancelled (child of
	// the turn ctx), not merely abandoned.
	if innerCtx == nil {
		t.Fatal("tool was never invoked")
	}
	if !errors.Is(innerCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("tool ctx should be DeadlineExceeded, got %v", innerCtx.Err())
	}
	if _, ok := innerCtx.Deadline(); !ok {
		t.Fatal("tool ctx should carry a deadline on the model-dispatch path")
	}
}

// TestWrapMCPToolTimeout_InternalCallBypasses proves the extension/pipe path is
// not clipped: a call stamped internal (as SessionBridge.CallTool does) runs
// under NO added deadline, so a timeout=-1 pipe/wait step whose body is a slow
// MCP tool still outruns N.
func TestWrapMCPToolTimeout_InternalCallBypasses(t *testing.T) {
	var innerCtx context.Context
	release := make(chan struct{})
	tool := wrapMCPToolTimeout(blockingTool("mcp__srv__slow", release, &innerCtx), 1*time.Millisecond)

	// N is 1ms; if it applied, the call would time out immediately. Because the
	// ctx is stamped internal, no deadline is added, so the tool runs until we
	// release it.
	ctx := WithInternalToolCall(context.Background())
	done := make(chan agent.AgentToolResult, 1)
	go func() {
		res, _ := tool.Execute(ctx, "ext-call-x", nil, nil)
		done <- res
	}()

	// Give the goroutine a chance to enter Execute and observe: no deadline.
	// We release explicitly; the result must be the success value, not a
	// timeout — proving N was bypassed.
	close(release)
	res := <-done
	if res.IsError {
		t.Fatalf("internal call must not be clipped by N, got error result: %+v", res)
	}
	if len(res.Content) == 0 || res.Content[0].Text != "ok" {
		t.Fatalf("expected success passthrough, got %+v", res.Content)
	}
	if innerCtx == nil {
		t.Fatal("tool was never invoked")
	}
	if _, ok := innerCtx.Deadline(); ok {
		t.Fatal("internal call must NOT carry an added deadline")
	}
}

// TestWrapMCPToolTimeout_UserAbortNotReportedAsTimeout proves a parent (turn)
// cancellation — e.g. ESC — is propagated as-is and never fabricated into a
// model-visible "timed out" tool result.
func TestWrapMCPToolTimeout_UserAbortNotReportedAsTimeout(t *testing.T) {
	tool := wrapMCPToolTimeout(blockingTool("mcp__srv__slow", make(chan struct{}), nil), 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		res agent.AgentToolResult
		err error
	}, 1)
	go func() {
		res, err := tool.Execute(ctx, "call-1", nil, nil)
		done <- struct {
			res agent.AgentToolResult
			err error
		}{res, err}
	}()
	cancel()
	got := <-done
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("expected propagated context.Canceled, got err=%v res=%+v", got.err, got.res)
	}
	if got.res.IsError && len(got.res.Content) > 0 && strings.Contains(got.res.Content[0].Text, "timed out") {
		t.Fatalf("user abort must not be reported as a timeout: %+v", got.res)
	}
}

// TestWrapMCPToolTimeout_DisabledIsNoop proves timeout<=0 leaves the tool
// unwrapped (no deadline, no interference).
func TestWrapMCPToolTimeout_DisabledIsNoop(t *testing.T) {
	var innerCtx context.Context
	release := make(chan struct{})
	close(release)
	tool := wrapMCPToolTimeout(blockingTool("mcp__srv__x", release, &innerCtx), 0)

	res, err := tool.Execute(context.Background(), "call-1", nil, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if _, ok := innerCtx.Deadline(); ok {
		t.Fatal("disabled wrapper must not add a deadline")
	}
}
