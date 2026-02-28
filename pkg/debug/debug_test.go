package debug

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestLog_Disabled(t *testing.T) {
	Disable()
	defer Disable()
	// Should not panic or produce output when disabled
	Log("this should be a no-op: %d", 42)
}

func TestLog_Enabled(t *testing.T) {
	Enable()
	defer Disable()

	// Capture stderr
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	Log("test message: %s", "hello")

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
	Disable()
	if Enabled() {
		t.Error("expected disabled")
	}
	Enable()
	if !Enabled() {
		t.Error("expected enabled")
	}
	Disable()
}
