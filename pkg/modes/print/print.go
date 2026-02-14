// Ported from: packages/coding-agent/src/modes/print-mode.ts
// Upstream hash: 1caadb2e
package print

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/core"
)

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
func Run(session *core.AgentSession, opts Options) error {
	if opts.Mode == ModeJSON {
		// Subscribe early to capture all events as JSON
		session.Subscribe(func(event core.AgentSessionEvent) {
			data, err := json.Marshal(event)
			if err == nil {
				fmt.Println(string(data))
			}
		})
	}

	// Send initial message
	if opts.InitialMessage != "" {
		promptOpts := &core.PromptOptions{}
		if len(opts.InitialImages) > 0 {
			promptOpts.Images = opts.InitialImages
		}
		if err := session.Prompt(opts.InitialMessage, promptOpts); err != nil {
			return fmt.Errorf("initial prompt failed: %w", err)
		}
	}

	// Send remaining messages
	for _, msg := range opts.Messages {
		if err := session.Prompt(msg); err != nil {
			return fmt.Errorf("prompt failed: %w", err)
		}
	}

	// In text mode, output the final assistant response
	if opts.Mode == ModeText {
		state := session.State()
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
			fmt.Fprintln(os.Stderr, errMsg)
			os.Exit(1)
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
