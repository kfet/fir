// Package modes provides entry points for the different execution modes (print, interactive, rpc).
package modes

import (
	"github.com/kfet/pi-go/pkg/core"
	printmode "github.com/kfet/pi-go/pkg/modes/print"
)

// PrintModeOptions configures print (single-shot) mode.
type PrintModeOptions = printmode.Options

// RunPrintMode executes print mode: sends prompts, outputs result, exits.
func RunPrintMode(session *core.AgentSession, opts PrintModeOptions) error {
	return printmode.Run(session, opts)
}
