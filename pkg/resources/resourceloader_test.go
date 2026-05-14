package resources

import (
	"github.com/kfet/fir/pkg/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// Helper: create temp dir structure
// ============================================================================

func setupResourceDir(t *testing.T) (cwd string, agentDir string) {
	t.Helper()
	cwd = t.TempDir()
	agentDir = t.TempDir()
	return
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ============================================================================
// resolveResourcePath
// ============================================================================

func TestResolveResourcePath_Absolute(t *testing.T) {
	result := resolveResourcePath("/some/cwd", "/absolute/path")
	if result != "/absolute/path" {
		t.Errorf("expected /absolute/path, got %s", result)
	}
}

func TestResolveResourcePath_Relative(t *testing.T) {
	result := resolveResourcePath("/some/cwd", "relative/path")
	expected := filepath.Join("/some/cwd", "relative/path")
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestResolveResourcePath_TildeExpansion(t *testing.T) {
	home, _ := os.UserHomeDir()

	result := resolveResourcePath("/cwd", "~/foo")
	expected := filepath.Join(home, "foo")
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}

	result2 := resolveResourcePath("/cwd", "~")
	if result2 != home {
		t.Errorf("expected %s, got %s", home, result2)
	}
}

func TestResolveResourcePath_Trimmed(t *testing.T) {
	result := resolveResourcePath("/cwd", "  /abs/path  ")
	if result != "/abs/path" {
		t.Errorf("expected /abs/path, got %s", result)
	}
}

// ============================================================================
// isUnderPath
// ============================================================================

func TestIsUnderPath_Exact(t *testing.T) {
	if !isUnderPath("/a/b/c", "/a/b/c") {
		t.Error("expected true for exact match")
	}
}

func TestIsUnderPath_Child(t *testing.T) {
	if !isUnderPath("/a/b/c/d", "/a/b/c") {
		t.Error("expected true for child path")
	}
}

func TestIsUnderPath_NotChild(t *testing.T) {
	if isUnderPath("/a/b/candy", "/a/b/c") {
		t.Error("expected false for path with prefix but not child")
	}
}

func TestIsUnderPath_Unrelated(t *testing.T) {
	if isUnderPath("/x/y/z", "/a/b/c") {
		t.Error("expected false for unrelated path")
	}
}

// ============================================================================
// mergePaths
// ============================================================================

func TestMergePaths_Deduplication(t *testing.T) {
	cwd := "/test"
	result := mergePaths(cwd, []string{"/a", "/b"}, []string{"/b", "/c"})
	if len(result) != 3 {
		t.Errorf("expected 3 paths, got %d: %v", len(result), result)
	}
}

func TestMergePaths_ResolveRelative(t *testing.T) {
	cwd := "/test"
	result := mergePaths(cwd, []string{"rel"}, nil)
	expected := filepath.Join("/test", "rel")
	if len(result) != 1 || result[0] != expected {
		t.Errorf("expected [%s], got %v", expected, result)
	}
}

func TestMergePaths_Empty(t *testing.T) {
	result := mergePaths("/cwd", nil, nil)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

// ============================================================================
// resolvePromptInput
// ============================================================================

func TestResolvePromptInput_Empty(t *testing.T) {
	if resolvePromptInput("") != "" {
		t.Error("expected empty string")
	}
}

func TestResolvePromptInput_LiteralString(t *testing.T) {
	result := resolvePromptInput("You are a helpful assistant")
	if result != "You are a helpful assistant" {
		t.Errorf("expected literal string, got %s", result)
	}
}

func TestResolvePromptInput_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.md")
	writeFile(t, path, "System prompt from file")

	result := resolvePromptInput(path)
	if result != "System prompt from file" {
		t.Errorf("expected file content, got %s", result)
	}
}

// ============================================================================
// defaultSkillPaths
// ============================================================================

func TestDefaultSkillPaths_ExistingDirs(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	globalSkills := filepath.Join(agentDir, "skills")
	projectSkills := filepath.Join(cwd, config.ConfigDirName, "skills")
	os.MkdirAll(globalSkills, 0o755)
	os.MkdirAll(projectSkills, 0o755)

	paths := defaultSkillPaths(cwd, agentDir)
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d: %v", len(paths), paths)
	}
	if paths[0] != globalSkills {
		t.Errorf("expected global path %s, got %s", globalSkills, paths[0])
	}
	if paths[1] != projectSkills {
		t.Errorf("expected project path %s, got %s", projectSkills, paths[1])
	}
}

func TestDefaultSkillPaths_NoDirs(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	paths := defaultSkillPaths(cwd, agentDir)
	if len(paths) != 0 {
		t.Errorf("expected 0 paths, got %d", len(paths))
	}
}

// ============================================================================
// loadProjectContextFiles
// ============================================================================

func TestLoadProjectContextFiles_NoFiles(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	files := loadProjectContextFiles(cwd, agentDir)
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestLoadProjectContextFiles_AgentsMd(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), "# Project agents")
	files := loadProjectContextFiles(cwd, agentDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Content != "# Project agents" {
		t.Errorf("unexpected content: %s", files[0].Content)
	}
}

func TestLoadProjectContextFiles_ClaudeMd(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "# Claude config")
	files := loadProjectContextFiles(cwd, agentDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Content != "# Claude config" {
		t.Errorf("unexpected content: %s", files[0].Content)
	}
}

func TestLoadProjectContextFiles_AgentsPreferredOverClaude(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), "agents content")
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "claude content")

	files := loadProjectContextFiles(cwd, agentDir)
	// Only AGENTS.md should be loaded (it's checked first)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Content != "agents content" {
		t.Errorf("expected AGENTS.md content, got %s", files[0].Content)
	}
}

