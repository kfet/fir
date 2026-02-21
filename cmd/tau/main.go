// tau — Go implementation of the tau coding agent CLI
package main

import (
	"errors"
	"fmt"
	"os"

	printmode "github.com/kfet/tau/pkg/modes/print"
)

var version = "dev"

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
