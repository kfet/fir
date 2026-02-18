package acp

import (
	"context"
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"
)

// mockConn records all calls for assertions in tests.
type mockConn struct {
	mu      sync.Mutex
	updates []acpsdk.SessionNotification
	// terminals tracks created terminals
	nextTerminalID string
	terminalOutput string
	terminalExit   *int
	// waitError, if non-nil, is returned by WaitForTerminalExit.
	waitError error
}

func newMockConn() *mockConn {
	return &mockConn{
		nextTerminalID: "term-1",
	}
}

func (m *mockConn) SessionUpdate(_ context.Context, params acpsdk.SessionNotification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates = append(m.updates, params)
	return nil
}

func (m *mockConn) CreateTerminal(_ context.Context, _ acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{TerminalId: m.nextTerminalID}, nil
}

func (m *mockConn) KillTerminalCommand(_ context.Context, _ acpsdk.KillTerminalCommandRequest) (acpsdk.KillTerminalCommandResponse, error) {
	return acpsdk.KillTerminalCommandResponse{}, nil
}

func (m *mockConn) ReleaseTerminal(_ context.Context, _ acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, nil
}

func (m *mockConn) TerminalOutput(_ context.Context, _ acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	resp := acpsdk.TerminalOutputResponse{Output: m.terminalOutput}
	if m.terminalExit != nil {
		resp.ExitStatus = &acpsdk.TerminalExitStatus{ExitCode: m.terminalExit}
	}
	return resp, nil
}

func (m *mockConn) WaitForTerminalExit(_ context.Context, _ acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	if m.waitError != nil {
		return acpsdk.WaitForTerminalExitResponse{}, m.waitError
	}
	resp := acpsdk.WaitForTerminalExitResponse{}
	if m.terminalExit != nil {
		resp.ExitCode = m.terminalExit
	}
	return resp, nil
}

func (m *mockConn) ReadTextFile(_ context.Context, params acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{Content: ""}, nil
}

func (m *mockConn) WriteTextFile(_ context.Context, params acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, nil
}

func (m *mockConn) getUpdates() []acpsdk.SessionNotification {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]acpsdk.SessionNotification, len(m.updates))
	copy(out, m.updates)
	return out
}
