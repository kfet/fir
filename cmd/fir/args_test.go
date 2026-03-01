package main

import (
	"testing"

	"github.com/kfet/fir/pkg/agent"
)

func TestParseArgs_Empty(t *testing.T) {
	args := ParseArgs([]string{}, nil)
	if args.Help || args.Version || args.Print || args.Continue || args.Resume {
		t.Error("expected all flags false for empty args")
	}
	if len(args.Messages) != 0 {
		t.Error("expected no messages")
	}
}

func TestParseArgs_Help(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		args := ParseArgs([]string{flag}, nil)
		if !args.Help {
			t.Errorf("expected Help=true for %s", flag)
		}
	}
}

func TestParseArgs_Version(t *testing.T) {
	for _, flag := range []string{"--version", "-v"} {
		args := ParseArgs([]string{flag}, nil)
		if !args.Version {
			t.Errorf("expected Version=true for %s", flag)
		}
	}
}

func TestParseArgs_Print(t *testing.T) {
	for _, flag := range []string{"--print", "-p"} {
		args := ParseArgs([]string{flag}, nil)
		if !args.Print {
			t.Errorf("expected Print=true for %s", flag)
		}
	}
}

func TestParseArgs_Continue(t *testing.T) {
	for _, flag := range []string{"--continue", "-c"} {
		args := ParseArgs([]string{flag}, nil)
		if !args.Continue {
			t.Errorf("expected Continue=true for %s", flag)
		}
	}
}

func TestParseArgs_Resume(t *testing.T) {
	for _, flag := range []string{"--resume", "-r"} {
		args := ParseArgs([]string{flag}, nil)
		if !args.Resume {
			t.Errorf("expected Resume=true for %s", flag)
		}
	}
}

func TestParseArgs_Provider(t *testing.T) {
	args := ParseArgs([]string{"--provider", "anthropic"}, nil)
	if args.Provider != "anthropic" {
		t.Errorf("expected provider 'anthropic', got %q", args.Provider)
	}
}

func TestParseArgs_Model(t *testing.T) {
	args := ParseArgs([]string{"--model", "claude-sonnet-4-5"}, nil)
	if args.Model != "claude-sonnet-4-5" {
		t.Errorf("expected model 'claude-sonnet-4-5', got %q", args.Model)
	}
}

func TestParseArgs_ApiKey(t *testing.T) {
	args := ParseArgs([]string{"--api-key", "sk-test"}, nil)
	if args.ApiKey != "sk-test" {
		t.Errorf("expected apiKey 'sk-test', got %q", args.ApiKey)
	}
}

func TestParseArgs_SystemPrompt(t *testing.T) {
	args := ParseArgs([]string{"--system-prompt", "You are a helpful assistant"}, nil)
	if args.SystemPrompt != "You are a helpful assistant" {
		t.Errorf("unexpected system prompt: %q", args.SystemPrompt)
	}
}

func TestParseArgs_AppendSystemPrompt(t *testing.T) {
	args := ParseArgs([]string{"--append-system-prompt", "Extra instructions"}, nil)
	if args.AppendSystemPrompt != "Extra instructions" {
		t.Errorf("unexpected append system prompt: %q", args.AppendSystemPrompt)
	}
}

func TestParseArgs_Thinking(t *testing.T) {
	args := ParseArgs([]string{"--thinking", "high"}, nil)
	if args.Thinking != agent.ThinkingHigh {
		t.Errorf("expected thinking 'high', got %q", args.Thinking)
	}
}

func TestParseArgs_ThinkingInvalid(t *testing.T) {
	args := ParseArgs([]string{"--thinking", "invalid"}, nil)
	if args.Thinking != "" {
		t.Errorf("expected empty thinking for invalid value, got %q", args.Thinking)
	}
}

func TestParseArgs_Mode(t *testing.T) {
	for _, mode := range []string{"text", "json", "rpc"} {
		args := ParseArgs([]string{"--mode", mode}, nil)
		if string(args.OutputMode) != mode {
			t.Errorf("expected mode %q, got %q", mode, args.OutputMode)
		}
	}
}

func TestParseArgs_NoSession(t *testing.T) {
	args := ParseArgs([]string{"--no-session"}, nil)
	if !args.NoSession {
		t.Error("expected NoSession=true")
	}
}

func TestParseArgs_Session(t *testing.T) {
	args := ParseArgs([]string{"--session", "/path/to/session.jsonl"}, nil)
	if args.Session != "/path/to/session.jsonl" {
		t.Errorf("expected session path, got %q", args.Session)
	}
}

func TestParseArgs_SessionDir(t *testing.T) {
	args := ParseArgs([]string{"--session-dir", "/tmp/sessions"}, nil)
	if args.SessionDir != "/tmp/sessions" {
		t.Errorf("expected session dir, got %q", args.SessionDir)
	}
}

func TestParseArgs_Models(t *testing.T) {
	args := ParseArgs([]string{"--models", "sonnet,haiku,gpt-4o"}, nil)
	if len(args.Models) != 3 {
		t.Fatalf("expected 3 models, got %d: %v", len(args.Models), args.Models)
	}
	if args.Models[0] != "sonnet" || args.Models[1] != "haiku" || args.Models[2] != "gpt-4o" {
		t.Errorf("unexpected models: %v", args.Models)
	}
}

func TestParseArgs_ModelsWithSpaces(t *testing.T) {
	args := ParseArgs([]string{"--models", " sonnet , haiku "}, nil)
	if len(args.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %v", len(args.Models), args.Models)
	}
	if args.Models[0] != "sonnet" || args.Models[1] != "haiku" {
		t.Errorf("unexpected models: %v", args.Models)
	}
}

