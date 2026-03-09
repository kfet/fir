package extension

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchAndReload watches the extension directories for file changes and
// triggers a reload when an extension file is created, modified, or removed.
// Changes are debounced by 500ms. The returned stop function cancels the watcher.
//
// onReload is called after each successful reload (may be nil). It receives
// the error from Reload (nil on success).
func (m *Manager) WatchAndReload(ctx context.Context, onReload func(error)) (stop func(), err error) {
	m.mu.Lock()
	projectDir := m.projectDir
	m.mu.Unlock()

	if projectDir == "" {
		return func() {}, nil
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Collect directories to watch.
	dirs := extensionDirs(projectDir)
	watched := 0
	for _, d := range dirs {
		if err := watcher.Add(d); err != nil {
			m.logger.Debug("cannot watch extension dir", "dir", d, "err", err)
			continue
		}
		watched++
		// Also watch immediate subdirectories (sub-directory extensions).
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if e.IsDir() {
				sub := filepath.Join(d, e.Name())
				_ = watcher.Add(sub)
			}
		}
	}

	if watched == 0 {
		_ = watcher.Close()
		return func() {}, nil
	}

	watchCtx, cancel := context.WithCancel(ctx)
	go m.watchLoop(watchCtx, watcher, onReload)

	return func() {
		cancel()
		_ = watcher.Close()
	}, nil
}

// extensionDirs returns the directories to watch for extension changes.
func extensionDirs(projectDir string) []string {
	var dirs []string

	homeDir, err := os.UserHomeDir()
	if err == nil {
		dirs = append(dirs, filepath.Join(homeDir, ".config", "fir", "extensions"))
	}

	dirs = append(dirs, filepath.Join(projectDir, ".fir", "extensions"))
	return dirs
}

func (m *Manager) watchLoop(ctx context.Context, watcher *fsnotify.Watcher, onReload func(error)) {
	var debounce *time.Timer
	for {
		select {
		case <-ctx.Done():
			if debounce != nil {
				debounce.Stop()
			}
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Ignore __pycache__ and hidden files.
			base := filepath.Base(event.Name)
			if base == "__pycache__" || (len(base) > 0 && base[0] == '.') {
				continue
			}
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) &&
				!event.Has(fsnotify.Remove) && !event.Has(fsnotify.Rename) {
				continue
			}
			// Capture the filename for the debounced callback.
			changedFile := event.Name
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(500*time.Millisecond, func() {
				if ctx.Err() != nil {
					return
				}
				m.logger.Info("extension file changed, reloading", "file", changedFile)
				// Use a detached context for reload so that stopping the
				// watcher doesn't cancel in-flight extension startups.
				err := m.Reload(context.Background())
				if onReload != nil {
					onReload(err)
				}
			})
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return
			}
			m.logger.Warn("extension watcher error", "err", watchErr)
		}
	}
}

// EnsureExtensionDirs creates extension directories if they don't exist,
// so the watcher has something to watch.
func EnsureExtensionDirs(projectDir string) {
	for _, d := range extensionDirs(projectDir) {
		_ = os.MkdirAll(d, 0755)
	}
}
