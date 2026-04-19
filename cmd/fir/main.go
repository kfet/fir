// fir — Go implementation of the fir coding agent CLI
package main

import (
	"errors"
	"fmt"
	"os"

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

func main() {
	if err := run(); err != nil {
		if errors.Is(err, printmode.ErrAgentAborted) {
			// Error message already written to stderr by print mode.
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
