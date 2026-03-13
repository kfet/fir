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

	// Watch extension directories. When an extensions dir doesn't exist yet,
	// watch its nearest existing ancestor so we detect when it gets created.
	dirs := extensionDirs(projectDir)
	watched := 0
	for _, d := range dirs {
		if err := watcher.Add(d); err != nil {
			// Directory doesn't exist — watch nearest existing ancestor.
			if parent := nearestExistingDir(d); parent != "" {
				if addErr := watcher.Add(parent); addErr == nil {
					watched++
					m.logger.Debug("watching parent for extension dir creation", "parent", parent, "target", d)
				}
			}
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
	go m.watchLoop(watchCtx, watcher, dirs, onReload)

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

// nearestExistingDir walks up from path until it finds a directory that exists.
// Returns "" if no ancestor exists (shouldn't happen in practice).
func nearestExistingDir(path string) string {
	for {
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		if info, err := os.Stat(parent); err == nil && info.IsDir() {
			return parent
		}
		path = parent
	}
}

// isParentOf reports whether parent is an ancestor directory of child.
func isParentOf(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	// rel must not start with ".." and must not be "." (same dir).
	return rel != "." && !filepath.IsAbs(rel) && (len(rel) < 2 || rel[:2] != "..")
}

func (m *Manager) watchLoop(ctx context.Context, watcher *fsnotify.Watcher, extDirs []string, onReload func(error)) {
	var debounce *time.Timer

	// targetSet for fast lookup of extension dirs we want to watch.
	targetSet := make(map[string]bool, len(extDirs))
	for _, d := range extDirs {
		targetSet[d] = true
	}

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

			// When a directory is created that matches one of our target
			// extension dirs (or is a parent on the way), start watching it.
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					if targetSet[event.Name] {
						// The actual extensions dir appeared — watch it.
						_ = watcher.Add(event.Name)
						m.logger.Info("extension directory created, now watching", "dir", event.Name)
					} else {
						// Could be a parent dir (e.g. .fir/ created, extensions/ coming next)
						// or a subdirectory extension. Watch it either way.
						for _, target := range extDirs {
							if isParentOf(event.Name, target) {
								_ = watcher.Add(event.Name)
								m.logger.Debug("watching new parent dir", "dir", event.Name, "target", target)
								break
							}
						}
						// Sub-directory extension inside an extensions dir.
						parent := filepath.Dir(event.Name)
						if targetSet[parent] {
							_ = watcher.Add(event.Name)
						}
					}
				}
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
