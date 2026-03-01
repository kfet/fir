package extproc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TrustEntry records when a particular extension hash was approved.
type TrustEntry struct {
	Hash      string `json:"hash"`
	TrustedAt string `json:"trusted_at"`
}

// TrustStore manages a persistent set of trusted project-local extensions.
// Keys in the JSON file are "projectDir:extensionName".
type TrustStore struct {
	path string
	mu   sync.Mutex
}

// NewTrustStore returns a TrustStore using the default path
// (~/.config/fir/trusted-extensions.json).
func NewTrustStore() *TrustStore {
	home, _ := os.UserHomeDir()
	return &TrustStore{
		path: filepath.Join(home, ".config", "fir", "trusted-extensions.json"),
	}
}

// NewTrustStoreWithPath returns a TrustStore backed by the given file path.
func NewTrustStoreWithPath(path string) *TrustStore {
	return &TrustStore{path: path}
}

func trustKey(projectDir, name string) string {
	return projectDir + ":" + name
}

// ComputeHash returns the hex-encoded SHA-256 hash of the file at path.
func ComputeHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (ts *TrustStore) load() (map[string]TrustEntry, error) {
	data, err := os.ReadFile(ts.path)
	if os.IsNotExist(err) {
		return make(map[string]TrustEntry), nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]TrustEntry
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (ts *TrustStore) save(m map[string]TrustEntry) error {
	if err := os.MkdirAll(filepath.Dir(ts.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ts.path, data, 0o644)
}

// IsTrusted returns true if the stored hash for (projectDir, name) matches hash.
func (ts *TrustStore) IsTrusted(projectDir, name, hash string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	m, err := ts.load()
	if err != nil {
		return false
	}
	entry, ok := m[trustKey(projectDir, name)]
	return ok && entry.Hash == hash
}

// RecordTrust persists approval for the given extension and hash.
func (ts *TrustStore) RecordTrust(projectDir, name, hash string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	m, err := ts.load()
	if err != nil {
		return err
	}
	m[trustKey(projectDir, name)] = TrustEntry{
		Hash:      hash,
		TrustedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return ts.save(m)
}

// RevokeTrust removes the trust entry for (projectDir, name).
func (ts *TrustStore) RevokeTrust(projectDir, name string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	m, err := ts.load()
	if err != nil {
		return err
	}
	delete(m, trustKey(projectDir, name))
	return ts.save(m)
}
