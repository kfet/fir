package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kfet/tau/pkg/ai/providers"
	"github.com/kfet/tau/pkg/core"
)

// ============================================================================
// createSessionManager
// ============================================================================

func TestCreateSessionManager_Default(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	args := &Args{}
	sm := createSessionManager(args, cwd, agentDir)

	if !sm.IsPersisted() {
		t.Error("default session manager should be persisted")
	}
}

func TestCreateSessionManager_NoSession(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	args := &Args{NoSession: true}
	sm := createSessionManager(args, cwd, agentDir)

	if sm.IsPersisted() {
		t.Error("--no-session should create non-persisted session manager")
	}
}

func TestCreateSessionManager_NamedSession(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	sessionDir := filepath.Join(agentDir, "sessions")
	os.MkdirAll(sessionDir, 0o755)

	args := &Args{Session: "my-session.jsonl"}
	sm := createSessionManager(args, cwd, agentDir)

	if !sm.IsPersisted() {
		t.Error("named session should be persisted")
	}
	sessionFile := sm.GetSessionFile()
	if sessionFile == "" {
		t.Error("expected session file to be set for named session")
	}
}

func TestCreateSessionManager_CustomSessionDir(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	customDir := t.TempDir()

	args := &Args{SessionDir: customDir, Session: "test.jsonl"}
	sm := createSessionManager(args, cwd, agentDir)

	if !sm.IsPersisted() {
		t.Error("custom session dir should still persist")
	}
	sessionFile := sm.GetSessionFile()
	if sessionFile != filepath.Join(customDir, "test.jsonl") {
		t.Errorf("expected session in custom dir, got %q", sessionFile)
	}
}

func TestCreateSessionManager_Continue(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	sessionDir := filepath.Join(agentDir, "sessions")

	os.MkdirAll(sessionDir, 0o755)
	sessionFile := filepath.Join(sessionDir, "session-abc.jsonl")
	os.WriteFile(sessionFile, []byte(`{"type":"header","version":"1","cwd":"`+cwd+`"}`+"\n"), 0o600)
	os.Chtimes(sessionFile, time.Now(), time.Now())

	args := &Args{Continue: true, SessionDir: sessionDir}
	sm := createSessionManager(args, cwd, agentDir)

	if !sm.IsPersisted() {
		t.Error("continue session should be persisted")
	}
}

func TestCreateSessionManager_Continue_NoExisting(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	sessionDir := filepath.Join(agentDir, "sessions-empty")

	args := &Args{Continue: true, SessionDir: sessionDir}
	sm := createSessionManager(args, cwd, agentDir)

	if !sm.IsPersisted() {
		t.Error("continue with no existing session should still create persisted session")
	}
}

// ============================================================================
// resolveTools
// ============================================================================

func TestResolveTools_Default(t *testing.T) {
	args := &Args{}
	result := resolveTools(args, t.TempDir())
	if result != nil {
		t.Error("expected nil tools for default args (use SDK defaults)")
	}
}

func TestResolveTools_NoTools(t *testing.T) {
	args := &Args{NoTools: true}
	result := resolveTools(args, t.TempDir())
	if result == nil {
		t.Fatal("expected non-nil tools slice for --no-tools")
	}
	if len(result) != 0 {
		t.Errorf("expected 0 tools for --no-tools, got %d", len(result))
	}
}

func TestResolveTools_NoToolsWithTools(t *testing.T) {
	args := &Args{NoTools: true, Tools: []string{"read", "bash"}}
	result := resolveTools(args, t.TempDir())
	if result == nil {
		t.Fatal("expected non-nil tools slice")
	}
	if len(result) != 2 {
		t.Errorf("expected 2 tools, got %d", len(result))
	}
}

func TestResolveTools_ToolsOnly(t *testing.T) {
	args := &Args{Tools: []string{"read"}}
	result := resolveTools(args, t.TempDir())
	if result == nil {
		t.Fatal("expected non-nil tools slice for --tools")
	}
	if len(result) != 1 {
		t.Errorf("expected 1 tool, got %d", len(result))
	}
	if result[0].Name != "read" {
		t.Errorf("expected read tool, got %q", result[0].Name)
	}
}

func TestResolveTools_AllTools(t *testing.T) {
	args := &Args{Tools: []string{"read", "bash", "edit", "write", "grep", "find", "ls"}}
	result := resolveTools(args, t.TempDir())
	if len(result) != 7 {
		t.Errorf("expected 7 tools, got %d", len(result))
	}
}