func TestLoadProjectContextFiles_GlobalAndProject(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	writeFile(t, filepath.Join(agentDir, "AGENTS.md"), "global agents")
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), "project agents")

	files := loadProjectContextFiles(cwd, agentDir)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
}

func TestLoadProjectContextFiles_AncestorDirs(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	subDir := filepath.Join(cwd, "a", "b")
	os.MkdirAll(subDir, 0o755)
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), "root agents")

	files := loadProjectContextFiles(subDir, agentDir)
	found := false
	for _, f := range files {
		if strings.Contains(f.Content, "root agents") {
			found = true
		}
	}
	if !found {
		t.Error("expected to find ancestor AGENTS.md")
	}
}

// ============================================================================
// NewResourceLoader
// ============================================================================

func TestNewResourceLoader_Defaults(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	loader := NewResourceLoader(ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})

	if loader.cwd != cwd {
		t.Errorf("expected cwd %s, got %s", cwd, loader.cwd)
	}
	if loader.agentDir != agentDir {
		t.Errorf("expected agentDir %s, got %s", agentDir, loader.agentDir)
	}
}

func TestNewResourceLoader_DefaultCwd(t *testing.T) {
	loader := NewResourceLoader(ResourceLoaderOptions{
		AgentDir: "/tmp/agent",
	})
	wd, _ := os.Getwd()
	if loader.cwd != wd {
		t.Errorf("expected cwd %s, got %s", wd, loader.cwd)
	}
}

// ============================================================================
// Reload
// ============================================================================

func TestReload_Empty(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	loader := NewResourceLoader(ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})

	if err := loader.Reload(); err != nil {
		t.Fatal(err)
	}

	builtinCount := len(LoadBuiltinSkills().Skills)
	skills, diags := loader.GetSkills()
	if len(skills) != builtinCount {
		t.Errorf("expected %d skills (builtins only), got %d", builtinCount, len(skills))
	}
	if len(diags) != 0 {
		t.Errorf("expected 0 diags, got %d", len(diags))
	}

	if len(loader.GetAgentsFiles()) != 0 {
		t.Error("expected 0 agents files")
	}
}

func TestReload_WithSkills(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	// Skills in subdirectories need SKILL.md; root-level .md files are also loaded
	skillDir := filepath.Join(cwd, config.ConfigDirName, "skills", "test-skill")
	os.MkdirAll(skillDir, 0o755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), `---
description: A test skill
---
Skill content here`)

	loader := NewResourceLoader(ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})

	if err := loader.Reload(); err != nil {
		t.Fatal(err)
	}

	builtinCount := len(LoadBuiltinSkills().Skills)
	skills, _ := loader.GetSkills()
	if len(skills) != builtinCount+1 {
		t.Fatalf("expected %d skills (1 + %d builtins), got %d", builtinCount+1, builtinCount, len(skills))
	}
	// Find the test skill
	var found bool
	for _, s := range skills {
		if s.Name == "test-skill" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find test-skill in loaded skills")
	}
}

func TestReload_WithAgentsFiles(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), "# Test agents")

	loader := NewResourceLoader(ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	loader.Reload()

	files := loader.GetAgentsFiles()
	if len(files) != 1 {
		t.Fatalf("expected 1 agents file, got %d", len(files))
	}
	if files[0].Content != "# Test agents" {
		t.Errorf("unexpected content: %s", files[0].Content)
	}
}

