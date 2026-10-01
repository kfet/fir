package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StatePath returns the per-session settings history for a transcript:
// <session>.jsonl.state.jsonl. It is a sibling of the transcript, not part of
// the .meta.json listing cache: that file is a 0644, mtime-invalidated cache
// that is rebuilt freely and doubles as the flock target, while the state file
// is authoritative, holds secrets (MCP env) and so is written 0600.
//
// The file is append-only JSONL: each line is a StateRecord holding a full
// snapshot. The last valid line is the current state.
func StatePath(sessionFile string) string {
	if sessionFile == "" {
		return ""
	}
	return sessionFile + ".state.jsonl"
}

// legacyStatePath is the pre-history single-snapshot file (fir <= 1.24.1).
func legacyStatePath(sessionFile string) string {
	return sessionFile + ".state.json"
}

// MaxStateHistory caps the history file: once it exceeds this many lines it
// is atomically rewritten to keep only the newest MaxStateHistory lines.
var MaxStateHistory = 1000

// StateRecord is one line of the state history.
type StateRecord struct {
	TS     string          `json:"ts"` // RFC3339Nano
	Reason string          `json:"reason,omitempty"`
	State  json.RawMessage `json:"state"`
}

// ReadStateHistory returns every valid record of sessionFile's state history,
// oldest first. Undecodable lines (e.g. a torn final write) are skipped. A
// legacy .state.json is migrated first.
func ReadStateHistory(sessionFile string) []StateRecord {
	recs, _ := readHistory(sessionFile)
	return recs
}

func readHistory(sessionFile string) ([]StateRecord, int) {
	path := StatePath(sessionFile)
	if path == "" {
		return nil, 0
	}
	migrateLegacyState(sessionFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0
	}
	var recs []StateRecord
	lines := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lines++
		var r StateRecord
		if json.Unmarshal(line, &r) == nil && len(r.State) > 0 {
			recs = append(recs, r)
		}
	}
	return recs, lines
}

// migrateLegacyState turns an old single-snapshot .state.json into the first
// history line and removes it. It is a no-op once the history exists.
func migrateLegacyState(sessionFile string) {
	legacy := legacyStatePath(sessionFile)
	data, err := os.ReadFile(legacy)
	if err != nil {
		return
	}
	if _, err := os.Stat(StatePath(sessionFile)); err == nil {
		_ = os.Remove(legacy)
		return
	}
	if !json.Valid(data) {
		return
	}
	var ts time.Time
	if fi, err := os.Stat(legacy); err == nil {
		ts = fi.ModTime()
	}
	if appendRecord(StatePath(sessionFile), json.RawMessage(data), ts, "migrate") == nil {
		_ = os.Remove(legacy)
	}
}

// ReadState decodes the latest state for sessionFile into v. It reports false
// when there is no history or no line can be decoded.
func ReadState(sessionFile string, v any) bool {
	recs := ReadStateHistory(sessionFile)
	if len(recs) == 0 {
		return false
	}
	return json.Unmarshal(recs[len(recs)-1].State, v) == nil
}

// WriteState records v as sessionFile's current state.
func WriteState(sessionFile string, v any) error {
	return AppendState(sessionFile, v, "")
}

// AppendState appends v to sessionFile's state history (mode 0600, fsynced)
// unless it equals the latest snapshot. reason optionally names the change.
func AppendState(sessionFile string, v any, reason string) error {
	path := StatePath(sessionFile)
	if path == "" {
		return errors.New("no session file")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	recs, lines := readHistory(sessionFile)
	if n := len(recs); n > 0 && jsonEqual(recs[n-1].State, data) {
		return nil
	}
	if err := appendRecord(path, data, time.Now(), reason); err != nil {
		return err
	}
	if lines+1 > MaxStateHistory {
		return compactHistory(path, MaxStateHistory)
	}
	return nil
}

func jsonEqual(a, b []byte) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

func appendRecord(path string, state json.RawMessage, ts time.Time, reason string) error {
	if ts.IsZero() {
		ts = time.Now()
	}
	line, err := json.Marshal(StateRecord{TS: ts.UTC().Format(time.RFC3339Nano), Reason: reason, State: state})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	// A previous torn write may have left no trailing newline; start fresh.
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		if last, err := lastByte(path, fi.Size()); err == nil && last != '\n' {
			line = append([]byte("\n"), line...)
		}
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func lastByte(path string, size int64) (byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	b := make([]byte, 1)
	_, err = f.ReadAt(b, size-1)
	return b[0], err
}

// compactHistory atomically rewrites path keeping only its newest keep lines.
func compactHistory(path string, keep int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var lines [][]byte
	for _, l := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			lines = append(lines, l)
		}
	}
	if len(lines) <= keep {
		return nil
	}
	lines = lines[len(lines)-keep:]
	return WriteFileAtomic(path, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600)
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
	if err := tmp.Sync(); err != nil {
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
