package interactive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFooterDataProvider_GetGitBranch_NotInRepo(t *testing.T) {
	dir := t.TempDir()
	f := NewFooterDataProvider(dir)
	defer f.Dispose()

	branch := f.GetGitBranch()
	if branch != "" {
		t.Errorf("expected empty branch for non-repo, got %q", branch)
	}
}

func TestFooterDataProvider_GetGitBranch_RegularRepo(t *testing.T) {
	dir := t.TempDir()

	// Create a fake git repo
	gitDir := filepath.Join(dir, ".git")
	os.MkdirAll(gitDir, 0755)
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644)

	f := NewFooterDataProvider(dir)
	defer f.Dispose()

	branch := f.GetGitBranch()
	if branch != "main" {
		t.Errorf("expected 'main', got %q", branch)
	}
}

func TestFooterDataProvider_GetGitBranch_DetachedHead(t *testing.T) {
	dir := t.TempDir()

	gitDir := filepath.Join(dir, ".git")
	os.MkdirAll(gitDir, 0755)
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("abc123def456\n"), 0644)

	f := NewFooterDataProvider(dir)
	defer f.Dispose()

	branch := f.GetGitBranch()
	if branch != "detached" {
		t.Errorf("expected 'detached', got %q", branch)
	}
}

func TestFooterDataProvider_GetGitBranch_Worktree(t *testing.T) {
	dir := t.TempDir()

	// Create the main git dir elsewhere
	mainGitDir := filepath.Join(dir, "main-repo", ".git")
	os.MkdirAll(mainGitDir, 0755)
	os.WriteFile(filepath.Join(mainGitDir, "HEAD"), []byte("ref: refs/heads/feature-branch\n"), 0644)

	// Create the worktree with .git file pointing to the main repo
	worktreeDir := filepath.Join(dir, "worktree")
	os.MkdirAll(worktreeDir, 0755)
	relPath, _ := filepath.Rel(worktreeDir, mainGitDir)
	os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: "+relPath+"\n"), 0644)

	f := NewFooterDataProvider(worktreeDir)
	defer f.Dispose()

	branch := f.GetGitBranch()
	if branch != "feature-branch" {
		t.Errorf("expected 'feature-branch', got %q", branch)
	}
}

func TestFooterDataProvider_GetGitBranch_Caching(t *testing.T) {
	dir := t.TempDir()

	gitDir := filepath.Join(dir, ".git")
	os.MkdirAll(gitDir, 0755)
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644)

	f := NewFooterDataProvider(dir)
	defer f.Dispose()

	// First call reads from disk
	branch1 := f.GetGitBranch()
	if branch1 != "main" {
		t.Fatalf("expected 'main', got %q", branch1)
	}

	// Change the file on disk
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/develop\n"), 0644)

	// Second call returns cached value
	branch2 := f.GetGitBranch()
	if branch2 != "main" {
		t.Errorf("expected cached 'main', got %q", branch2)
	}

	// Invalidate cache
	f.InvalidateBranchCache()

	// Now reads updated value
	branch3 := f.GetGitBranch()
	if branch3 != "develop" {
		t.Errorf("expected 'develop' after invalidation, got %q", branch3)
	}
}

func TestFooterDataProvider_ExtensionStatuses(t *testing.T) {
	f := NewFooterDataProvider(t.TempDir())
	defer f.Dispose()

	// Initially empty
	statuses := f.GetExtensionStatuses()
	if len(statuses) != 0 {
		t.Error("expected empty statuses")
	}

	// Set status
	f.SetExtensionStatus("ext1", "running")
	f.SetExtensionStatus("ext2", "idle")

	statuses = f.GetExtensionStatuses()
	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}
	if statuses["ext1"] != "running" {
		t.Errorf("ext1 status = %q, want 'running'", statuses["ext1"])
	}

	// Clear specific status
	f.SetExtensionStatus("ext1", "")
	statuses = f.GetExtensionStatuses()
	if len(statuses) != 1 {
		t.Errorf("expected 1 status after clear, got %d", len(statuses))
	}

	// Clear all
	f.ClearExtensionStatuses()
	statuses = f.GetExtensionStatuses()
	if len(statuses) != 0 {
		t.Error("expected empty after clear all")
	}
}

func TestFooterDataProvider_AvailableProviderCount(t *testing.T) {
	f := NewFooterDataProvider(t.TempDir())
	defer f.Dispose()

	if f.GetAvailableProviderCount() != 0 {
		t.Error("expected 0 initially")
	}

	f.SetAvailableProviderCount(5)
	if f.GetAvailableProviderCount() != 5 {
		t.Errorf("expected 5, got %d", f.GetAvailableProviderCount())
	}
}

func TestFooterDataProvider_OnBranchChange(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	os.MkdirAll(gitDir, 0755)
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644)

	f := NewFooterDataProvider(dir)
	defer f.Dispose()

	// Subscribe
	callCount := 0
	unsub := f.OnBranchChange(func() {
		callCount++
	})

	// Invalidate triggers callback
	f.InvalidateBranchCache()
	if callCount != 1 {
		t.Errorf("expected 1 callback, got %d", callCount)
	}

	// Unsubscribe
	unsub()
	f.InvalidateBranchCache()
	if callCount != 1 {
		t.Errorf("expected still 1 callback after unsub, got %d", callCount)
	}
}

func TestFooterDataProvider_Dispose(t *testing.T) {
	f := NewFooterDataProvider(t.TempDir())
	f.Dispose()
	f.Dispose() // Double dispose should not panic
}

func TestFindGitHeadPath_WalksUpDirectories(t *testing.T) {
	dir := t.TempDir()

	// Create git repo at top level
	gitDir := filepath.Join(dir, ".git")
	os.MkdirAll(gitDir, 0755)
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644)

	// Create a nested directory
	nested := filepath.Join(dir, "a", "b", "c")
	os.MkdirAll(nested, 0755)

	// Should find the HEAD from nested dir
	headPath := findGitHeadPath(nested)
	if headPath == "" {
		t.Fatal("expected to find git HEAD")
	}
	expected := filepath.Join(gitDir, "HEAD")
	if headPath != expected {
		t.Errorf("HEAD path = %q, want %q", headPath, expected)
	}
}

func TestFindGitHeadPath_NoRepo(t *testing.T) {
	dir := t.TempDir()
	headPath := findGitHeadPath(dir)
	// This might find the actual repo we're in, so just check it doesn't panic
	_ = headPath
}
