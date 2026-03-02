package components

import (
	"strings"
	"testing"
)

func TestBashExecutionComponent_Render(t *testing.T) {
	comp := NewBashExecutionComponent("echo hello", nil, false)
	defer comp.loader.Stop()

	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}

	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "$ echo hello") {
		t.Error("expected command header")
	}
}

func TestBashExecutionComponent_AppendOutput(t *testing.T) {
	comp := NewBashExecutionComponent("ls", nil, false)
	defer comp.loader.Stop()

	comp.AppendOutput("file1.txt\nfile2.txt")

	got := comp.GetOutput()
	if !strings.Contains(got, "file1.txt") || !strings.Contains(got, "file2.txt") {
		t.Errorf("output = %q, want file1.txt and file2.txt", got)
	}
}

func TestBashExecutionComponent_SetComplete(t *testing.T) {
	comp := NewBashExecutionComponent("false", nil, false)

	exitCode := 1
	comp.SetComplete(&exitCode, false, nil, "")

	lines := comp.Render(80)
	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "exit 1") {
		t.Error("expected exit code in output")
	}
}

func TestBashExecutionComponent_Cancelled(t *testing.T) {
	comp := NewBashExecutionComponent("sleep 100", nil, false)

	comp.SetComplete(nil, true, nil, "")

	lines := comp.Render(80)
	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "cancelled") {
		t.Error("expected cancelled status in output")
	}
}

func TestBashExecutionComponent_ExcludeFromContext(t *testing.T) {
	comp := NewBashExecutionComponent("echo test", nil, true)
	defer comp.loader.Stop()

	if comp.borderColorKey != "dim" {
		t.Errorf("expected dim border for excluded command, got %q", comp.borderColorKey)
	}
}

func TestBashExecutionComponent_GetCommand(t *testing.T) {
	comp := NewBashExecutionComponent("whoami", nil, false)
	defer comp.loader.Stop()

	if comp.GetCommand() != "whoami" {
		t.Errorf("GetCommand() = %q, want %q", comp.GetCommand(), "whoami")
	}
}

func TestBashExecutionComponent_PreserveAnsi(t *testing.T) {
	comp := NewBashExecutionComponent("ls", nil, false)
	defer comp.loader.Stop()

	comp.AppendOutput("\x1b[32mcolored\x1b[0m text")
	got := comp.GetOutput()
	if !strings.Contains(got, "\x1b[32m") {
		t.Errorf("output should preserve ANSI codes, got %q", got)
	}
	if !strings.Contains(got, "colored") {
		t.Errorf("output should contain text, got %q", got)
	}
}

func TestBashExecutionComponent_RenderPreservesAnsi(t *testing.T) {
	comp := NewBashExecutionComponent("ls --color", nil, false)

	// Simulate colored output from bash
	comp.AppendOutput("\x1b[32mgreen_file\x1b[0m\n\x1b[34mblue_dir\x1b[0m")

	// Mark complete so we get stable output (no loader)
	exitCode := 0
	comp.SetComplete(&exitCode, false, nil, "")

	lines := comp.Render(80)
	rendered := strings.Join(lines, "\n")

	// The rendered output should contain the ANSI escape codes
	if !strings.Contains(rendered, "\x1b[32m") {
		t.Errorf("rendered output should preserve green ANSI code, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "\x1b[34m") {
		t.Errorf("rendered output should preserve blue ANSI code, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "green_file") {
		t.Errorf("rendered output should contain green_file, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "blue_dir") {
		t.Errorf("rendered output should contain blue_dir, got:\n%s", rendered)
	}
}
