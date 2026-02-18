// Ported from: packages/coding-agent/src/modes/acp/acp-terminal.ts
// Upstream hash: pi-mono-acp branch
//
// ACP terminal operations for bash command execution. When the ACP client
// supports terminals, bash commands are delegated to the client for rendering.
package acp

import (
	"context"
	"fmt"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// maxBackgroundTerminals is the max concurrent background terminals per session.
const maxBackgroundTerminals = 10

// defaultMaxBytes is the output byte limit for ACP terminals.
const defaultMaxBytes = 50 * 1024

// terminalState tracks ACP terminals for a session.
type terminalState struct {
	mu sync.Mutex
	// pendingBashTerminals maps toolCallId → terminalId for foreground bash commands
	pendingBashTerminals map[string]string
	// backgroundTerminals maps commandId → terminalId for background commands
	backgroundTerminals map[string]string
}

func newTerminalState() *terminalState {
	return &terminalState{
		pendingBashTerminals: make(map[string]string),
		backgroundTerminals:  make(map[string]string),
	}
}

// embedTerminalInToolCall sends a tool_call_update to embed a terminal in the tool call UI.
func embedTerminalInToolCall(conn acpConn, sessionID, toolCallID, terminalID string) {
	_ = conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(sessionID),
		Update: acpsdk.UpdateToolCall(
			acpsdk.ToolCallId(toolCallID),
			acpsdk.WithUpdateContent([]acpsdk.ToolCallContent{
				acpsdk.ToolTerminalRef(terminalID),
			}),
		),
	})
}

// AcpBashExecResult is the result of a foreground bash execution via ACP terminal.
type AcpBashExecResult struct {
	ExitCode *int
	Output   string
}

// AcpBashExec executes a command via the ACP client's terminal.
func AcpBashExec(
	ctx context.Context,
	conn acpConn,
	ts *terminalState,
	sessionID, toolCallID, command, cwd string,
	timeout int,
) (*AcpBashExecResult, error) {
	outputByteLimit := defaultMaxBytes
	terminal, err := conn.CreateTerminal(ctx, acpsdk.CreateTerminalRequest{
		SessionId:       acpsdk.SessionId(sessionID),
		Command:         command,
		Cwd:             &cwd,
		OutputByteLimit: &outputByteLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("create terminal: %w", err)
	}
	termID := terminal.TerminalId

	ts.mu.Lock()
	ts.pendingBashTerminals[toolCallID] = termID
	ts.mu.Unlock()
	embedTerminalInToolCall(conn, sessionID, toolCallID, termID)

	sid := acpsdk.SessionId(sessionID)

	if ctx.Err() != nil {
		conn.KillTerminalCommand(context.Background(), acpsdk.KillTerminalCommandRequest{SessionId: sid, TerminalId: termID})
		conn.ReleaseTerminal(context.Background(), acpsdk.ReleaseTerminalRequest{SessionId: sid, TerminalId: termID})
		return nil, fmt.Errorf("aborted")
	}

	execCtx := ctx
	var cancel context.CancelFunc
	timedOut := false
	if timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}

	exitResult, waitErr := conn.WaitForTerminalExit(execCtx, acpsdk.WaitForTerminalExitRequest{SessionId: sid, TerminalId: termID})
	if waitErr != nil && execCtx.Err() != nil && timeout > 0 {
		timedOut = true
		conn.KillTerminalCommand(context.Background(), acpsdk.KillTerminalCommandRequest{SessionId: sid, TerminalId: termID})
	} else if waitErr != nil {
		ts.mu.Lock()
		delete(ts.pendingBashTerminals, toolCallID)
		ts.mu.Unlock()
		conn.ReleaseTerminal(context.Background(), acpsdk.ReleaseTerminalRequest{SessionId: sid, TerminalId: termID})
		return nil, waitErr
	}

	output, _ := conn.TerminalOutput(context.Background(), acpsdk.TerminalOutputRequest{SessionId: sid, TerminalId: termID})
	conn.ReleaseTerminal(context.Background(), acpsdk.ReleaseTerminalRequest{SessionId: sid, TerminalId: termID})

	if ctx.Err() != nil {
		return nil, fmt.Errorf("aborted")
	}
	if timedOut {
		return nil, fmt.Errorf("timeout:%d", timeout)
	}

	return &AcpBashExecResult{ExitCode: exitResult.ExitCode, Output: output.Output}, nil
}