func TestReload_WithSystemPrompt(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	loader := NewResourceLoader(ResourceLoaderOptions{
		Cwd:          cwd,
		AgentDir:     agentDir,
		SystemPrompt: "You are a helpful bot",
	})
	loader.Reload()

	if loader.GetSystemPrompt() != "You are a helpful bot" {
		t.Errorf("expected system prompt, got %s", loader.GetSystemPrompt())
	}
}

func TestReload_WithSystemPromptFile(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	promptDir := filepath.Join(cwd, config.ConfigDirName)
	os.MkdirAll(promptDir, 0o755)
	writeFile(t, filepath.Join(promptDir, "SYSTEM.md"), "System prompt from file")

	loader := NewResourceLoader(ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	loader.Reload()

	if loader.GetSystemPrompt() != "System prompt from file" {
		t.Errorf("expected system prompt from file, got %s", loader.GetSystemPrompt())
	}
}

func TestReload_WithAppendSystemPrompt(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	loader := NewResourceLoader(ResourceLoaderOptions{
		Cwd:                cwd,
		AgentDir:           agentDir,
		AppendSystemPrompt: "Additional context",
	})
	loader.Reload()

	appended := loader.GetAppendSystemPrompt()
	if len(appended) != 1 || appended[0] != "Additional context" {
		t.Errorf("expected append system prompt, got %v", appended)
	}
}

func TestReload_NoSkills(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	skillDir := filepath.Join(cwd, config.ConfigDirName, "skills", "test")
	os.MkdirAll(skillDir, 0o755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), `---
description: should be skipped
---
Content`)

	loader := NewResourceLoader(ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
		NoSkills: true,
	})
	loader.Reload()

	skills, _ := loader.GetSkills()
	if len(skills) != 0 {
		t.Errorf("expected 0 skills with NoSkills=true, got %d", len(skills))
	}
}

// ============================================================================
// ExtendResources
// ============================================================================

func TestExtendResources_SkillPaths(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	extBaseDir := t.TempDir()
	extSkillDir := filepath.Join(extBaseDir, "ext-skill")
	os.MkdirAll(extSkillDir, 0o755)
	writeFile(t, filepath.Join(extSkillDir, "SKILL.md"), `---
description: Extension skill
---
Content`)

	loader := NewResourceLoader(ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	loader.Reload()

	// Pass the parent dir containing the ext-skill subdirectory
	loader.ExtendResources(ResourceExtensionPaths{
		SkillPaths: []PathEntry{
			{Path: extBaseDir, Metadata: PathMetadata{Source: "local", Scope: "user", Origin: "package"}},
		},
	})

	skills, _ := loader.GetSkills()
	found := false
	for _, s := range skills {
		if s.Name == "ext-skill" {
			found = true
		}
	}
	if !found {
		names := make([]string, len(skills))
		for i, s := range skills {
			names[i] = s.Name
		}
		t.Errorf("expected extension skill to be loaded, got skills: %v", names)
	}
}

// ============================================================================
// PathMetadata
// ============================================================================

func TestPathMetadata_DefaultForSkills(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	skillDir := filepath.Join(cwd, config.ConfigDirName, "skills", "my-skill")
	os.MkdirAll(skillDir, 0o755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), `---
description: test
---
body`)

	loader := NewResourceLoader(ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	loader.Reload()

	metadata := loader.GetPathMetadata()
	// Should have metadata for the loaded skill
	found := false
	for _, meta := range metadata {
		if meta.Source == "local" && meta.Scope == "project" {
			found = true
		}
	}
	if !found {
		t.Error("expected project-scope metadata for local skill")
	}
}

func TestPathMetadata_GlobalSkills(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	skillDir := filepath.Join(agentDir, "skills", "global-skill")
	os.MkdirAll(skillDir, 0o755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), `---
description: global skill
---
body`)

	loader := NewResourceLoader(ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	loader.Reload()

	metadata := loader.GetPathMetadata()
	found := false
	for _, meta := range metadata {
		if meta.Source == "local" && meta.Scope == "user" {
			found = true
		}
	}
	if !found {
		t.Error("expected user-scope metadata for global skill")
	}
}

// ============================================================================
// fileExists / dirExists
// ============================================================================

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	writeFile(t, f, "hello")

	if !fileExists(f) {
		t.Error("expected file to exist")
	}
	if fileExists(filepath.Join(dir, "nope.txt")) {
		t.Error("expected false for nonexistent file")
	}
	if fileExists(dir) {
		t.Error("expected false for directory")
	}
}

