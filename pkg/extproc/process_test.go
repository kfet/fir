package extproc

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcess_StartStop(t *testing.T) {
	script := writeTestScript(t, "#!/bin/sh\ncat\n")

	proc := NewProcess(
		ExtProcConfig{Name: "test", Path: script, Scope: "project"},
		nil, slog.Default(),
	)

	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	if proc.GetCodec() == nil {
		t.Fatal("codec is nil after start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := proc.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestProcess_Wait(t *testing.T) {
	script := writeTestScript(t, "#!/bin/sh\nexit 0\n")

	proc := NewProcess(
		ExtProcConfig{Name: "test", Path: script, Scope: "project"},
		nil, slog.Default(),
	)
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestProcess_RestartBackoff(t *testing.T) {
	// Script that always fails immediately.
	script := writeTestScript(t, "#!/bin/sh\nexit 1\n")

	proc := NewProcess(
		ExtProcConfig{Name: "fail", Path: script, Scope: "project"},
		nil, slog.Default(),
	)
	// Start succeeds (process starts then exits).
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	proc.Wait()

	// Restart should succeed (process starts, even though it exits quickly).
	// The backoff resets on successful start.
	for i := 0; i < 3; i++ {
		if err := proc.Restart(); err != nil {
			t.Fatalf("restart %d: %v", i, err)
		}
		proc.Wait()
	}
}

func TestProcess_RestartFailure(t *testing.T) {
	// Use a non-existent path so Start itself fails.
	proc := NewProcess(
		ExtProcConfig{Name: "bad", Path: "/nonexistent/binary", Scope: "project"},
		nil, slog.Default(),
	)
	proc.backoff = 1 * time.Millisecond // speed up test

	var err error
	for i := 0; i < maxRestarts+1; i++ {
		err = proc.Restart()
		if err == ErrTooManyFailures {
			break
		}
	}
	if err != ErrTooManyFailures {
		t.Fatalf("expected ErrTooManyFailures, got %v", err)
	}
}

func TestProcess_StopWaitCleanup(t *testing.T) {
	// Verify that after Stop, Wait returns promptly (process dead, pipes closed,
	// goroutines unblocked).
	script := writeTestScript(t, "#!/bin/sh\ncat\n") // blocks on stdin

	proc := NewProcess(
		ExtProcConfig{Name: "cleanup", Path: script, Scope: "project"},
		nil, slog.Default(),
	)
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := proc.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	// Wait should return immediately since Stop waited for the process.
	done := make(chan struct{})
	go func() {
		proc.Wait()
		close(done)
	}()
	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after Stop")
	}
}

func TestProcess_Codec_RoundTrip(t *testing.T) {
	// Script that echoes back a JSON-RPC response for any request.
	script := writeTestScript(t, `#!/bin/sh
read line
echo '{"jsonrpc":"2.0","id":1,"result":{"name":"echo-ext","tools":[],"events":[]}}'
cat
`)

	proc := NewProcess(
		ExtProcConfig{Name: "echo", Path: script, Scope: "project"},
		nil, slog.Default(),
	)
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	defer proc.Stop(context.Background())

	codec := proc.GetCodec()
	if err := codec.WriteRequest(1, "init", map[string]string{"version": "1"}); err != nil {
		t.Fatal(err)
	}

	msg, err := codec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := msg.(*Response)
	if !ok {
		t.Fatalf("expected *Response, got %T", msg)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func writeTestScript(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sh")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
