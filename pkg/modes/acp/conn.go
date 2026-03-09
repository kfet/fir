// Connection interface for ACP client communication.
// Extracted to enable testing with mock connections.
package acp

import (
	"context"
	"encoding/json"
	"io"
	"runtime"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	firlog "github.com/kfet/fir/pkg/log"
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
func rawMethodHandler(pa *firAgent, wn *writeNotifier) acpsdk.MethodHandler {
	mu := sync.Mutex{}
	sessionCancels := map[string]func(){}

	return func(ctx context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
		firlog.Debug("acp: incoming method", "method", method, "params", truncate(string(params), 200))
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
			// Replace authMethods with extended format (RFD auth-methods).
			// The SDK's AuthMethod only has id/name/description/_meta, but the RFD
			// defines type/varName/link/args/env as top-level fields.
			pa.mu.Lock()
			extMethods := pa.authMethods
			pa.mu.Unlock()
			if len(extMethods) > 0 {
				respMap["authMethods"] = extMethods
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
			// Send available commands AFTER the response is written to stdout.
			// Without this, the notification races the response and arrives first,
			// causing clients to drop commands for a session they don't know yet.
			//
			// AfterWrite waits for the next stdout write. In rare cases a concurrent
			// event-handler write could signal it before the response write; the
			// Gosched+Sleep below lets the SDK's handleInbound finish sending the
			// response even in that edge case.
			afterWrite := wn.AfterWrite()
			go func() {
				<-afterWrite
				runtime.Gosched()
				time.Sleep(5 * time.Millisecond)
				pa.sendAvailableCommands(string(resp.SessionId))
			}()
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
			// Send available commands + replay history AFTER the response is written.
			afterWrite := wn.AfterWrite()
			go func() {
				<-afterWrite
				runtime.Gosched()
				time.Sleep(5 * time.Millisecond)
				pa.sendAvailableCommands(p.SessionId)
				pa.replaySessionHistory(p.SessionId, resumeEntry)
			}()
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

// writeNotifier wraps an io.Writer and signals waiters after each Write.
// This allows goroutines to wait for a response to be flushed before
// sending follow-up notifications, avoiding the race where a notification
// for a session arrives before the session/new response.
type writeNotifier struct {
	inner io.Writer
	mu    sync.Mutex
	ch    chan struct{} // closed after the next Write, then recreated
}

func newWriteNotifier(w io.Writer) *writeNotifier {
	return &writeNotifier{inner: w, ch: make(chan struct{})}
}

func (w *writeNotifier) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	// Signal all waiters that a write completed.
	w.mu.Lock()
	old := w.ch
	w.ch = make(chan struct{})
	w.mu.Unlock()
	close(old)
	return n, err
}

// AfterWrite returns a channel that is closed after the next Write completes.
// Used by handlers to defer notifications until after the response is sent.
func (w *writeNotifier) AfterWrite() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

// newRawConn creates a raw connection that handles ALL inbound methods
// (including unstable session/list and session/resume) and returns
// an acpConn for outbound calls plus a done channel.
func newRawConn(pa *firAgent, stdout io.Writer, stdin io.Reader) (acpConn, <-chan struct{}) {
	wn := newWriteNotifier(stdout)
	handler := rawMethodHandler(pa, wn)
	conn := acpsdk.NewConnection(handler, wn, stdin)
	return &rawConn{conn: conn}, conn.Done()
}
