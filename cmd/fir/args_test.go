package main

import (
	"testing"
	"time"

	"github.com/kfet/fir/pkg/agent"
)

func TestParseArgs_Empty(t *testing.T) {
	args := ParseArgs([]string{})
	if args.Help || args.Version || args.Print || args.Continue || args.Resume {
		t.Error("expected all flags false for empty args")
	}
	if len(args.Messages) != 0 {
		t.Error("expected no messages")
	}
}

func TestParseArgs_Help(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		args := ParseArgs([]string{flag})
		if !args.Help {
			t.Errorf("expected Help=true for %s", flag)
		}
	}
}

func TestParseArgs_Version(t *testing.T) {
	for _, flag := range []string{"--version", "-V"} {
		args := ParseArgs([]string{flag})
		if !args.Version {
			t.Errorf("expected Version=true for %s", flag)
		}
	}
}

func TestParseArgs_VerboseShort(t *testing.T) {
	args := ParseArgs([]string{"-v"})
	if args.Version {
		t.Error("expected -v to NOT set Version (it now means verbose)")
	}
	if args.VerboseCount != 1 {
		t.Errorf("expected VerboseCount=1, got %d", args.VerboseCount)
	}
	if !args.LegacyVersionHint {
		t.Error("expected LegacyVersionHint=true for lone `-v`")
	}
}

func TestParseArgs_VerboseShortRepeated(t *testing.T) {
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"-v"}, 1},
		{[]string{"-vv"}, 2},
		{[]string{"-vvv"}, 3},
		{[]string{"-v", "-v"}, 2},
		{[]string{"-v", "-vv"}, 3},
		{[]string{"-vvvv"}, 4},
	}
	for _, tc := range cases {
		got := ParseArgs(tc.args).VerboseCount
		if got != tc.want {
			t.Errorf("args=%v: VerboseCount=%d want %d", tc.args, got, tc.want)
		}
	}
}

func TestParseArgs_VerboseDoesNotSetLegacyHintWithOtherArgs(t *testing.T) {
	args := ParseArgs([]string{"-v", "hello"})
	if args.LegacyVersionHint {
		t.Error("expected no LegacyVersionHint when other args present")
	}
}

func TestParseArgs_Print(t *testing.T) {
	for _, flag := range []string{"--print", "-p"} {
		args := ParseArgs([]string{flag})
		if !args.Print {
			t.Errorf("expected Print=true for %s", flag)
		}
	}
}

func TestParseArgs_Continue(t *testing.T) {
	for _, flag := range []string{"--continue", "-c"} {
		args := ParseArgs([]string{flag})
		if !args.Continue {
			t.Errorf("expected Continue=true for %s", flag)
		}
	}
}

func TestParseArgs_Resume(t *testing.T) {
	for _, flag := range []string{"--resume", "-r"} {
		args := ParseArgs([]string{flag})
		if !args.Resume {
			t.Errorf("expected Resume=true for %s", flag)
		}
	}
}

func TestParseArgs_Provider(t *testing.T) {
	args := ParseArgs([]string{"--provider", "anthropic"})
	if args.Provider != "anthropic" {
		t.Errorf("expected provider 'anthropic', got %q", args.Provider)
	}
}

func TestParseArgs_Model(t *testing.T) {
	args := ParseArgs([]string{"--model", "claude-sonnet-4-5"})
	if args.Model != "claude-sonnet-4-5" {
		t.Errorf("expected model 'claude-sonnet-4-5', got %q", args.Model)
	}
}

func TestParseArgs_ApiKey(t *testing.T) {
	args := ParseArgs([]string{"--api-key", "sk-test"})
	if args.ApiKey != "sk-test" {
		t.Errorf("expected apiKey 'sk-test', got %q", args.ApiKey)
	}
}

func TestParseArgs_SystemPrompt(t *testing.T) {
	args := ParseArgs([]string{"--system-prompt", "You are a helpful assistant"})
	if args.SystemPrompt != "You are a helpful assistant" {
		t.Errorf("unexpected system prompt: %q", args.SystemPrompt)
	}
}

