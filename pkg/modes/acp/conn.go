// Connection interface for ACP client communication.
// Extracted to enable testing with mock connections.
package acp

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/debug"
)

// acpConn abstracts the methods used on *acpsdk.AgentSideConnection,
// enabling tests to substitute a mock.
type acpConn interface {
	SessionUpdate(ctx context.Context, params acpsdk.SessionNotification) error
	CreateTerminal(ctx context.Context, params acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error)
	KillTerminalCommand(ctx context.Context, params acpsdk.KillTerminalCommandRequest) (acpsdk.KillTerminalCommandResponse, error)
	ReleaseTerminal(ctx context.Context, params acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error)
	TerminalOutput(ctx context.Context, params acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error)
	WaitForTerminalExit(ctx context.Context, params acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error)
	ReadTextFile(ctx context.Context, params acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error)
	WriteTextFile(ctx context.Context, params acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error)
}

// Compile-time check that *acpsdk.AgentSideConnection satisfies acpConn.
var _ acpConn = (*acpsdk.AgentSideConnection)(nil)

// rawMethodHandler builds an acpsdk.MethodHandler that handles ALL inbound methods,
// including session/list and session/resume which the Go SDK doesn't know about.
// It is used by newRawConn below to create a connection that handles these methods.
//
// Unfortunately, AgentSideConnection.handle is unexported, so we can't chain
// to it. Instead, we build our own complete dispatch table. This keeps the code
// in one place and avoids any SDK forking.
func rawMethodHandler(pa *firAgent) acpsdk.MethodHandler {
	mu := sync.Mutex{}
	sessionCancels := map[string]func(){}

	return func(ctx context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
		debug.Log("acp: incoming method=%s params=%s", method, truncate(string(params), 200))
		switch method {
		case "authenticate":
			var p acpsdk.AuthenticateRequest
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			resp, err := pa.Authenticate(ctx, p)
			if err != nil {
				return nil, toReqErr(err)
			}
			return resp, nil

		case "initialize":
			var p acpsdk.InitializeRequest
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			resp, err := pa.Initialize(ctx, p)
			if err != nil {
				return nil, toReqErr(err)
			}
			// Augment with sessionCapabilities (not in SDK 0.10.7, present in TS SDK 0.14.1).
			// Marshal to map and inject manually so clients like Zed know we support
			// session/list and session/resume.
			rawResp, err := json.Marshal(resp)
			if err != nil {
				return resp, nil // fall back to plain struct (no session capabilities)
			}
			var respMap map[string]any
			if err := json.Unmarshal(rawResp, &respMap); err != nil {
				return resp, nil // fall back to plain struct (no session capabilities)
			}
			if caps, ok := respMap["agentCapabilities"].(map[string]any); ok {
				caps["sessionCapabilities"] = map[string]any{
					"list":   map[string]any{},
					"resume": map[string]any{},
				}
			}
			return respMap, nil

		case "session/cancel":
			var p acpsdk.CancelNotification
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			mu.Lock()
			if cn, ok := sessionCancels[string(p.SessionId)]; ok {
				cn()
				delete(sessionCancels, string(p.SessionId))
			}
			mu.Unlock()
			if err := pa.Cancel(ctx, p); err != nil {
				return nil, toReqErr(err)
			}
			return nil, nil

		case "session/new":
			var p acpsdk.NewSessionRequest
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			resp, err := pa.NewSession(ctx, p)
			if err != nil {
				return nil, toReqErr(err)
			}
			// Inject configOptions (not in SDK v0.6.3, present in upstream schema).
			rawResp, merr := json.Marshal(resp)
			if merr != nil {
				return resp, nil
			}
			var respMap map[string]any
			if merr := json.Unmarshal(rawResp, &respMap); merr != nil {
				return resp, nil
			}
			pa.mu.Lock()
			entry, ok := pa.sessions[string(resp.SessionId)]
			pa.mu.Unlock()
			if ok {
				respMap["configOptions"] = buildConfigOptions(entry)
			}
			return respMap, nil

		case "session/prompt":
			var p acpsdk.PromptRequest
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			reqCtx, cancel := context.WithCancel(ctx)
			mu.Lock()
			sessionCancels[string(p.SessionId)] = cancel
			mu.Unlock()
			resp, err := pa.Prompt(reqCtx, p)
			mu.Lock()
			delete(sessionCancels, string(p.SessionId))
			mu.Unlock()
			cancel()
			if err != nil {
				return nil, toReqErr(err)
			}
			return resp, nil

		case "session/set_mode":
			var p acpsdk.SetSessionModeRequest
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			resp, err := pa.SetSessionMode(ctx, p)
			if err != nil {
				return nil, toReqErr(err)
			}
			return resp, nil

		case "session/set_model":
			var p acpsdk.SetSessionModelRequest
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			resp, err := pa.SetSessionModel(ctx, p)
			if err != nil {
				return nil, toReqErr(err)
			}
			return resp, nil

		case "session/set_config_option":
			var p SetSessionConfigOptionRequest
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			resp, err := pa.SetSessionConfigOption(ctx, p)
			if err != nil {
				return nil, toReqErr(err)
			}
			return resp, nil

		case "session/list":
			var p ListSessionsRequest
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			resp, err := pa.ListSessions(ctx, p)
			if err != nil {
				return nil, toReqErr(err)
			}
			return resp, nil

		case "session/resume":
			var p ResumeSessionRequest
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
			}
			resp, err := pa.ResumeSession(ctx, p)
			if err != nil {
				return nil, toReqErr(err)
			}
			// Inject configOptions like session/new.
			rawResp, merr := json.Marshal(resp)
			if merr != nil {
				return resp, nil
			}
			var respMap map[string]any
			if merr := json.Unmarshal(rawResp, &respMap); merr != nil {
				return resp, nil
			}
			pa.mu.Lock()
			resumeEntry, rok := pa.sessions[p.SessionId]
			pa.mu.Unlock()
			if rok {
				respMap["configOptions"] = buildConfigOptions(resumeEntry)
			}
			return respMap, nil

		default:
			return nil, acpsdk.NewMethodNotFound(method)
		}
	}
}

