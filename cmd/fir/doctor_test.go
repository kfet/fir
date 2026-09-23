package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/models"
)

func TestDoctorClientVersionGates(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runDoctorClientVersionGates(&out, dir); err != nil || out.Len() != 0 {
		t.Fatalf("no log must print nothing: %q %v", out.String(), err)
	}
	eff := models.LocalClientVersion(dir, models.ClientVersionKeyClaudeCode)
	for _, pin := range []string{"0.0.1", eff} { // resolved, unresolved
		if err := models.AppendGateRecord(models.DoctorLogPath(dir), models.GateRecord{Key: models.ClientVersionKeyClaudeCode, Pin: pin, Required: "999.0", Host: "h1", Model: "m", Timestamp: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runDoctorClientVersionGates(&out, dir); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "pin="+eff) || !strings.Contains(lines[0], "hosts=h1") {
		t.Fatalf("output: %q", out.String())
	}
	// Unreadable log is an error, not silence.
	if err := os.Remove(models.DoctorLogPath(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(models.DoctorLogPath(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runDoctorClientVersionGates(&out, dir); err == nil {
		t.Fatal("want error")
	}
}

func TestRunDoctorUsage(t *testing.T) {
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = []string{"fir", "doctor", "bogus"}
	if err := runDoctor(); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("got %v", err)
	}
	t.Setenv("FIR_AGENT_DIR", t.TempDir())
	os.Args = []string{"fir", "doctor", "client-version-gates"}
	if err := runDoctor(); err != nil {
		t.Fatal(err)
	}
}