func TestDirExists(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	writeFile(t, f, "hello")

	if !dirExists(dir) {
		t.Error("expected dir to exist")
	}
	if dirExists(f) {
		t.Error("expected false for file")
	}
	if dirExists(filepath.Join(dir, "nope")) {
		t.Error("expected false for nonexistent dir")
	}
}

// ============================================================================
// ResourceLoader interface compliance
// ============================================================================

func TestResourceLoaderInterface(t *testing.T) {
	var _ ResourceLoader = &DefaultResourceLoader{}
}

// ============================================================================
// loadContextFileFromDir
// ============================================================================

func TestLoadContextFileFromDir_PreferAgents(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "agents")
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "claude")

	f := loadContextFileFromDir(dir, []string{"AGENTS.md", "CLAUDE.md"})
	if f == nil {
		t.Fatal("expected to find file")
	}
	if f.Content != "agents" {
		t.Errorf("expected AGENTS.md to be preferred, got %s", f.Content)
	}
}

func TestLoadContextFileFromDir_FallbackClaude(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "claude")

	f := loadContextFileFromDir(dir, []string{"AGENTS.md", "CLAUDE.md"})
	if f == nil {
		t.Fatal("expected to find file")
	}
	if f.Content != "claude" {
		t.Errorf("expected CLAUDE.md fallback, got %s", f.Content)
	}
}

func TestLoadContextFileFromDir_None(t *testing.T) {
	dir := t.TempDir()
	f := loadContextFileFromDir(dir, []string{"AGENTS.md", "CLAUDE.md"})
	if f != nil {
		t.Error("expected nil when no files exist")
	}
}

// ============================================================================
// discoverSystemPromptFile
// ============================================================================

func TestDiscoverSystemPromptFile_Project(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	promptPath := filepath.Join(cwd, config.ConfigDirName, "SYSTEM.md")
	writeFile(t, promptPath, "system prompt")

	loader := NewResourceLoader(ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	result := loader.discoverSystemPromptFile()
	if result != promptPath {
		t.Errorf("expected %s, got %s", promptPath, result)
	}
}

func TestDiscoverSystemPromptFile_Global(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	promptPath := filepath.Join(agentDir, "SYSTEM.md")
	writeFile(t, promptPath, "global system prompt")

	loader := NewResourceLoader(ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	result := loader.discoverSystemPromptFile()
	if result != promptPath {
		t.Errorf("expected %s, got %s", promptPath, result)
	}
}

func TestDiscoverSystemPromptFile_ProjectPreferred(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	projectPath := filepath.Join(cwd, config.ConfigDirName, "SYSTEM.md")
	globalPath := filepath.Join(agentDir, "SYSTEM.md")
	writeFile(t, projectPath, "project")
	writeFile(t, globalPath, "global")

	loader := NewResourceLoader(ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	result := loader.discoverSystemPromptFile()
	if result != projectPath {
		t.Errorf("expected project path %s, got %s", projectPath, result)
	}
}

func TestDiscoverSystemPromptFile_None(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	loader := NewResourceLoader(ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	result := loader.discoverSystemPromptFile()
	if result != "" {
		t.Errorf("expected empty, got %s", result)
	}
}

func TestDiscoverAppendSystemPromptFile(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	promptPath := filepath.Join(cwd, config.ConfigDirName, "APPEND_SYSTEM.md")
	writeFile(t, promptPath, "append content")

	loader := NewResourceLoader(ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	result := loader.discoverAppendSystemPromptFile()
	if result != promptPath {
		t.Errorf("expected %s, got %s", promptPath, result)
	}
}

// ============================================================================
// Reload idempotence
// ============================================================================

func TestReload_Idempotent(t *testing.T) {
	cwd, agentDir := setupResourceDir(t)
	skillDir := filepath.Join(cwd, config.ConfigDirName, "skills", "s")
	os.MkdirAll(skillDir, 0o755)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), `---
description: skill
---
body`)

	loader := NewResourceLoader(ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	loader.Reload()
	skills1, _ := loader.GetSkills()

	loader.Reload()
	skills2, _ := loader.GetSkills()

	if len(skills1) != len(skills2) {
		t.Errorf("reload not idempotent: %d vs %d skills", len(skills1), len(skills2))
	}
}