func TestParseArgs_AppendSystemPrompt(t *testing.T) {
	args := ParseArgs([]string{"--append-system-prompt", "Extra instructions"})
	if args.AppendSystemPrompt != "Extra instructions" {
		t.Errorf("unexpected append system prompt: %q", args.AppendSystemPrompt)
	}
}

func TestParseArgs_Thinking(t *testing.T) {
	args := ParseArgs([]string{"--thinking", "high"})
	if args.Thinking != agent.ThinkingHigh {
		t.Errorf("expected thinking 'high', got %q", args.Thinking)
	}
}

func TestParseArgs_ThinkingInvalid(t *testing.T) {
	args := ParseArgs([]string{"--thinking", "invalid"})
	if args.Thinking != "" {
		t.Errorf("expected empty thinking for invalid value, got %q", args.Thinking)
	}
}

func TestParseArgs_Mode(t *testing.T) {
	for _, mode := range []string{"text", "json"} {
		args := ParseArgs([]string{"--mode", mode})
		if string(args.OutputMode) != mode {
			t.Errorf("expected mode %q, got %q", mode, args.OutputMode)
		}
	}
}

func TestParseArgs_NoSession(t *testing.T) {
	args := ParseArgs([]string{"--no-session"})
	if !args.NoSession {
		t.Error("expected NoSession=true")
	}
}

func TestParseArgs_Session(t *testing.T) {
	args := ParseArgs([]string{"--session", "/path/to/session.jsonl"})
	if args.Session != "/path/to/session.jsonl" {
		t.Errorf("expected session path, got %q", args.Session)
	}
}

func TestParseArgs_SessionDir(t *testing.T) {
	args := ParseArgs([]string{"--session-dir", "/tmp/sessions"})
	if args.SessionDir != "/tmp/sessions" {
		t.Errorf("expected session dir, got %q", args.SessionDir)
	}
}

func TestParseArgs_AgentDir(t *testing.T) {
	args := ParseArgs([]string{"--agent-dir", "/tmp/fir-agent", "hello"})
	if args.AgentDir != "/tmp/fir-agent" {
		t.Errorf("expected agent dir, got %q", args.AgentDir)
	}
	if !args.Seen["--agent-dir"] {
		t.Error("expected --agent-dir to be marked seen")
	}
	if len(args.Messages) != 1 || args.Messages[0] != "hello" {
		t.Fatalf("expected message preserved, got %#v", args.Messages)
	}
}

func TestParseArgs_AgentDirEquals(t *testing.T) {
	args := ParseArgs([]string{"--agent-dir=/tmp/fir-agent"})
	if args.AgentDir != "/tmp/fir-agent" {
		t.Errorf("expected agent dir, got %q", args.AgentDir)
	}
}

func TestParseArgs_SessionName(t *testing.T) {
	args := ParseArgs([]string{"--session-name", "my cool session"})
	if args.SessionName != "my cool session" {
		t.Errorf("expected session name, got %q", args.SessionName)
	}
}

func TestParseArgs_Models(t *testing.T) {
	args := ParseArgs([]string{"--models", "sonnet,haiku,gpt-4o"})
	if len(args.Models) != 3 {
		t.Fatalf("expected 3 models, got %d: %v", len(args.Models), args.Models)
	}
	if args.Models[0] != "sonnet" || args.Models[1] != "haiku" || args.Models[2] != "gpt-4o" {
		t.Errorf("unexpected models: %v", args.Models)
	}
}

func TestParseArgs_ModelsWithSpaces(t *testing.T) {
	args := ParseArgs([]string{"--models", " sonnet , haiku "})
	if len(args.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %v", len(args.Models), args.Models)
	}
	if args.Models[0] != "sonnet" || args.Models[1] != "haiku" {
		t.Errorf("unexpected models: %v", args.Models)
	}
}

