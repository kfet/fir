package store

import (
	"encoding/json"
	"os"
	"time"
)

// ReexecSidecar holds state that must survive a reexec: queued follow-up
// messages, any text the user had typed in the editor, and per-extension
// key/value data saved by extensions before the exec.
type ReexecSidecar struct {
	QueueMessages []string                     `json:"queue_messages,omitempty"`
	PendingInput  string                       `json:"pending_input,omitempty"`
	ExtensionData map[string]map[string]string `json:"extension_data,omitempty"`
}

// ReexecSidecarPath returns the sidecar file path for a given session file.
func ReexecSidecarPath(sessionFile string) string {
	return sessionFile + ".reexec.json"
}

// WriteReexecSidecar writes the sidecar JSON next to the session file.
func WriteReexecSidecar(sessionFile string, sidecar *ReexecSidecar) error {
	data, err := json.Marshal(sidecar)
	if err != nil {
		return err
	}
	path := ReexecSidecarPath(sessionFile)
	return os.WriteFile(path, data, 0o644)
}

// ReadReexecSidecar reads and deletes the sidecar file. If the file does not
// exist, it returns (nil, nil).
func ReadReexecSidecar(sessionFile string) (*ReexecSidecar, error) {
	path := ReexecSidecarPath(sessionFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// Remove before parsing so it's cleaned up even on bad JSON.
	os.Remove(path)
	var sc ReexecSidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, err
	}
	return &sc, nil
}

// MetaSidecar caches session metadata to speed up listing.
type MetaSidecar struct {
	Name              string    `json:"name"`
	FirstMessage      string    `json:"first_message"`
	Cwd               string    `json:"cwd"`
	ID                string    `json:"id"`
	ParentSessionPath string    `json:"parent_session_path"`
	Created           time.Time `json:"created"`
	MessageCount      int       `json:"message_count"`
	ModTime           time.Time `json:"mod_time"` // mtime of the .jsonl when this was written
}

func metaSidecarPath(jsonlPath string) string {
	return jsonlPath + ".meta.json"
}

func readSidecar(jsonlPath string, jsonlMtime time.Time) *MetaSidecar {
	path := metaSidecarPath(jsonlPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m MetaSidecar
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	if !m.ModTime.Equal(jsonlMtime) {
		return nil
	}
	return &m
}

func writeSidecar(jsonlPath string, m *MetaSidecar) {
	path := metaSidecarPath(jsonlPath)
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
