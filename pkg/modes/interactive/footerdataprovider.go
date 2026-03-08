// Ported from: packages/coding-agent/src/core/footer-data-provider.ts
// Upstream hash: 1caadb2e
package interactive

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FooterDataProvider provides git branch and extension statuses for the footer.
// Token stats and model info are available via SessionManager and Model.
type FooterDataProvider struct {
	mu                     sync.RWMutex
	extensionStatuses      map[string]string
	cachedBranch           *string // nil = not yet computed, pointer to "" = computed but not in repo
	branchChangeCallbacks  []func()
	availableProviderCount int
	cwd                    string
	stopWatcher            func()
}

// NewFooterDataProvider creates a new FooterDataProvider.
func NewFooterDataProvider(cwd string) *FooterDataProvider {
	f := &FooterDataProvider{
		extensionStatuses: make(map[string]string),
		cwd:               cwd,
	}
	f.startGitWatcher()
	return f
}

// startGitWatcher polls the git HEAD file for changes.
func (f *FooterDataProvider) startGitWatcher() {
	headPath := findGitHeadPath(f.cwd)
	if headPath == "" {
		return
	}

	stop := make(chan struct{})
	f.stopWatcher = func() { close(stop) }

	go func() {
		var lastMod time.Time
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				info, err := os.Stat(headPath)
				if err != nil {
					continue
				}
				mod := info.ModTime()
				if !lastMod.IsZero() && mod != lastMod {
					f.InvalidateBranchCache()
				}
				lastMod = mod
			}
		}
	}()
}

// GetGitBranch returns the current git branch name.
// Returns "" if not in a git repo, "detached" if HEAD is detached.
func (f *FooterDataProvider) GetGitBranch() string {
	f.mu.RLock()
	if f.cachedBranch != nil {
		branch := *f.cachedBranch
		f.mu.RUnlock()
		return branch
	}
	f.mu.RUnlock()

	branch := f.readGitBranch()

	f.mu.Lock()
	f.cachedBranch = &branch
	f.mu.Unlock()

	return branch
}

// readGitBranch reads the git branch from the .git/HEAD file.
func (f *FooterDataProvider) readGitBranch() string {
	headPath := findGitHeadPath(f.cwd)
	if headPath == "" {
		return ""
	}

	data, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}

	content := strings.TrimSpace(string(data))
	if strings.HasPrefix(content, "ref: refs/heads/") {
		return content[len("ref: refs/heads/"):]
	}
	return "detached"
}

// GetExtensionStatuses returns a copy of extension status texts.
func (f *FooterDataProvider) GetExtensionStatuses() map[string]string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	result := make(map[string]string, len(f.extensionStatuses))
	for k, v := range f.extensionStatuses {
		result[k] = v
	}
	return result
}

// OnBranchChange subscribes to git branch changes. Returns an unsubscribe function.
func (f *FooterDataProvider) OnBranchChange(callback func()) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branchChangeCallbacks = append(f.branchChangeCallbacks, callback)
	idx := len(f.branchChangeCallbacks) - 1
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if idx < len(f.branchChangeCallbacks) {
			f.branchChangeCallbacks[idx] = nil
		}
	}
}

// SetExtensionStatus sets or clears an extension status.
func (f *FooterDataProvider) SetExtensionStatus(key, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if text == "" {
		delete(f.extensionStatuses, key)
	} else {
		f.extensionStatuses[key] = text
	}
}

// ClearExtensionStatuses removes all extension statuses.
func (f *FooterDataProvider) ClearExtensionStatuses() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extensionStatuses = make(map[string]string)
}

// GetAvailableProviderCount returns the number of providers with available models.
func (f *FooterDataProvider) GetAvailableProviderCount() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.availableProviderCount
}

// SetAvailableProviderCount updates the available provider count.
func (f *FooterDataProvider) SetAvailableProviderCount(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.availableProviderCount = count
}

// InvalidateBranchCache clears the cached branch so it's re-read on next access.
func (f *FooterDataProvider) InvalidateBranchCache() {
	f.mu.Lock()
	f.cachedBranch = nil
	callbacks := make([]func(), 0)
	for _, cb := range f.branchChangeCallbacks {
		if cb != nil {
			callbacks = append(callbacks, cb)
		}
	}
	f.mu.Unlock()

	for _, cb := range callbacks {
		cb()
	}
}

// Dispose cleans up resources (stops the git watcher if running).
func (f *FooterDataProvider) Dispose() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopWatcher != nil {
		f.stopWatcher()
		f.stopWatcher = nil
	}
	f.branchChangeCallbacks = nil
}

// findGitHeadPath finds the git HEAD file by walking up from the given directory.
// Handles both regular git repos (.git is a directory) and worktrees (.git is a file).
func findGitHeadPath(cwd string) string {
	dir := cwd
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				headPath := filepath.Join(gitPath, "HEAD")
				if _, err := os.Stat(headPath); err == nil {
					return headPath
				}
			} else {
				// .git is a file (worktree)
				data, err := os.ReadFile(gitPath)
				if err == nil {
					content := strings.TrimSpace(string(data))
					if strings.HasPrefix(content, "gitdir: ") {
						gitDir := content[len("gitdir: "):]
						if !filepath.IsAbs(gitDir) {
							gitDir = filepath.Join(dir, gitDir)
						}
						headPath := filepath.Join(gitDir, "HEAD")
						if _, err := os.Stat(headPath); err == nil {
							return headPath
						}
					}
				}
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
