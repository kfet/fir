// Ported from: packages/coding-agent/src/modes/rpc/rpc-mode.ts
// Upstream hash: 1caadb2e
package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/core"
)

// Server is the RPC mode server.
// It reads JSON commands from stdin, dispatches them to the AgentSession,
// and writes JSON responses/events to stdout.
type Server struct {
	session *core.AgentSession
	input   io.Reader
	output  io.Writer

	mu sync.Mutex // protects writes to output
}

// NewServer creates a new RPC server.
func NewServer(session *core.AgentSession) *Server {
	return &Server{
		session: session,
		input:   os.Stdin,
		output:  os.Stdout,
	}
}

// NewServerWithIO creates a new RPC server with custom I/O (for testing).
func NewServerWithIO(session *core.AgentSession, input io.Reader, output io.Writer) *Server {
	return &Server{
		session: session,
		input:   input,
		output:  output,
	}
}

// outputJSON writes a JSON object as a single line to stdout.
func (s *Server) outputJSON(obj interface{}) {
	data, err := json.Marshal(obj)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(s.output, string(data))
}

// Run starts the RPC server. It blocks until stdin is closed or an error occurs.
func (s *Server) Run() error {
	// Subscribe to all agent session events and output them as JSON
	if s.session != nil {
		s.session.Subscribe(func(event core.AgentSessionEvent) {
			s.outputJSON(event)
		})
	}

	// Read JSON commands from stdin line by line
	scanner := bufio.NewScanner(s.input)
	// Increase max line size for large commands (e.g., commands with embedded images)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Try to parse as extension UI response first
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			s.outputJSON(NewErrorResponse("", "parse", fmt.Sprintf("Failed to parse command: %s", err.Error())))
			continue
		}

		// Check if this is an extension UI response
		if typeField, ok := raw["type"]; ok {
			var typeStr string
			if err := json.Unmarshal(typeField, &typeStr); err == nil && typeStr == "extension_ui_response" {
				// Handle extension UI response (currently no-op as extension support is not yet wired)
				continue
			}
		}

		// Parse as regular command
		cmd, err := ParseRpcCommand([]byte(line))
		if err != nil {
			s.outputJSON(NewErrorResponse("", "parse", fmt.Sprintf("Failed to parse command: %s", err.Error())))
			continue
		}

		resp := s.handleCommand(cmd)
		s.outputJSON(resp)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("stdin read error: %w", err)
	}

	return nil
}