// rawConn wraps an *acpsdk.Connection and implements acpConn.
// It is used when we bypass AgentSideConnection and use NewConnection directly
// so that we can handle session/list and session/resume.
type rawConn struct {
	conn *acpsdk.Connection
}

func (r *rawConn) SessionUpdate(ctx context.Context, params acpsdk.SessionNotification) error {
	return r.conn.SendNotification(ctx, acpsdk.ClientMethodSessionUpdate, params)
}

func (r *rawConn) CreateTerminal(ctx context.Context, params acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.SendRequest[acpsdk.CreateTerminalResponse](r.conn, ctx, acpsdk.ClientMethodTerminalCreate, params)
}

func (r *rawConn) KillTerminalCommand(ctx context.Context, params acpsdk.KillTerminalCommandRequest) (acpsdk.KillTerminalCommandResponse, error) {
	return acpsdk.SendRequest[acpsdk.KillTerminalCommandResponse](r.conn, ctx, acpsdk.ClientMethodTerminalKill, params)
}

func (r *rawConn) ReleaseTerminal(ctx context.Context, params acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.SendRequest[acpsdk.ReleaseTerminalResponse](r.conn, ctx, acpsdk.ClientMethodTerminalRelease, params)
}

func (r *rawConn) TerminalOutput(ctx context.Context, params acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.SendRequest[acpsdk.TerminalOutputResponse](r.conn, ctx, acpsdk.ClientMethodTerminalOutput, params)
}

func (r *rawConn) WaitForTerminalExit(ctx context.Context, params acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.SendRequest[acpsdk.WaitForTerminalExitResponse](r.conn, ctx, acpsdk.ClientMethodTerminalWaitForExit, params)
}

func (r *rawConn) ReadTextFile(ctx context.Context, params acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.SendRequest[acpsdk.ReadTextFileResponse](r.conn, ctx, acpsdk.ClientMethodFsReadTextFile, params)
}

func (r *rawConn) WriteTextFile(ctx context.Context, params acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.SendRequest[acpsdk.WriteTextFileResponse](r.conn, ctx, acpsdk.ClientMethodFsWriteTextFile, params)
}

// truncate returns s truncated to n characters with "…" suffix if needed.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// toReqErr converts a Go error to a JSON-RPC RequestError.
func toReqErr(err error) *acpsdk.RequestError {
	if re, ok := err.(*acpsdk.RequestError); ok {
		return re
	}
	return acpsdk.NewInternalError(map[string]any{"error": err.Error()})
}

// newRawConn creates a raw connection that handles ALL inbound methods
// (including unstable session/list and session/resume) and returns
// an acpConn for outbound calls plus a done channel.
func newRawConn(pa *firAgent, stdout io.Writer, stdin io.Reader) (acpConn, <-chan struct{}) {
	handler := rawMethodHandler(pa)
	conn := acpsdk.NewConnection(handler, stdout, stdin)
	return &rawConn{conn: conn}, conn.Done()
}
