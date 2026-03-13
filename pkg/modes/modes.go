// Package modes provides entry points for the different execution modes (print, interactive, acp).
package modes

import (
	printmode "github.com/kfet/fir/pkg/modes/print"
	"github.com/kfet/fir/pkg/session"
)

// PrintModeOptions configures print (single-shot) mode.
type PrintModeOptions = printmode.Options

// RunPrintMode executes print mode: sends prompts, outputs result, exits.
func RunPrintMode(session *session.AgentSession, opts PrintModeOptions) error {
	return printmode.Run(session, opts)
}
