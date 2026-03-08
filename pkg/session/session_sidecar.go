package session

import (
	"encoding/json"
	"os"
)

// ReexecSidecar holds state that must survive a reexec: queued follow-up
// messages and any text the user had typed in the editor.
type ReexecSidecar struct {
	QueueMessages []string `json:"queue_messages,omitempty"`
	PendingInput  string   `json:"pending_input,omitempty"`
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