func TestResolveTools_UnknownToolIgnored(t *testing.T) {
	args := &Args{Tools: []string{"read", "nonexistent"}}
	result := resolveTools(args, t.TempDir())
	if len(result) != 1 {
		t.Errorf("expected 1 tool (unknown ignored), got %d", len(result))
	}
}

// ============================================================================
// resolveEnabledExtensions
// ============================================================================

func TestResolveEnabledExtensions_Default(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	sm := core.NewSettingsManager(cwd, agentDir)

	args := &Args{}
	result := resolveEnabledExtensions(args, sm)
	if len(result) != 0 {
		t.Errorf("expected 0 extensions by default, got %d: %v", len(result), result)
	}
}

func TestResolveEnabledExtensions_FromSettings(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "settings.json"),
		[]byte(`{"extensions":["notify","sandbox"]}`), 0o600)

	sm := core.NewSettingsManager(cwd, agentDir)
	args := &Args{}
	result := resolveEnabledExtensions(args, sm)
	if len(result) != 2 {
		t.Fatalf("expected 2 extensions from settings, got %d: %v", len(result), result)
	}
	if result[0] != "notify" || result[1] != "sandbox" {
		t.Errorf("unexpected extensions: %v", result)
	}
}

func TestResolveEnabledExtensions_FromCLI(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	sm := core.NewSettingsManager(cwd, agentDir)

	args := &Args{Extensions: []string{"notify", "sandbox"}}
	result := resolveEnabledExtensions(args, sm)
	if len(result) != 2 {
		t.Fatalf("expected 2 extensions from CLI, got %d: %v", len(result), result)
	}
}

func TestResolveEnabledExtensions_MergedDeduped(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "settings.json"),
		[]byte(`{"extensions":["notify"]}`), 0o600)

	sm := core.NewSettingsManager(cwd, agentDir)
	args := &Args{Extensions: []string{"notify", "sandbox"}}
	result := resolveEnabledExtensions(args, sm)
	if len(result) != 2 {
		t.Fatalf("expected 2 extensions (deduped), got %d: %v", len(result), result)
	}
}

func TestResolveEnabledExtensions_NoExtensionsFlag(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "settings.json"),
		[]byte(`{"extensions":["notify","sandbox"]}`), 0o600)

	sm := core.NewSettingsManager(cwd, agentDir)
	args := &Args{
		NoExtensions: true,
		Extensions:   []string{"sandbox"},
	}
	result := resolveEnabledExtensions(args, sm)
	if len(result) != 0 {
		t.Errorf("expected 0 extensions with --no-extensions, got %d: %v", len(result), result)
	}
}

// ============================================================================
// readPipedStdin
// ============================================================================

func TestReadPipedStdin_FromPipe(t *testing.T) {
	// Create a pipe and replace os.Stdin temporarily.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	// Write content to the pipe and close the write end.
	_, _ = w.WriteString("hello from pipe\nsecond line")
	w.Close()

	result := readPipedStdin()
	if result != "hello from pipe\nsecond line" {
		t.Errorf("expected piped content, got %q", result)
	}
}

func TestReadPipedStdin_EmptyPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	w.Close()

	result := readPipedStdin()
	if result != "" {
		t.Errorf("expected empty string for empty pipe, got %q", result)
	}
}

func TestReadPipedStdin_WhitespaceOnly(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	_, _ = w.WriteString("  \n  \n  ")
	w.Close()

	result := readPipedStdin()
	if result != "" {
		t.Errorf("expected empty string for whitespace-only pipe, got %q", result)
	}
}

// ============================================================================
// runListModels
// ============================================================================

func TestRunListModels_AllModels(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("TAU_AGENT_DIR", agentDir)

	providers.RegisterDefaultProviders()

	args := &Args{ListModels: true}
	err := runListModels(args)
	if err != nil {
		t.Fatalf("runListModels returned error: %v", err)
	}
}

func TestRunListModels_WithPattern(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("TAU_AGENT_DIR", agentDir)

	providers.RegisterDefaultProviders()

	args := &Args{ListModels: "claude"}
	err := runListModels(args)
	if err != nil {
		t.Fatalf("runListModels returned error: %v", err)
	}
}
