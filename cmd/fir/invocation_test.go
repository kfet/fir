package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/session/store"
)

func TestBuildInvocation_EmptyArgsReturnsNil(t *testing.T) {
	args := &Args{Seen: map[string]bool{}}
	if got := BuildInvocation(args); got != nil {
		t.Errorf("expected nil for empty args, got %+v", got)
	}
}

func TestBuildInvocation_PopulatesFields(t *testing.T) {
	args := &Args{
		Provider:     "anthropic",
		Model:        "claude-sonnet",
		Thinking:     agent.ThinkingLevel("high"),
		Extensions:   []string{"demo"},
		NoExtensions: false,
		Tools:        []string{"read", "edit"},
		NoMCP:        true,
		Seen:         map[string]bool{},
	}
	inv := BuildInvocation(args)
	if inv == nil {
		t.Fatal("expected non-nil invocation")
	}
	if inv.Provider != "anthropic" || inv.Model != "claude-sonnet" {
		t.Errorf("provider/model wrong: %+v", inv)
	}
	if inv.Thinking != "high" {
		t.Errorf("thinking wrong: %q", inv.Thinking)
	}
	if !inv.NoMCP {
		t.Error("NoMCP not set")
	}
	if len(inv.Extensions) != 1 || inv.Extensions[0] != "demo" {
		t.Errorf("extensions wrong: %+v", inv.Extensions)
	}
}

func TestBuildInvocation_HashesMCPConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(p, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := &Args{MCPConfig: p, Seen: map[string]bool{}}
	inv := BuildInvocation(args)
	if inv == nil || inv.MCPConfig != p {
		t.Fatalf("invocation MCPConfig wrong: %+v", inv)
	}
	if inv.MCPConfigSHA256 == "" {
		t.Error("expected non-empty SHA256 for existing file")
	}
}

func TestApplyInvocation_NoRestoreConfigShortCircuits(t *testing.T) {
	args := &Args{NoRestoreConfig: true, Seen: map[string]bool{}}
	inv := &store.SessionInvocation{Model: "claude", Extensions: []string{"demo"}}
	ApplyInvocation(args, inv, nil)
	if args.Model != "" || len(args.Extensions) != 0 {
		t.Errorf("--no-restore-config should suppress restore, got %+v", args)
	}
}

func TestApplyInvocation_FillsMissingFields(t *testing.T) {
	args := &Args{Seen: map[string]bool{}}
	inv := &store.SessionInvocation{
		Provider:     "anthropic",
		Model:        "claude-sonnet",
		Thinking:     "high",
		Extensions:   []string{"demo"},
		Tools:        []string{"read"},
		NoMCP:        true,
		NoExtensions: false,
	}
	ApplyInvocation(args, inv, nil)
	if args.Provider != "anthropic" || args.Model != "claude-sonnet" {
		t.Errorf("provider/model not restored: %+v", args)
	}
	if args.Thinking != "high" {
		t.Errorf("thinking not restored: %q", args.Thinking)
	}
	if !args.NoMCP {
		t.Error("NoMCP not restored")
	}
	if len(args.Extensions) != 1 || args.Extensions[0] != "demo" {
		t.Errorf("extensions not restored: %+v", args.Extensions)
	}
}

func TestApplyInvocation_ExplicitFlagWinsOverPersisted(t *testing.T) {
	args := &Args{
		Model: "gpt-5",
		Seen:  map[string]bool{"--model": true},
	}
	inv := &store.SessionInvocation{Model: "claude-sonnet"}
	ApplyInvocation(args, inv, nil)
	if args.Model != "gpt-5" {
		t.Errorf("explicit --model should win, got %q", args.Model)
	}
}

func TestApplyInvocation_ExtensionsReplaceNotUnion(t *testing.T) {
	args := &Args{
		Extensions: []string{"local"},
		Seen:       map[string]bool{"--extension": true},
	}
	inv := &store.SessionInvocation{Extensions: []string{"persisted1", "persisted2"}}
	ApplyInvocation(args, inv, nil)
	if len(args.Extensions) != 1 || args.Extensions[0] != "local" {
		t.Errorf("explicit -e should replace, not union, got %+v", args.Extensions)
	}
}

