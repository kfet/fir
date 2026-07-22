// Ported from: packages/coding-agent/src/modes/print-mode.ts
// Upstream hash: 1caadb2e
package print

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/session"
)

// ErrAgentAborted is returned when the agent stops with an error or aborted
// stop reason. The caller should exit with a non-zero status code.
var ErrAgentAborted = errors.New("agent aborted")

// Mode specifies the output format for print mode.
type Mode string

const (
	ModeText Mode = "text"
	ModeJSON Mode = "json"
)

// Options configures print mode.
type Options struct {
	// Mode is the output format: "text" for final response only, "json" for all events as JSON.
	Mode Mode
	// Messages are additional prompts to send after InitialMessage.
	Messages []string
	// InitialMessage is the first message to send.
	InitialMessage string
	// InitialImages are images to attach to the initial message.
	InitialImages []ai.ImageContent
}

// Run executes print (single-shot) mode.
// It sends prompts to the agent session and outputs results to stdout.
func Run(asession *session.AgentSession, opts Options) error {
	firlog.Debug("print mode", "outputMode", opts.Mode)
	if opts.Mode == ModeJSON {
		// Subscribe early to capture all events as JSON
		asession.Subscribe(func(event session.AgentSessionEvent) {
			data, err := json.Marshal(event)
			if err == nil {
				fmt.Println(string(data))
			}
		})
	}

	// Send initial message
	if opts.InitialMessage != "" {
		promptOpts := &session.PromptOptions{}
		if len(opts.InitialImages) > 0 {
			promptOpts.Images = opts.InitialImages
		}
		if err := asession.Prompt(opts.InitialMessage, promptOpts); err != nil {
			return fmt.Errorf("initial prompt failed: %w", err)
		}
	}

	// Send remaining messages
	for _, msg := range opts.Messages {
		if err := asession.Prompt(msg); err != nil {
			return fmt.Errorf("prompt failed: %w", err)
		}
	}

	// In text mode, output the final assistant response
	if opts.Mode == ModeText {
		state := asession.State()
		messages := state.Messages
		if len(messages) == 0 {
			return nil
		}

		lastMsg := messages[len(messages)-1]
		assistant := lastMsg.Message.AsAssistant()
		if assistant == nil {
			return nil
		}

		// Check for error/aborted
		if assistant.StopReason == ai.StopReasonError || assistant.StopReason == ai.StopReasonAborted {
			errMsg := assistant.ErrorMessage
			if errMsg == "" {
				errMsg = fmt.Sprintf("Request %s", assistant.StopReason)
			}
			fmt.Fprintln(os.Stderr, "["+time.Now().Format("15:04:05")+"] "+errMsg)
			return ErrAgentAborted
		}

		// Output text content
		for _, content := range assistant.Content {
			if content.Text != nil {
				fmt.Println(content.Text.Text)
			}
		}
	}

	return nil
}
