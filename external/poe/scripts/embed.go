// Package scripts embeds the spawn-poe-agent shell script so poe-bridge
// can execute it without requiring a separate installed script.
package scripts

import (
	_ "embed"
	"os"
	"path/filepath"
	"sync"
)

//go:embed spawn-poe-agent.sh
var spawnPoeAgent []byte

var (
	spawnPath string
	spawnOnce sync.Once
	spawnErr  error
)

// SpawnPoeAgentPath returns the path to an executable copy of the
// spawn-poe-agent script. The script is extracted from the embedded
// binary on first call and cached for the process lifetime.
func SpawnPoeAgentPath() (string, error) {
	spawnOnce.Do(func() {
		dir := filepath.Join(os.TempDir(), "poe-bridge")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			spawnErr = err
			return
		}
		p := filepath.Join(dir, "spawn-poe-agent")
		if err := os.WriteFile(p, spawnPoeAgent, 0o755); err != nil {
			spawnErr = err
			return
		}
		spawnPath = p
	})
	return spawnPath, spawnErr
}