func TestApplyInvocation_MCPConfigHashMismatchWarns(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(p, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := &Args{Seen: map[string]bool{}}
	inv := &store.SessionInvocation{
		MCPConfig:       p,
		MCPConfigSHA256: "deadbeef0000", // wrong on purpose
	}
	var got []string
	ApplyInvocation(args, inv, func(s string) { got = append(got, s) })
	if args.MCPConfig != p {
		t.Errorf("MCPConfig should still be applied, got %q", args.MCPConfig)
	}
	if len(got) == 0 || !strings.Contains(got[0], "changed since") {
		t.Errorf("expected drift warning, got %v", got)
	}
}

func TestApplyInvocation_MCPConfigMissingWarnsAndClears(t *testing.T) {
	args := &Args{Seen: map[string]bool{}}
	inv := &store.SessionInvocation{
		MCPConfig:       "/nonexistent/mcp.json",
		MCPConfigSHA256: "deadbeef",
	}
	var got []string
	ApplyInvocation(args, inv, func(s string) { got = append(got, s) })
	if args.MCPConfig != "" {
		t.Errorf("missing MCPConfig should be cleared, got %q", args.MCPConfig)
	}
	if len(got) == 0 || !strings.Contains(got[0], "missing") {
		t.Errorf("expected missing-file warning, got %v", got)
	}
}

func TestApplyInvocation_NoMCPSuppressesMCPConfigRestore(t *testing.T) {
	args := &Args{
		NoMCP: true,
		Seen:  map[string]bool{"--no-mcp": true},
	}
	inv := &store.SessionInvocation{MCPConfig: "/x.json"}
	ApplyInvocation(args, inv, nil)
	if args.MCPConfig != "" {
		t.Errorf("--no-mcp should suppress MCPConfig restore, got %q", args.MCPConfig)
	}
}

func TestMaybeRestoreInvocation_NewSessionStamps(t *testing.T) {
	dir := t.TempDir()
	sm := store.NewSessionStore(dir, dir)
	args := &Args{Model: "claude", Seen: map[string]bool{}}

	var buf bytes.Buffer
	maybeRestoreInvocation(args, sm, false, &buf)

	inv := sm.GetInvocation()
	if inv == nil {
		t.Fatal("expected invocation to be stamped on new session")
	}
	if inv.Model != "claude" {
		t.Errorf("stamped model: %q", inv.Model)
	}
}

func TestMaybeRestoreInvocation_ResumedSessionApplies(t *testing.T) {
	dir := t.TempDir()

	// First "session" — stamp an invocation.
	sm1 := store.NewSessionStore(dir, dir)
	sm1.StampInvocation(&store.SessionInvocation{
		Model:      "claude",
		Extensions: []string{"demo"},
	})
	sessionFile := sm1.GetSessionFile()
	sm1.Close()

	// Reopen as resumed.
	sm2, _ := store.OpenSessionStore(sessionFile)
	args := &Args{Seen: map[string]bool{}}
	var buf bytes.Buffer
	maybeRestoreInvocation(args, sm2, true, &buf)

	if args.Model != "claude" {
		t.Errorf("model not restored on resume: %q", args.Model)
	}
	if len(args.Extensions) != 1 || args.Extensions[0] != "demo" {
		t.Errorf("extensions not restored: %+v", args.Extensions)
	}
}

func TestMaybeRestoreInvocation_LegacyResumedSessionIsNoop(t *testing.T) {
	dir := t.TempDir()
	sm := store.NewSessionStore(dir, dir) // no Stamp
	sessionFile := sm.GetSessionFile()
	sm.Close()
	// Reopen.
	sm2, _ := store.OpenSessionStore(sessionFile)
	args := &Args{Seen: map[string]bool{}}
	maybeRestoreInvocation(args, sm2, true, nil)
	if args.Model != "" || len(args.Extensions) != 0 {
		t.Errorf("legacy resume should leave args untouched, got %+v", args)
	}
}

func TestParseArgs_SeenTracksFlags(t *testing.T) {
	a := ParseArgs([]string{"--model", "claude", "-e", "demo", "hello"})
	if !a.Seen["--model"] {
		t.Error("--model not in Seen")
	}
	if !a.Seen["--extension"] {
		t.Error("--extension (via -e) not in Seen")
	}
	if a.Seen["--mcp-config"] {
		t.Error("--mcp-config falsely in Seen")
	}
}

func TestParseArgs_NoRestoreConfig(t *testing.T) {
	a := ParseArgs([]string{"--no-restore-config", "-c"})
	if !a.NoRestoreConfig {
		t.Error("--no-restore-config not parsed")
	}
	if !a.Seen["--no-restore-config"] {
		t.Error("--no-restore-config not in Seen")
	}
	if !a.Continue {
		t.Error("--continue should still parse")
	}
}