// handleCommand dispatches a single RPC command and returns a response.
func (s *Server) handleCommand(cmd RpcCommand) RpcResponse {
	id := cmd.ID

	switch cmd.Type {
	// =================================================================
	// Prompting
	// =================================================================

	case CmdPrompt:
		// Fire and forget - events will stream via subscription
		go func() {
			if err := s.session.Prompt(cmd.Message); err != nil {
				s.outputJSON(NewErrorResponse(id, CmdPrompt, err.Error()))
			}
		}()
		return NewSuccessResponse(id, CmdPrompt, nil)

	case CmdSteer:
		// Steer interrupts current streaming and injects a new message
		msg := agent.NewAgentMessage(ai.NewUserMsg(cmd.Message, time.Now().UnixMilli()))
		s.session.Agent.Steer(msg)
		return NewSuccessResponse(id, CmdSteer, nil)

	case CmdFollowUp:
		// Follow-up queues a message for after current streaming completes
		msg := agent.NewAgentMessage(ai.NewUserMsg(cmd.Message, time.Now().UnixMilli()))
		s.session.Agent.FollowUp(msg)
		return NewSuccessResponse(id, CmdFollowUp, nil)

	case CmdAbort:
		s.session.Close()
		return NewSuccessResponse(id, CmdAbort, nil)

	case CmdNewSession:
		success, err := s.session.NewSessionCmd()
		if err != nil {
			return NewErrorResponse(id, CmdNewSession, err.Error())
		}
		return NewSuccessResponse(id, CmdNewSession, NewSessionData{Cancelled: !success})

	// =================================================================
	// State
	// =================================================================

	case CmdGetState:
		model := s.session.Model()
		state := RpcSessionState{
			Model:                 model,
			ThinkingLevel:        ai.ThinkingLevel(s.session.ThinkingLevel()),
			IsStreaming:          s.session.IsStreaming(),
			SteeringMode:        "all",
			FollowUpMode:        "all",
			SessionID:           "default",
			AutoCompactionEnabled: true,
			MessageCount:        len(s.session.State().Messages),
		}
		return NewSuccessResponse(id, CmdGetState, state)

	// =================================================================
	// Model
	// =================================================================

	case CmdSetModel:
		registry := s.session.ModelRegistryRef()
		if registry == nil {
			return NewErrorResponse(id, CmdSetModel, "model registry not available")
		}
		models := registry.GetAvailable()
		var found *ai.Model
		for _, m := range models {
			if m.Provider == cmd.Provider && m.ID == cmd.ModelID {
				found = m
				break
			}
		}
		if found == nil {
			return NewErrorResponse(id, CmdSetModel, fmt.Sprintf("Model not found: %s/%s", cmd.Provider, cmd.ModelID))
		}
		s.session.SetModel(found)
		return NewSuccessResponse(id, CmdSetModel, found)

	case CmdCycleModel:
		// Not yet fully implemented - return null
		return NewSuccessResponse(id, CmdCycleModel, nil)

	case CmdGetAvailableModels:
		registry := s.session.ModelRegistryRef()
		if registry == nil {
			return NewErrorResponse(id, CmdGetAvailableModels, "model registry not available")
		}
		models := registry.GetAvailable()
		// Convert to value slice for JSON serialization
		modelValues := make([]ai.Model, len(models))
		for i, m := range models {
			modelValues[i] = *m
		}
		return NewSuccessResponse(id, CmdGetAvailableModels, GetAvailableModelsData{Models: modelValues})

	// =================================================================
	// Thinking
	// =================================================================

	case CmdSetThinkingLevel:
		s.session.SetThinkingLevel(string(cmd.Level))
		return NewSuccessResponse(id, CmdSetThinkingLevel, nil)

	case CmdCycleThinkingLevel:
		// Not yet fully implemented - return null
		return NewSuccessResponse(id, CmdCycleThinkingLevel, nil)

	// =================================================================
	// Queue Modes
	// =================================================================

	case CmdSetSteeringMode:
		return NewSuccessResponse(id, CmdSetSteeringMode, nil)

	case CmdSetFollowUpMode:
		return NewSuccessResponse(id, CmdSetFollowUpMode, nil)

	// =================================================================
	// Compaction
	// =================================================================

	case CmdCompact:
		result, err := s.session.RunCompaction()
		if err != nil {
			return NewErrorResponse(id, CmdCompact, err.Error())
		}
		return NewSuccessResponse(id, CmdCompact, result)

	case CmdSetAutoCompaction:
		return NewSuccessResponse(id, CmdSetAutoCompaction, nil)

	// =================================================================
	// Retry
	// =================================================================

	case CmdSetAutoRetry:
		return NewSuccessResponse(id, CmdSetAutoRetry, nil)

	case CmdAbortRetry:
		return NewSuccessResponse(id, CmdAbortRetry, nil)

	// =================================================================
	// Bash
	// =================================================================

	case CmdBash:
		// TODO: wire to bash executor when available on AgentSession
		return NewErrorResponse(id, CmdBash, "bash execution via RPC not yet implemented")

	case CmdAbortBash:
		return NewSuccessResponse(id, CmdAbortBash, nil)

	// =================================================================
	// Session
	// =================================================================

	case CmdGetSessionStats:
		state := s.session.State()
		stats := SessionStats{
			SessionID:     "default",
			TotalMessages: len(state.Messages),
		}
		// Count message types
		for _, msg := range state.Messages {
			if msg.Message.AsUser() != nil {
				stats.UserMessages++
			} else if msg.Message.AsAssistant() != nil {
				stats.AssistantMessages++
			}
		}
		return NewSuccessResponse(id, CmdGetSessionStats, stats)

	case CmdExportHTML:
		return NewErrorResponse(id, CmdExportHTML, "HTML export not yet implemented")

	case CmdSwitchSession:
		return NewErrorResponse(id, CmdSwitchSession, "session switching not yet implemented")

	case CmdFork:
		if err := s.session.Fork(cmd.EntryID); err != nil {
			return NewErrorResponse(id, CmdFork, err.Error())
		}
		return NewSuccessResponse(id, CmdFork, ForkData{Cancelled: false})

	case CmdGetForkMessages:
		return NewSuccessResponse(id, CmdGetForkMessages, GetForkMessagesData{Messages: nil})

	case CmdGetLastAssistantText:
		state := s.session.State()
		var text *string
		for i := len(state.Messages) - 1; i >= 0; i-- {
			if assistant := state.Messages[i].Message.AsAssistant(); assistant != nil {
				for _, content := range assistant.Content {
					if content.Text != nil {
						t := content.Text.Text
						text = &t
						break
					}
				}
				break
			}
		}
		return NewSuccessResponse(id, CmdGetLastAssistantText, GetLastAssistantTextData{Text: text})

	case CmdSetSessionName:
		name := strings.TrimSpace(cmd.Name)
		if name == "" {
			return NewErrorResponse(id, CmdSetSessionName, "Session name cannot be empty")
		}
		return NewSuccessResponse(id, CmdSetSessionName, nil)

	// =================================================================
	// Messages
	// =================================================================

	case CmdGetMessages:
		state := s.session.State()
		return NewSuccessResponse(id, CmdGetMessages, GetMessagesData{Messages: state.Messages})

	// =================================================================
	// Commands
	// =================================================================

	case CmdGetCommands:
		return NewSuccessResponse(id, CmdGetCommands, GetCommandsData{Commands: nil})

	default:
		return NewErrorResponse(id, cmd.Type, fmt.Sprintf("Unknown command: %s", cmd.Type))
	}
}
