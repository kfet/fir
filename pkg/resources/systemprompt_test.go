package resources

import (
	"strings"
	"testing"
)

func TestBuildSystemPrompt_Default(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		Cwd: "/test/dir",
		ToolSnippets: map[string]string{
			"read":  "Read file contents",
			"bash":  "Execute bash commands",
			"edit":  "Make surgical edits",
			"write": "Create or overwrite files",
		},
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
	if !strings.Contains(prompt, "Current date:") {
		t.Error("should contain date")
	}
}

func TestBuildSystemPrompt_NoSnippets(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		Cwd: "/test/dir",
	})

	// Without ToolSnippets, tools section shows "(none)"
	if !strings.Contains(prompt, "(none)") {
		t.Error("should show (none) when no tool snippets provided")
	}
}

func TestBuildSystemPrompt_CustomTools(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		SelectedTools: []string{"read", "write"},
		Cwd:           "/test",
		ToolSnippets: map[string]string{
			"read":  "Read file contents",
			"write": "Create or overwrite files",
		},
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

func TestBuildSystemPrompt_CustomPromptIncludesSkills(t *testing.T) {
	// Skills must be appended even when a custom system prompt is used —
	// previously, agentsession.go replaced the full built prompt with the
	// custom one, silently dropping the skills section.
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		CustomPrompt: "You are a specialised agent.",
		Skills: []Skill{
			{Name: "my-skill", Description: "A test skill", FilePath: "/skills/my-skill/SKILL.md"},
		},
		Cwd: "/test",
	})

	if !strings.Contains(prompt, "You are a specialised agent.") {
		t.Error("should contain custom prompt text")
	}
	if !strings.Contains(prompt, "<available_skills>") {
		t.Error("custom prompt should still include skills section")
	}
	if !strings.Contains(prompt, "my-skill") {
		t.Error("custom prompt should still list skill name")
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

	// With only bash
	prompt2 := BuildSystemPrompt(BuildSystemPromptOptions{
		SelectedTools: []string{"bash"},
		Cwd:           "/test",
	})
	if !strings.Contains(prompt2, "Use bash for file operations") {
		t.Error("should suggest bash for file ops when it's the only option")
	}
}

func TestBuildSystemPrompt_EmptySelectedToolsIncludesSkills(t *testing.T) {
	// When SelectedTools is empty (no explicit selection), all tools are
	// available — including read. Skills should still appear.
	prompt := BuildSystemPrompt(BuildSystemPromptOptions{
		Skills: []Skill{
			{Name: "my-skill", Description: "A test skill", FilePath: "/skills/my-skill/SKILL.md"},
		},
		Cwd: "/test",
	})

	if !strings.Contains(prompt, "<available_skills>") {
		t.Error("empty SelectedTools should still include skills section")
	}
	if !strings.Contains(prompt, "my-skill") {
		t.Error("empty SelectedTools should still list skill names")
	}
}
