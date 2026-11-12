package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AuthStorageEnv overrides the auth.storage setting for one invocation.
const AuthStorageEnv = "FIR_AUTH_STORAGE"

// Auth storage modes (the auth.storage setting).
const (
	// AuthStorageFile keeps every secret in auth.json (0600 + flock).
	AuthStorageFile = "file"
	// AuthStorageAuto moves secrets into the OS secret store when one
	// answers a probe, and stays on auth.json otherwise.
	AuthStorageAuto = "auto"
	// AuthStorageKeyring requires the OS secret store; writes fail without it.
	AuthStorageKeyring = "keyring"
)

// AuthSettings is the "auth" settings object.
type AuthSettings struct {
	// Storage is "file" (default), "auto" or "keyring".
	Storage string `json:"storage,omitempty"`
}

// ParseAuthStorageMode validates a mode string; empty means the default.
func ParseAuthStorageMode(v string) (string, error) {
	switch m := strings.ToLower(strings.TrimSpace(v)); m {
	case "":
		return AuthStorageFile, nil
	case AuthStorageFile, AuthStorageAuto, AuthStorageKeyring:
		return m, nil
	default:
		return "", fmt.Errorf("invalid auth.storage %q (want auto, keyring or file)", v)
	}
}

// LoadAuthStorageMode returns the auth storage mode from FIR_AUTH_STORAGE, or
// else from the global settings.json in agentDir. It is read straight from the
// global file (never project settings: a cloned repo must not be able to
// choose where credentials go). Missing or unreadable settings yield "file".
func LoadAuthStorageMode(agentDir string) (string, error) {
	if v, ok := os.LookupEnv(AuthStorageEnv); ok && strings.TrimSpace(v) != "" {
		return ParseAuthStorageMode(v)
	}
	raw, err := os.ReadFile(filepath.Join(agentDir, "settings.json"))
	if err != nil {
		return AuthStorageFile, nil
	}
	var s struct {
		Auth *AuthSettings `json:"auth"`
	}
	if json.Unmarshal(raw, &s) != nil || s.Auth == nil {
		return AuthStorageFile, nil
	}
	return ParseAuthStorageMode(s.Auth.Storage)
}
