// fir — Go implementation of the fir coding agent CLI
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	printmode "github.com/kfet/fir/pkg/modes/print"
)

var (
	version = "dev"
	// licensesURL points to the GitHub release page where LICENSE and
	// THIRD_PARTY_NOTICES.md are published as assets. Injected at release
	// build time via -ldflags "-X main.licensesURL=...". When empty (e.g.
	// dev builds), the --version output falls back to a generic pointer.
	licensesURL = ""
)

// parseChdirFlag scans args for a `-C <dir>` (or `-C=dir`, `--cwd[=dir]`,
// `--directory[=dir]`) option. It returns the target directory, the number
// of slice elements consumed starting at the flag, the index where the flag
// was found, and whether a flag was present. An error is returned if the
// flag is malformed (missing/empty directory). Only the first occurrence
// is honoured; downstream parsing should strip `args[idx:idx+consume]`.
func parseChdirFlag(args []string) (dir string, idx, consume int, found bool, err error) {
	for i, a := range args {
		switch {
		case a == "-C" || a == "--cwd" || a == "--directory":
			if i+1 >= len(args) {
				return "", i, 0, true, fmt.Errorf("%s requires a directory argument", a)
			}
			d := args[i+1]
			if d == "" {
				return "", i, 0, true, fmt.Errorf("%s: empty directory", a)
			}
			return d, i, 2, true, nil
		case strings.HasPrefix(a, "-C="):
			d := a[len("-C="):]
			if d == "" {
				return "", i, 0, true, fmt.Errorf("-C: empty directory")
			}
			return d, i, 1, true, nil
		case strings.HasPrefix(a, "--cwd="):
			d := a[len("--cwd="):]
			if d == "" {
				return "", i, 0, true, fmt.Errorf("--cwd: empty directory")
			}
			return d, i, 1, true, nil
		case strings.HasPrefix(a, "--directory="):
			d := a[len("--directory="):]
			if d == "" {
				return "", i, 0, true, fmt.Errorf("--directory: empty directory")
			}
			return d, i, 1, true, nil
		}
	}
	return "", 0, 0, false, nil
}

// applyChdirFlag scans os.Args for a `-C <dir>` option, chdirs into it, and
// strips the flag from os.Args so subsequent argument parsing ignores it.
// Supports `-C dir`, `-C=dir`, `--cwd[=dir]`, and `--directory[=dir]`.
// Only the first occurrence is honoured.
func applyChdirFlag() error {
	// Skip os.Args[0] (program name).
	dir, idx, consume, found, err := parseChdirFlag(os.Args[1:])
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("chdir %q: %w", dir, err)
	}
	// Translate index from the args[1:] slice back to os.Args.
	start := idx + 1
	os.Args = append(os.Args[:start], os.Args[start+consume:]...)
	return nil
}

func main() {
	if err := applyChdirFlag(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := run(); err != nil {
		if errors.Is(err, printmode.ErrAgentAborted) {
			// Error message already written to stderr by print mode.
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
