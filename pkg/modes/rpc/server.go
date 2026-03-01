// Ported from: packages/coding-agent/src/modes/rpc/rpc-mode.ts
// Upstream hash: 1caadb2e
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
	firlog "github.com/kfet/fir/pkg/log"
)

// Server is the RPC mode server.
// It reads JSON commands from stdin, dispatches them to the AgentSession,
// and writes JSON responses/events to stdout.
type Server struct {
	session *core.AgentSession
	input   io.Reader
	output  io.Writer

	mu sync.Mutex    // protects writes to output
	wg sync.WaitGroup // tracks in-flight prompt goroutines
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
func (s *Server) outputJSON(obj any) {
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
	firlog.Info("rpc server starting")
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

	// Wait for any in-flight prompt goroutines to finish before returning,
	// so the server doesn't exit while the agent is still processing.
	s.wg.Wait()

	return nil
}

// handleCommand dispatches a single RPC command and returns a response.
func (s *Server) handleCommand(cmd RpcCommand) RpcResponse {
	id := cmd.ID
	firlog.Debug("rpc command", "method", cmd.Type, "id", id)

	switch cmd.Type {
	// =================================================================
	// Prompting
	// =================================================================

	case CmdPrompt:
		// Fire and forget - events will stream via subscription.
		// Track with wg so Run() waits for the goroutine before exiting.
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
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
		s.session.Agent.Abort()
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
		autoCompactionEnabled := true
		if s.session.SettingsManager != nil {
			autoCompactionEnabled = s.session.SettingsManager.GetCompactionEnabled()
		}
		state := RpcSessionState{
			Model:                 model,
			ThinkingLevel:        ai.ThinkingLevel(s.session.ThinkingLevel()),
			IsStreaming:          s.session.IsStreaming(),
			SteeringMode:        "all",
			FollowUpMode:        "all",
			SessionID:           "default",
			AutoCompactionEnabled: autoCompactionEnabled,
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
		registry := s.session.ModelRegistryRef()
		if registry == nil {
			return NewSuccessResponse(id, CmdCycleModel, nil)
		}
		models := registry.GetAvailable()
		if len(models) == 0 {
			return NewSuccessResponse(id, CmdCycleModel, nil)
		}
		current := s.session.Model()
		nextIdx := 0
		for i, m := range models {
			if current != nil && m.Provider == current.Provider && m.ID == current.ID {
				nextIdx = (i + 1) % len(models)
				break
			}
		}
		next := models[nextIdx]
		s.session.SetModel(next)
		return NewSuccessResponse(id, CmdCycleModel, CycleModelData{
			Model:         *next,
			ThinkingLevel: ai.ThinkingLevel(s.session.ThinkingLevel()),
			IsScoped:      false,
		})

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
		levels := []ai.ThinkingLevel{
			ai.ThinkingOff,
			ai.ThinkingMinimal,
			ai.ThinkingLow,
			ai.ThinkingMedium,
			ai.ThinkingHigh,
			ai.ThinkingXHigh,
		}
		current := ai.ThinkingLevel(s.session.ThinkingLevel())
		nextIdx := 0
		for i, l := range levels {
			if l == current {
				nextIdx = (i + 1) % len(levels)
				break
			}
		}
		next := levels[nextIdx]
		s.session.SetThinkingLevel(string(next))
		return NewSuccessResponse(id, CmdCycleThinkingLevel, CycleThinkingLevelData{Level: next})

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
		result, err := s.session.RunCompaction(context.Background(), cmd.CustomInstructions)
		if err != nil {
			return NewErrorResponse(id, CmdCompact, err.Error())
		}
		// Auto-resume if pending work (unanswered user message or tool result)
		if s.session.HasPendingWork() {
			go func() { _ = s.session.Agent.Continue() }()
		}
		return NewSuccessResponse(id, CmdCompact, result)

	case CmdSetAutoCompaction:
		if cmd.Enabled != nil && s.session.SettingsManager != nil {
			s.session.SettingsManager.SetCompactionEnabled(*cmd.Enabled)
		}
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
		if cmd.Command == "" {
			return NewErrorResponse(id, CmdBash, "command is required")
		}
		result, err := s.session.ExecuteBash(cmd.Command, nil)
		if err != nil {
			return NewErrorResponse(id, CmdBash, err.Error())
		}
		return NewSuccessResponse(id, CmdBash, result)

	case CmdAbortBash:
		s.session.AbortBash()
		return NewSuccessResponse(id, CmdAbortBash, nil)

	// =================================================================
	// Session
	// =================================================================

	case CmdGetSessionStats:
		coreStats := s.session.GetSessionStats()
		stats := SessionStats{
			SessionID:         coreStats.SessionID,
			TotalMessages:     coreStats.TotalMessages,
			UserMessages:      coreStats.UserMessages,
			AssistantMessages: coreStats.AssistantMessages,
		}
		return NewSuccessResponse(id, CmdGetSessionStats, stats)

	case CmdExportHTML:
		exportPath, err := s.session.ExportToHTML("")
		if err != nil {
			return NewErrorResponse(id, CmdExportHTML, err.Error())
		}
		return NewSuccessResponse(id, CmdExportHTML, ExportHTMLData{Path: exportPath})

	case CmdSwitchSession:
		if cmd.SessionPath == "" {
			return NewErrorResponse(id, CmdSwitchSession, "session path is required")
		}
		if err := s.session.SwitchSession(cmd.SessionPath); err != nil {
			return NewErrorResponse(id, CmdSwitchSession, err.Error())
		}
		return NewSuccessResponse(id, CmdSwitchSession, nil)

	case CmdFork:
		text, cancelled, err := s.session.Fork(cmd.EntryID)
		if err != nil {
			return NewErrorResponse(id, CmdFork, err.Error())
		}
		return NewSuccessResponse(id, CmdFork, ForkData{Text: text, Cancelled: cancelled})

	case CmdGetForkMessages:
		msgs := s.session.GetUserMessagesForForking()
		forkMsgs := make([]ForkMessageEntry, len(msgs))
		for i, m := range msgs {
			forkMsgs[i] = ForkMessageEntry{EntryID: m.EntryID, Text: m.Text}
		}
		return NewSuccessResponse(id, CmdGetForkMessages, GetForkMessagesData{Messages: forkMsgs})

	case CmdGetLastAssistantText:
		t := s.session.GetLastAssistantText()
		var text *string
		if t != "" {
			text = &t
		}
		return NewSuccessResponse(id, CmdGetLastAssistantText, GetLastAssistantTextData{Text: text})

	case CmdSetSessionName:
		name := strings.TrimSpace(cmd.Name)
		if name == "" {
			return NewErrorResponse(id, CmdSetSessionName, "Session name cannot be empty")
		}
		s.session.SetSessionName(name)
		return NewSuccessResponse(id, CmdSetSessionName, nil)

	// =================================================================
	// Messages
	// =================================================================

	case CmdGetMessages:
		state := s.session.State()
		msgs := state.Messages
		if msgs == nil {
			msgs = []agent.AgentMessage{}
		}
		return NewSuccessResponse(id, CmdGetMessages, GetMessagesData{Messages: msgs})

	// =================================================================
	// Commands
	// =================================================================

	case CmdGetCommands:
		var commands []RpcSlashCommand

		// Add prompt templates
		if rl := s.session.ResourceLoader(); rl != nil {
			if prompts, _ := rl.GetPrompts(); len(prompts) > 0 {
				for _, p := range prompts {
					commands = append(commands, RpcSlashCommand{
						Name:        p.Name,
						Description: p.Description,
						Source:      "prompt",
						Location:    p.Source,
						Path:        p.FilePath,
					})
				}
			}

			// Add skill commands
			if skills, _ := rl.GetSkills(); len(skills) > 0 {
				for _, sk := range skills {
					commands = append(commands, RpcSlashCommand{
						Name:        "skill:" + sk.Name,
						Description: sk.Description,
						Source:      "skill",
						Location:    sk.Source,
						Path:        sk.FilePath,
					})
				}
			}
		}

		return NewSuccessResponse(id, CmdGetCommands, GetCommandsData{Commands: commands})

	default:
		return NewErrorResponse(id, cmd.Type, fmt.Sprintf("Unknown command: %s", cmd.Type))
	}
}