func TestParseArgs_NoTools(t *testing.T) {
	args := ParseArgs([]string{"--no-tools"})
	if !args.NoTools {
		t.Error("expected NoTools=true")
	}
}

func TestParseArgs_Tools(t *testing.T) {
	args := ParseArgs([]string{"--tools", "read,bash,edit"})
	if len(args.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(args.Tools))
	}
}

func TestParseArgs_NoExtensions(t *testing.T) {
	args := ParseArgs([]string{"--no-extensions"})
	if !args.NoExtensions {
		t.Error("expected NoExtensions=true")
	}
}

func TestParseArgs_Extension(t *testing.T) {
	args := ParseArgs([]string{"--extension", "myext"})
	if len(args.Extensions) != 1 || args.Extensions[0] != "myext" {
		t.Errorf("expected Extensions=[myext], got %v", args.Extensions)
	}
}

func TestParseArgs_ExtensionShortFlag(t *testing.T) {
	args := ParseArgs([]string{"-e", "ext1", "-e", "ext2"})
	if len(args.Extensions) != 2 {
		t.Fatalf("expected 2 extensions, got %d: %v", len(args.Extensions), args.Extensions)
	}
	if args.Extensions[0] != "ext1" || args.Extensions[1] != "ext2" {
		t.Errorf("unexpected extensions: %v", args.Extensions)
	}
}

func TestParseArgs_ExtensionMultiple(t *testing.T) {
	args := ParseArgs([]string{"--extension", "alpha", "--extension", "beta"})
	if len(args.Extensions) != 2 {
		t.Fatalf("expected 2 extensions, got %d", len(args.Extensions))
	}
	if args.Extensions[0] != "alpha" || args.Extensions[1] != "beta" {
		t.Errorf("unexpected: %v", args.Extensions)
	}
}

func TestParseArgs_DisableExtension(t *testing.T) {
	args := ParseArgs([]string{"--disable-extension", "myext"})
	if len(args.DisabledExtensions) != 1 || args.DisabledExtensions[0] != "myext" {
		t.Errorf("expected DisabledExtensions=[myext], got %v", args.DisabledExtensions)
	}
}

func TestParseArgs_DisableExtensionMultiple(t *testing.T) {
	args := ParseArgs([]string{"--disable-extension", "ext1", "-d", "ext2"})
	if len(args.DisabledExtensions) != 2 {
		t.Fatalf("expected 2 disabled extensions, got %d", len(args.DisabledExtensions))
	}
	if args.DisabledExtensions[0] != "ext1" || args.DisabledExtensions[1] != "ext2" {
		t.Errorf("unexpected: %v", args.DisabledExtensions)
	}
}

func TestParseArgs_Skills(t *testing.T) {
	args := ParseArgs([]string{"--skill", "skill1.md", "--skill", "skill2.md"})
	if len(args.Skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(args.Skills))
	}
}

func TestParseArgs_Themes(t *testing.T) {
	args := ParseArgs([]string{"--theme", "dark.json"})
	if len(args.Themes) != 1 {
		t.Fatalf("expected 1 theme, got %d", len(args.Themes))
	}
}

func TestParseArgs_NoSkills(t *testing.T) {
	args := ParseArgs([]string{"--no-skills"})
	if !args.NoSkills {
		t.Error("expected NoSkills=true")
	}
}

func TestParseArgs_NoThemes(t *testing.T) {
	args := ParseArgs([]string{"--no-themes"})
	if !args.NoThemes {
		t.Error("expected NoThemes=true")
	}
}

func TestParseArgs_Export(t *testing.T) {
	args := ParseArgs([]string{"--export", "session.jsonl"})
	if args.Export != "session.jsonl" {
		t.Errorf("expected export 'session.jsonl', got %q", args.Export)
	}
}

