package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StatePath returns the per-session settings file for a transcript:
// <session>.jsonl.state.json. It is a sibling of the transcript, not part of
// the .meta.json listing cache: that file is a 0644, mtime-invalidated cache
// that is rebuilt freely and doubles as the flock target, while the state file
// is authoritative, holds secrets (MCP env) and so is written 0600.
func StatePath(sessionFile string) string {
	if sessionFile == "" {
		return ""
	}
	return sessionFile + ".state.json"
}

// ReadState decodes the state file for sessionFile into v. It reports false
// when there is no file or it cannot be decoded.
func ReadState(sessionFile string, v any) bool {
	path := StatePath(sessionFile)
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}

// WriteState atomically writes v as the state file for sessionFile, mode 0600.
func WriteState(sessionFile string, v any) error {
	path := StatePath(sessionFile)
	if path == "" {
		return errors.New("no session file")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data, 0o600)
}

// WriteFileAtomic writes data to path via a temp file and rename, so readers
// never see a partial file. The file is created with perm.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// --- Handles ---
//
// A handle is an external, stable name for a session (e.g. an ACP sessionId)
// that must keep resolving after the process restarts or the session moves to
// another transcript (/new, /resume, a fork). Each handle is a tiny pointer
// file <agentDir>/session-handles/<sha256(handle)[:16]> holding the transcript
// path. All settings live in the transcript's state file.

func handlesDir(agentDir string) string {
	return filepath.Join(agentDir, "session-handles")
}

func handlePath(agentDir, handle string) string {
	if agentDir == "" || handle == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(handle))
	return filepath.Join(handlesDir(agentDir), hex.EncodeToString(sum[:16]))
}

// BindHandle points handle at sessionFile.
func BindHandle(agentDir, handle, sessionFile string) error {
	path := handlePath(agentDir, handle)
	if path == "" || sessionFile == "" {
		return nil
	}
	if cur, err := os.ReadFile(path); err == nil && string(cur) == sessionFile {
		// Touch so pruning sees it as live.
		now := time.Now()
		return os.Chtimes(path, now, now)
	}
	return WriteFileAtomic(path, []byte(sessionFile), 0o600)
}

// ResolveHandle returns the transcript handle points at, or "" when unbound
// or the transcript no longer exists.
func ResolveHandle(agentDir, handle string) string {
	path := handlePath(agentDir, handle)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	file := strings.TrimSpace(string(data))
	if _, err := os.Stat(file); err != nil {
		return ""
	}
	return file
}

// ForgetHandle removes handle's binding. It reports whether one existed.
func ForgetHandle(agentDir, handle string) bool {
	path := handlePath(agentDir, handle)
	return path != "" && os.Remove(path) == nil
}

// PruneHandles removes handle bindings not touched since cutoff.
func PruneHandles(agentDir string, cutoff time.Time) {
	PruneDir(handlesDir(agentDir), cutoff)
}

// PruneDir removes regular files in dir last modified before cutoff.
func PruneDir(dir string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
