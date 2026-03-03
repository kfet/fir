package core

import (
	"strings"
	"testing"
)

func TestBuildSystemPrompt_Default(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		Cwd: "/test/dir",
	})

	if !strings.Contains(prompt, "expert coding assistant") {
		t.Error("should contain role description")
	}
	if !strings.Contains(prompt, "read: Read file contents") {
		t.Error("should list read tool")
	}
	if !strings.Contains(prompt, "bash: Execute bash") {
		t.Error("should list bash tool")
	}
	if !strings.Contains(prompt, "/test/dir") {
		t.Error("should contain cwd")
	}
	if !strings.Contains(prompt, "Current date and time:") {
		t.Error("should contain date/time")
	}
}

func TestBuildSystemPrompt_CustomTools(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		SelectedTools: []string{"read", "write"},
		Cwd:           "/test",
	})

	if !strings.Contains(prompt, "read: Read file contents") {
		t.Error("should list read")
	}
	if !strings.Contains(prompt, "write: Create or overwrite") {
		t.Error("should list write")
	}
	if strings.Contains(prompt, "bash: Execute bash") {
		t.Error("should NOT list bash")
	}
}

func TestBuildSystemPrompt_CustomPrompt(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		CustomPrompt: "You are a custom agent.",
		Cwd:          "/custom",
	})

	if !strings.Contains(prompt, "You are a custom agent.") {
		t.Error("should contain custom prompt")
	}
	if !strings.Contains(prompt, "/custom") {
		t.Error("should contain cwd")
	}
	// Should NOT contain default prompt content
	if strings.Contains(prompt, "expert coding assistant") {
		t.Error("should not contain default role")
	}
}

func TestBuildSystemPrompt_AppendPrompt(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		AppendSystemPrompt: "Extra instructions here.",
		Cwd:                "/test",
	})

	if !strings.Contains(prompt, "Extra instructions here.") {
		t.Error("should contain appended text")
	}
}

func TestBuildSystemPrompt_WithSkills(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		SelectedTools: []string{"read", "bash"},
		Skills: []Skill{
			{Name: "testing", Description: "Testing guidelines", FilePath: "/skills/testing/SKILL.md"},
		},
		Cwd: "/test",
	})

	if !strings.Contains(prompt, "<available_skills>") {
		t.Error("should contain skills section")
	}
	if !strings.Contains(prompt, "testing") {
		t.Error("should contain skill name")
	}
}

func TestBuildSystemPrompt_WithContextFiles(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		ContextFiles: []ContextFile{
			{Path: ".fir/prompts/context.md", Content: "Project-specific info"},
		},
		Cwd: "/test",
	})

	if !strings.Contains(prompt, "Project Context") {
		t.Error("should contain project context section")
	}
	if !strings.Contains(prompt, "Project-specific info") {
		t.Error("should contain context file content")
	}
}

func TestBuildSystemPrompt_Guidelines(t *testing.T) {
	// With all tools
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		SelectedTools: []string{"read", "bash", "edit", "write", "grep", "find", "ls"},
		Cwd:           "/test",
	})

	if !strings.Contains(prompt, "Prefer grep/find/ls") {
		t.Error("should suggest preferring grep/find/ls over bash")
	}
	if !strings.Contains(prompt, "Use read to examine files before editing") {
		t.Error("should suggest reading before editing")
	}

	// With only bash
	prompt2 := BuildSystemPrompt(BuildSystemPromptOptions{
		SelectedTools: []string{"bash"},
		Cwd:           "/test",
	})
	if !strings.Contains(prompt2, "Use bash for file operations") {
		t.Error("should suggest bash for file ops when it's the only option")
	}
}

func TestBuildSystemPrompt_ToolSnippets(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		SelectedTools: []string{"read", "bash", "my-custom-tool"},
		ToolSnippets: map[string]string{
			"my-custom-tool": "Does custom things",
		},
	})
	if !strings.Contains(prompt, "- my-custom-tool: Does custom things") {
		t.Error("expected custom tool snippet in prompt")
	}
	// Built-in tools should still use their default descriptions
	if !strings.Contains(prompt, "- read: ") {
		t.Error("expected read tool in prompt")
	}
}

func TestBuildSystemPrompt_ToolSnippets_OverridesBuiltin(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		SelectedTools: []string{"read"},
		ToolSnippets: map[string]string{
			"read": "Custom read description",
		},
	})
	if !strings.Contains(prompt, "- read: Custom read description") {
		t.Error("expected custom snippet to override built-in description")
	}
}

func TestBuildSystemPrompt_PromptGuidelines(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		SelectedTools:    []string{"bash"},
		PromptGuidelines: []string{"Always use JSON output", "Never delete files"},
	})
	if !strings.Contains(prompt, "- Always use JSON output") {
		t.Error("expected custom guideline in prompt")
	}
	if !strings.Contains(prompt, "- Never delete files") {
		t.Error("expected second custom guideline in prompt")
	}
}

func TestBuildSystemPrompt_PromptGuidelines_Dedup(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		SelectedTools:    []string{"bash"},
		PromptGuidelines: []string{"Be concise in your responses", "Be concise in your responses"},
	})
	// "Be concise in your responses" is a built-in guideline; duplicates should be removed
	count := strings.Count(prompt, "Be concise in your responses")
	if count != 1 {
		t.Errorf("expected 'Be concise' to appear exactly once, appeared %d times", count)
	}
}