// StartBackgroundCommand starts a background command via ACP terminal.
func StartBackgroundCommand(
	ctx context.Context,
	conn acpConn,
	ts *terminalState,
	sessionID, command, cwd, toolCallID string,
) (string, error) {
	ts.mu.Lock()
	if len(ts.backgroundTerminals) >= maxBackgroundTerminals {
		ts.mu.Unlock()
		return "", fmt.Errorf("maximum of %d background commands reached. Kill an existing one with bash_kill first", maxBackgroundTerminals)
	}
	ts.mu.Unlock()

	outputByteLimit := defaultMaxBytes
	terminal, err := conn.CreateTerminal(ctx, acpsdk.CreateTerminalRequest{
		SessionId:       acpsdk.SessionId(sessionID),
		Command:         command,
		Cwd:             &cwd,
		OutputByteLimit: &outputByteLimit,
	})
	if err != nil {
		return "", fmt.Errorf("create terminal: %w", err)
	}

	cmdID := terminal.TerminalId
	ts.mu.Lock()
	ts.backgroundTerminals[cmdID] = cmdID
	ts.pendingBashTerminals[toolCallID] = cmdID
	ts.mu.Unlock()
	embedTerminalInToolCall(conn, sessionID, toolCallID, cmdID)
	return cmdID, nil
}

// GetBackgroundOutput returns the current output and status of a background command.
func GetBackgroundOutput(
	ctx context.Context,
	conn acpConn,
	ts *terminalState,
	sessionID, commandID string,
) (output string, isRunning bool, exitCode *int, err error) {
	ts.mu.Lock()
	termID, ok := ts.backgroundTerminals[commandID]
	ts.mu.Unlock()
	if !ok {
		return "", false, nil, fmt.Errorf("no background command found with ID: %s", commandID)
	}

	result, err := conn.TerminalOutput(ctx, acpsdk.TerminalOutputRequest{
		SessionId: acpsdk.SessionId(sessionID), TerminalId: termID,
	})
	if err != nil {
		return "", false, nil, err
	}
	isRunning = result.ExitStatus == nil
	if result.ExitStatus != nil {
		exitCode = result.ExitStatus.ExitCode
	}
	return result.Output, isRunning, exitCode, nil
}

// KillBackgroundCommand kills a background command and returns its final output.
func KillBackgroundCommand(
	ctx context.Context,
	conn acpConn,
	ts *terminalState,
	sessionID, commandID string,
) (output string, exitCode *int, err error) {
	ts.mu.Lock()
	termID, ok := ts.backgroundTerminals[commandID]
	ts.mu.Unlock()
	if !ok {
		return "", nil, fmt.Errorf("no background command found with ID: %s", commandID)
	}

	sid := acpsdk.SessionId(sessionID)
	conn.KillTerminalCommand(ctx, acpsdk.KillTerminalCommandRequest{SessionId: sid, TerminalId: termID})
	result, _ := conn.TerminalOutput(ctx, acpsdk.TerminalOutputRequest{SessionId: sid, TerminalId: termID})
	conn.ReleaseTerminal(ctx, acpsdk.ReleaseTerminalRequest{SessionId: sid, TerminalId: termID})

	ts.mu.Lock()
	delete(ts.backgroundTerminals, commandID)
	ts.mu.Unlock()

	if result.ExitStatus != nil {
		exitCode = result.ExitStatus.ExitCode
	}
	return result.Output, exitCode, nil
}

// CleanupBackgroundTerminals kills and releases all background terminals.
func CleanupBackgroundTerminals(ctx context.Context, conn acpConn, ts *terminalState, sessionID string) {
	ts.mu.Lock()
	terminals := make(map[string]string, len(ts.backgroundTerminals))
	for k, v := range ts.backgroundTerminals {
		terminals[k] = v
	}
	ts.backgroundTerminals = make(map[string]string)
	ts.mu.Unlock()

	sid := acpsdk.SessionId(sessionID)
	for _, termID := range terminals {
		conn.KillTerminalCommand(ctx, acpsdk.KillTerminalCommandRequest{SessionId: sid, TerminalId: termID})
		conn.ReleaseTerminal(ctx, acpsdk.ReleaseTerminalRequest{SessionId: sid, TerminalId: termID})
	}
}
