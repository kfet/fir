package log_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	firlog "github.com/kfet/fir/pkg/log"
)

func TestLog_Disabled(t *testing.T) {
	firlog.Disable()
	defer firlog.Disable()
	// Should not panic or produce output when disabled
	firlog.Log("this should be a no-op: %d", 42)
}

func TestLog_Enabled(t *testing.T) {
	firlog.Enable()
	defer firlog.Disable()

	// Capture stderr
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	firlog.Log("test message: %s", "hello")

	w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "[DEBUG") {
		t.Errorf("expected [DEBUG prefix, got: %s", output)
	}
	if !strings.Contains(output, "test message: hello") {
		t.Errorf("expected message content, got: %s", output)
	}
}

func TestEnabled(t *testing.T) {
	firlog.Disable()
	if firlog.Enabled() {
		t.Error("expected disabled")
	}
	firlog.Enable()
	if !firlog.Enabled() {
		t.Error("expected enabled")
	}
	firlog.Disable()
}