func TestParseArgs_ListModelsNoPattern(t *testing.T) {
	args := ParseArgs([]string{"--list-models"})
	if args.ListModels != true {
		t.Errorf("expected ListModels=true, got %v", args.ListModels)
	}
}

func TestParseArgs_ListModelsWithPattern(t *testing.T) {
	args := ParseArgs([]string{"--list-models", "sonnet"})
	if args.ListModels != "sonnet" {
		t.Errorf("expected ListModels='sonnet', got %v", args.ListModels)
	}
}

func TestParseArgs_ListModelsBeforeFlag(t *testing.T) {
	// --list-models followed by a flag should not consume the flag as pattern
	args := ParseArgs([]string{"--list-models", "--verbose"})
	if args.ListModels != true {
		t.Errorf("expected ListModels=true, got %v", args.ListModels)
	}
	if !args.Verbose {
		t.Error("expected Verbose=true")
	}
}

func TestParseArgs_ListAvailableModelsNoPattern(t *testing.T) {
	args := ParseArgs([]string{"--list-available-models"})
	if args.ListAvailModels != true {
		t.Errorf("expected ListAvailModels=true, got %v", args.ListAvailModels)
	}
}

func TestParseArgs_ListAvailableModelsWithPattern(t *testing.T) {
	args := ParseArgs([]string{"--list-available-models", "gemini"})
	if args.ListAvailModels != "gemini" {
		t.Errorf("expected ListAvailModels='gemini', got %v", args.ListAvailModels)
	}
}

func TestParseArgs_ListAvailableModelsBeforeFlag(t *testing.T) {
	args := ParseArgs([]string{"--list-available-models", "--verbose"})
	if args.ListAvailModels != true {
		t.Errorf("expected ListAvailModels=true, got %v", args.ListAvailModels)
	}
	if !args.Verbose {
		t.Error("expected Verbose=true")
	}
}

func TestParseArgs_Verbose(t *testing.T) {
	args := ParseArgs([]string{"--verbose"})
	if !args.Verbose {
		t.Error("expected Verbose=true")
	}
}

func TestParseArgs_Messages(t *testing.T) {
	args := ParseArgs([]string{"hello", "world"})
	if len(args.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(args.Messages))
	}
	if args.Messages[0] != "hello" || args.Messages[1] != "world" {
		t.Errorf("unexpected messages: %v", args.Messages)
	}
}

func TestParseArgs_FileArgs(t *testing.T) {
	args := ParseArgs([]string{"@file1.md", "@image.png"})
	if len(args.FileArgs) != 2 {
		t.Fatalf("expected 2 file args, got %d", len(args.FileArgs))
	}
	if args.FileArgs[0] != "file1.md" || args.FileArgs[1] != "image.png" {
		t.Errorf("unexpected file args: %v", args.FileArgs)
	}
}

func TestParseArgs_Mixed(t *testing.T) {
	args := ParseArgs([]string{
		"--provider", "anthropic",
		"--model", "claude-sonnet-4-5",
		"-p",
		"@readme.md",
		"Summarize this file",
	})

	if args.Provider != "anthropic" {
		t.Errorf("expected provider 'anthropic'")
	}
	if args.Model != "claude-sonnet-4-5" {
		t.Errorf("expected model 'claude-sonnet-4-5'")
	}
	if !args.Print {
		t.Error("expected Print=true")
	}
	if len(args.FileArgs) != 1 || args.FileArgs[0] != "readme.md" {
		t.Errorf("expected file args [readme.md], got %v", args.FileArgs)
	}
	if len(args.Messages) != 1 || args.Messages[0] != "Summarize this file" {
		t.Errorf("expected messages ['Summarize this file'], got %v", args.Messages)
	}
}

func TestParseArgs_MCPConfig(t *testing.T) {
	args := ParseArgs([]string{"--mcp-config", "/tmp/mcp.json", "hello"})
	if args.MCPConfig != "/tmp/mcp.json" {
		t.Errorf("expected MCPConfig '/tmp/mcp.json', got %q", args.MCPConfig)
	}
	if args.NoMCP {
		t.Error("expected NoMCP=false")
	}
}

