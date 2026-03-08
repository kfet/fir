// Ported from: packages/coding-agent/src/core/defaults.ts
// Upstream hash: 1caadb2e
package config

import "github.com/kfet/fir/pkg/ai"

// DefaultThinkingLevel is the default reasoning level for agent sessions.
const DefaultThinkingLevel ai.ThinkingLevel = ai.ThinkingMedium

// ConfigDirName is the project-local configuration directory name.
const ConfigDirName = ".fir"
