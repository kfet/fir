package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchConfig watches the config file at path for changes and calls onChange
// with the newly parsed ConfigFile on each change. Changes are debounced by
// 200ms so rapid edits (e.g. editor temp-file swaps) produce a single call.
//
// The parent directory is watched rather than the file itself so that atomic
// rename-based saves (vim, many IDEs) are detected correctly.
//
// Returns a stop function that terminates the watcher. The caller must call
// stop when done to avoid leaking goroutines and OS file-watch resources.
func WatchConfig(path string, onChange func(*ConfigFile)) (stop func(), err error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("mcp: create watcher: %w", err)
	}

	// Watching the directory is more robust than watching the file: editors
	// that save via atomic rename (write to tmp, then rename onto the target)
	// break a direct file watch because the inode changes.
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("mcp: watch %s: %w", dir, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer watcher.Close()
		var timer *time.Timer
		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// Filter to the specific file we care about.
				if filepath.Base(event.Name) != base {
					continue
				}
				if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) && !event.Has(fsnotify.Rename) {
					continue
				}
				// Debounce: reset the timer on each relevant event so that
				// a burst of writes results in a single onChange call.
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(200*time.Millisecond, func() {
					cfg, loadErr := LoadConfigFile(path)
					if loadErr != nil {
						slog.Warn("mcp: config reload error", "path", path, "err", loadErr)
						return
					}
					onChange(cfg)
				})
			case watchErr, ok := <-watcher.Errors:
				if !ok {
					return
				}
				slog.Warn("mcp: config watcher error", "path", path, "err", watchErr)
			}
		}
	}()

	return cancel, nil
}