func TestParseArgs_MCPConfigEnvVar(t *testing.T) {
	t.Setenv("FIR_MCP_CONFIG", "/tmp/env-mcp.json")
	args := ParseArgs([]string{"hello"})
	if args.MCPConfig != "/tmp/env-mcp.json" {
		t.Errorf("expected MCPConfig '/tmp/env-mcp.json', got %q", args.MCPConfig)
	}
}

func TestParseArgs_MCPConfigFlagOverridesEnv(t *testing.T) {
	t.Setenv("FIR_MCP_CONFIG", "/tmp/env-mcp.json")
	args := ParseArgs([]string{"--mcp-config", "/tmp/flag-mcp.json", "hello"})
	if args.MCPConfig != "/tmp/flag-mcp.json" {
		t.Errorf("expected flag to win, got %q", args.MCPConfig)
	}
}

func TestParseArgs_NoMCP(t *testing.T) {
	args := ParseArgs([]string{"--no-mcp", "hello"})
	if !args.NoMCP {
		t.Error("expected NoMCP=true")
	}
	if args.MCPConfig != "" {
		t.Errorf("expected empty MCPConfig, got %q", args.MCPConfig)
	}
}

func TestIsValidThinkingLevel(t *testing.T) {
	for _, level := range ValidThinkingLevels {
		if !IsValidThinkingLevel(level) {
			t.Errorf("expected %q to be valid", level)
		}
	}
	if IsValidThinkingLevel("invalid") {
		t.Error("expected 'invalid' to be invalid")
	}
	if IsValidThinkingLevel("") {
		t.Error("expected empty string to be invalid")
	}
}

func TestParseArgs_WaitMCP(t *testing.T) {
	args := ParseArgs([]string{"--wait-mcp", "hello"})
	if !args.WaitMCP {
		t.Error("expected WaitMCP=true")
	}
	if len(args.Messages) != 1 || args.Messages[0] != "hello" {
		t.Errorf("expected message 'hello' to survive the flag, got %v", args.Messages)
	}
}

func TestParseArgs_WaitMCPDefaultsFalse(t *testing.T) {
	args := ParseArgs([]string{"hello"})
	if args.WaitMCP {
		t.Error("expected WaitMCP=false by default")
	}
}

func TestParseArgs_ACPSessionIdleTTLDefault(t *testing.T) {
	args := ParseArgs([]string{"hello"})
	if args.ACPSessionIdleTTL != 15*time.Minute {
		t.Errorf("expected default ACPSessionIdleTTL=15m, got %v", args.ACPSessionIdleTTL)
	}
}

func TestParseArgs_ACPSessionIdleTTL(t *testing.T) {
	args := ParseArgs([]string{"--acp-session-idle-ttl", "30m", "hello"})
	if args.ACPSessionIdleTTL != 30*time.Minute {
		t.Errorf("expected ACPSessionIdleTTL=30m, got %v", args.ACPSessionIdleTTL)
	}
	if !args.Seen["--acp-session-idle-ttl"] {
		t.Error("expected --acp-session-idle-ttl marked seen")
	}
}

func TestParseArgs_ACPSessionIdleTTLZeroDisables(t *testing.T) {
	args := ParseArgs([]string{"--acp-session-idle-ttl", "0"})
	if args.ACPSessionIdleTTL != 0 {
		t.Errorf("expected ACPSessionIdleTTL=0 (disabled), got %v", args.ACPSessionIdleTTL)
	}
}

func TestParseArgs_ACPSessionIdleTTLInvalidKeepsDefault(t *testing.T) {
	args := ParseArgs([]string{"--acp-session-idle-ttl", "notaduration"})
	if args.ACPSessionIdleTTL != 15*time.Minute {
		t.Errorf("expected invalid value to keep default 15m, got %v", args.ACPSessionIdleTTL)
	}
}
