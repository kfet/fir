package tools

import (
	"regexp"
	"strings"
)

// ansiRegexp matches ANSI escape sequences.
var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]|\x1b\][^\x07]*\x07|\x1b[^[(\x1b]`)

// StripAnsi removes ANSI escape sequences from a string.
func StripAnsi(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

// AppendColorEnv appends environment variables that force CLI tools to emit
// ANSI color codes even when stdout is not a TTY. Covers:
//   - CLICOLOR=1       — BSD/macOS convention to enable color (ls, etc.)
//   - CLICOLOR_FORCE=3 — BSD/macOS convention to force color even without TTY
//   - FORCE_COLOR=1    — Node.js/chalk convention (jest, vitest, etc.)
//
// macOS /bin/ls requires CLICOLOR=1 in addition to CLICOLOR_FORCE=3 to emit
// color codes when stdout is not a TTY.
//
// Existing values are not overwritten so the user can opt out.
func AppendColorEnv(env []string) []string {
	hasCLI, hasCLIForce, hasForce := false, false, false
	for _, e := range env {
		if strings.HasPrefix(e, "CLICOLOR=") {
			hasCLI = true
		}
		if strings.HasPrefix(e, "CLICOLOR_FORCE=") {
			hasCLIForce = true
		}
		if strings.HasPrefix(e, "FORCE_COLOR=") {
			hasForce = true
		}
	}
	if !hasCLI {
		env = append(env, "CLICOLOR=1")
	}
	if !hasCLIForce {
		env = append(env, "CLICOLOR_FORCE=3")
	}
	if !hasForce {
		env = append(env, "FORCE_COLOR=1")
	}
	return env
}