func TestParseArgs_NoTools(t *testing.T) {
	args := ParseArgs([]string{"--no-tools"}, nil)
	if !args.NoTools {
		t.Error("expected NoTools=true")
	}
}

func TestParseArgs_Tools(t *testing.T) {
	args := ParseArgs([]string{"--tools", "read,bash,edit"}, nil)
	if len(args.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(args.Tools))
	}
}

func TestParseArgs_Extensions(t *testing.T) {
	args := ParseArgs([]string{"-e", "ext1.js", "--extension", "ext2.js"}, nil)
	if len(args.Extensions) != 2 {
		t.Fatalf("expected 2 extensions, got %d", len(args.Extensions))
	}
	if args.Extensions[0] != "ext1.js" || args.Extensions[1] != "ext2.js" {
		t.Errorf("unexpected extensions: %v", args.Extensions)
	}
}

func TestParseArgs_NoExtensions(t *testing.T) {
	args := ParseArgs([]string{"--no-extensions"}, nil)
	if !args.NoExtensions {
		t.Error("expected NoExtensions=true")
	}
}

func TestParseArgs_Skills(t *testing.T) {
	args := ParseArgs([]string{"--skill", "skill1.md", "--skill", "skill2.md"}, nil)
	if len(args.Skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(args.Skills))
	}
}

func TestParseArgs_PromptTemplates(t *testing.T) {
	args := ParseArgs([]string{"--prompt-template", "tmpl.md"}, nil)
	if len(args.PromptTemplates) != 1 {
		t.Fatalf("expected 1 prompt template, got %d", len(args.PromptTemplates))
	}
}

func TestParseArgs_Themes(t *testing.T) {
	args := ParseArgs([]string{"--theme", "dark.json"}, nil)
	if len(args.Themes) != 1 {
		t.Fatalf("expected 1 theme, got %d", len(args.Themes))
	}
}

func TestParseArgs_NoSkills(t *testing.T) {
	args := ParseArgs([]string{"--no-skills"}, nil)
	if !args.NoSkills {
		t.Error("expected NoSkills=true")
	}
}

func TestParseArgs_NoPromptTemplates(t *testing.T) {
	args := ParseArgs([]string{"--no-prompt-templates"}, nil)
	if !args.NoPromptTemplates {
		t.Error("expected NoPromptTemplates=true")
	}
}

func TestParseArgs_NoThemes(t *testing.T) {
	args := ParseArgs([]string{"--no-themes"}, nil)
	if !args.NoThemes {
		t.Error("expected NoThemes=true")
	}
}

func TestParseArgs_Export(t *testing.T) {
	args := ParseArgs([]string{"--export", "session.jsonl"}, nil)
	if args.Export != "session.jsonl" {
		t.Errorf("expected export 'session.jsonl', got %q", args.Export)
	}
}

func TestParseArgs_ListModelsNoPattern(t *testing.T) {
	args := ParseArgs([]string{"--list-models"}, nil)
	if args.ListModels != true {
		t.Errorf("expected ListModels=true, got %v", args.ListModels)
	}
}

func TestParseArgs_ListModelsWithPattern(t *testing.T) {
	args := ParseArgs([]string{"--list-models", "sonnet"}, nil)
	if args.ListModels != "sonnet" {
		t.Errorf("expected ListModels='sonnet', got %v", args.ListModels)
	}
}

func TestParseArgs_ListModelsBeforeFlag(t *testing.T) {
	// --list-models followed by a flag should not consume the flag as pattern
	args := ParseArgs([]string{"--list-models", "--verbose"}, nil)
	if args.ListModels != true {
		t.Errorf("expected ListModels=true, got %v", args.ListModels)
	}
	if !args.Verbose {
		t.Error("expected Verbose=true")
	}
}

func TestParseArgs_Verbose(t *testing.T) {
	args := ParseArgs([]string{"--verbose"}, nil)
	if !args.Verbose {
		t.Error("expected Verbose=true")
	}
}

func TestParseArgs_Messages(t *testing.T) {
	args := ParseArgs([]string{"hello", "world"}, nil)
	if len(args.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(args.Messages))
	}
	if args.Messages[0] != "hello" || args.Messages[1] != "world" {
		t.Errorf("unexpected messages: %v", args.Messages)
	}
}

func TestParseArgs_FileArgs(t *testing.T) {
	args := ParseArgs([]string{"@file1.md", "@image.png"}, nil)
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
	}, nil)

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

func TestParseArgs_ExtensionFlags(t *testing.T) {
	extFlags := map[string]ExtensionFlagDef{
		"plan":   {Type: "boolean"},
		"format": {Type: "string"},
	}

	args := ParseArgs([]string{"--plan", "--format", "markdown"}, extFlags)
	if v, ok := args.UnknownFlags["plan"]; !ok || v != true {
		t.Error("expected plan flag to be true")
	}
	if v, ok := args.UnknownFlags["format"]; !ok || v != "markdown" {
		t.Errorf("expected format='markdown', got %v", v)
	}
}

func TestParseArgs_UnknownFlagsCaptured(t *testing.T) {
	// Unknown flags are captured even without extensionFlags, so extensions
	// can be set up after parsing and still receive their flag values.
	args := ParseArgs([]string{"--unknown-flag", "value"}, nil)
	if v, ok := args.UnknownFlags["unknown-flag"]; !ok || v != "value" {
		t.Errorf("expected unknown-flag=value, got %v", args.UnknownFlags)
	}

	// Boolean-style unknown flag (no value arg follows)
	args = ParseArgs([]string{"--no-network"}, nil)
	if v, ok := args.UnknownFlags["no-network"]; !ok || v != true {
		t.Errorf("expected no-network=true, got %v", args.UnknownFlags)
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
