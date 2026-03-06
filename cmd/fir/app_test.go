package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers"
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
	t.Setenv("FIR_AGENT_DIR", agentDir)

	providers.RegisterDefaultProviders()

	args := &Args{ListModels: true}
	err := runListModels(args)
	if err != nil {
		t.Fatalf("runListModels returned error: %v", err)
	}
}

func TestRunListModels_WithPattern(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", agentDir)

	providers.RegisterDefaultProviders()

	args := &Args{ListModels: "claude"}
	err := runListModels(args)
	if err != nil {
		t.Fatalf("runListModels returned error: %v", err)
	}
}

// ============================================================================
// runExport
// ============================================================================

func TestRunExport_RequiresSession(t *testing.T) {
	providers.RegisterDefaultProviders()
	args := &Args{Export: filepath.Join(t.TempDir(), "out.html")}
	err := runExport(args)
	if err == nil {
		t.Fatal("expected error when --session not provided")
	}
	if !strings.Contains(err.Error(), "--export requires --session") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunExport_ExportsToFile(t *testing.T) {
	providers.RegisterDefaultProviders()
	agentDir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", agentDir)

	outFile := filepath.Join(t.TempDir(), "export.html")
	args := &Args{
		Session: "test.jsonl",
		Export:  outFile,
	}
	if err := runExport(args); err != nil {
		t.Fatalf("runExport error: %v", err)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("expected export file at %s: %v", outFile, err)
	}
	if !strings.Contains(string(data), "<html") {
		t.Error("expected exported file to contain HTML")
	}
}

// ============================================================================
// checkModelAvailable
// ============================================================================

func TestCheckModelAvailable_WithModel(t *testing.T) {
	model := &ai.Model{ID: "test", Provider: "test"}
	for _, args := range []*Args{
		{},
		{Print: true},
		{OutputMode: ModeJSON},
		{OutputMode: ModeRPC},
	} {
		if err := checkModelAvailable(model, args); err != nil {
			t.Errorf("expected no error with model present, got %v (args: %+v)", err, args)
		}
	}
}

func TestCheckModelAvailable_NilModel_Interactive(t *testing.T) {
	if err := checkModelAvailable(nil, &Args{}); err != nil {
		t.Errorf("expected nil model allowed in interactive mode, got %v", err)
	}
}

func TestCheckModelAvailable_NilModel_ACP(t *testing.T) {
	// ACP mode creates sessions on demand — no model at startup is fine.
	if err := checkModelAvailable(nil, &Args{OutputMode: ModeACP}); err != nil {
		t.Errorf("expected nil model allowed in ACP mode, got %v", err)
	}
}

func TestCheckModelAvailable_NilModel_NonInteractive(t *testing.T) {
	cases := []struct {
		name string
		args *Args
	}{
		{"print", &Args{Print: true}},
		{"json", &Args{OutputMode: ModeJSON}},
		{"rpc", &Args{OutputMode: ModeRPC}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkModelAvailable(nil, tc.args); err == nil {
				t.Error("expected error for nil model in non-interactive mode")
			}
		})
	}
}

// ============================================================================
// clampThinkingLevel
// ============================================================================

type mockThinkingSetter struct {
	model    *ai.Model
	thinking string
}

func (m *mockThinkingSetter) Model() *ai.Model          { return m.model }
func (m *mockThinkingSetter) ThinkingLevel() string     { return m.thinking }
func (m *mockThinkingSetter) SetThinkingLevel(l string) { m.thinking = l }

func TestClampThinkingLevel_NilModel(t *testing.T) {
	s := &mockThinkingSetter{model: nil, thinking: ""}
	clampThinkingLevel(s, "high")
	if s.thinking != "" {
		t.Errorf("expected thinking unchanged with nil model, got %q", s.thinking)
	}
}

func TestClampThinkingLevel_EmptyThinking(t *testing.T) {
	s := &mockThinkingSetter{model: &ai.Model{Reasoning: true}, thinking: "high"}
	clampThinkingLevel(s, "")
	if s.thinking != "high" {
		t.Errorf("expected thinking unchanged with empty level, got %q", s.thinking)
	}
}

func TestClampThinkingLevel_NoReasoning(t *testing.T) {
	s := &mockThinkingSetter{model: &ai.Model{Reasoning: false}, thinking: ""}
	clampThinkingLevel(s, "high")
	if s.thinking != "off" {
		t.Errorf("expected thinking clamped to off, got %q", s.thinking)
	}
}

func TestClampThinkingLevel_WithReasoning(t *testing.T) {
	s := &mockThinkingSetter{model: &ai.Model{Reasoning: true}, thinking: ""}
	clampThinkingLevel(s, "high")
	if s.thinking != "high" {
		t.Errorf("expected thinking set to high, got %q", s.thinking)
	}
}

// ============================================================================
// resolveAgentDir
// ============================================================================

func TestResolveAgentDir_Default(t *testing.T) {
	t.Setenv("FIR_AGENT_DIR", "")
	got := resolveAgentDir()
	if got == "" {
		t.Error("expected non-empty default agent dir")
	}
}

func TestResolveAgentDir_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", dir)
	got := resolveAgentDir()
	if got != dir {
		t.Errorf("expected %q, got %q", dir, got)
	}
}

// ============================================================================
// drainUpdateNotice
// ============================================================================

func TestDrainUpdateNotice_WithNotice(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "› fir v1.0.0 available"
	// drainUpdateNotice should not block and should consume the message
	drainUpdateNotice(ch)
	if len(ch) != 0 {
		t.Error("expected channel to be drained")
	}
}

func TestDrainUpdateNotice_EmptyNotice(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "" // empty — no notice to print
	drainUpdateNotice(ch)
	if len(ch) != 0 {
		t.Error("expected channel to be drained even for empty notice")
	}
}

func TestDrainUpdateNotice_NoValue_NonBlocking(t *testing.T) {
	ch := make(chan string, 1)
	// Channel is empty — drainUpdateNotice must not block.
	done := make(chan struct{})
	go func() {
		drainUpdateNotice(ch)
		close(done)
	}()
	select {
	case <-done:
		// OK — returned immediately
	case <-time.After(time.Second):
		t.Error("drainUpdateNotice blocked on empty channel")
	}
}

// ============================================================================
// runUpdate
// ============================================================================

func TestRunUpdate_MacOS_NoNetwork(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-specific path")
	}
	// Replace the default HTTP transport so FetchLatest fails immediately.
	orig := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("mock network error")
	})
	defer func() { http.DefaultTransport = orig }()

	err := runUpdate()
	if err == nil {
		t.Error("expected error when FetchLatest fails, got nil")
	}
}

// roundTripFunc is a minimal http.RoundTripper for testing.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRunUpdate_Linux_FetchFails(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-specific path")
	}
	// Replace the default HTTP transport so FetchLatest fails immediately.
	orig := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("mock network error")
	})
	defer func() { http.DefaultTransport = orig }()

	err := runUpdate()
	if err == nil {
		t.Error("expected error when FetchLatest fails, got nil")
	}
}
