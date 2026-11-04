package acp

import (
	"context"
	"errors"
	"fmt"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/pinoauth"
)

// Interactive MCP OAuth over ACP.
//
// A relay whose user's browser is not on the agent host cannot complete
// `/mcp login`: the redirect lands on an ephemeral 127.0.0.1 listener on the
// agent host. The `mcp:<server>` authenticate method reuses the two-call
// interactive protocol (see auth_interactive.go) so the user can paste the
// landing URL back. The loopback listener stays up in parallel; whichever
// arrives first wins. See docs/acp-interactive-auth.md.
//
//	Call 1: { methodId: "mcp:<server>", _meta: { auth: { interactive: true, sessionId } } }
//	Call 2: { methodId: "mcp:<server>", _meta: { auth: { interactive: true, id, redirect } } }
//	Cancel: { methodId: "mcp:<server>", _meta: { auth: { interactive: true, id, cancel: true } } }
//
// sessionId is required on call 1: MCP managers are per session. mcp:*
// methods are not advertised in initialize authMethods.

// mcpAuthMethodPrefix prefixes the dynamic per-server MCP auth method ids.
const mcpAuthMethodPrefix = "mcp:"

// errMCPAuthNeedsInteractive rejects mcp:* calls that do not opt in to the
// two-call protocol: there is no blocking fallback for MCP logins.
var errMCPAuthNeedsInteractive = errors.New("mcp:* authentication requires _meta.auth.interactive")

// mcpLoginFunc runs one MCP server login. Overridable in tests.
type mcpLoginFunc func(ctx context.Context, mgr *mcp.Manager, server string, cb pinoauth.LoginCallbacks) error

func defaultMCPLogin(ctx context.Context, mgr *mcp.Manager, server string, cb pinoauth.LoginCallbacks) error {
	// LoginServer reconnects the server on success, and the manager's
	// tools-changed hook pushes the new tools into the session's agent.
	return mgr.LoginServer(ctx, server, cb)
}

// authenticateMCP serves authenticate for methodId "mcp:<server>".
func (pa *firAgent) authenticateMCP(reqCtx context.Context, methodID, server string, in authMetaIn) (acpsdk.AuthenticateResponse, error) {
	if server == "" {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("auth method %q names no MCP server", methodID)
	}
	if in.Cancel {
		pa.cancelPendingAuthFor(methodID, in.ID)
		return acpsdk.AuthenticateResponse{Meta: authResponseMeta(authStateCancelled, in.ID, "", "", "")}, nil
	}
	if in.Redirect != "" {
		return pa.completePendingAuth(reqCtx, methodID, in.ID, in.Redirect)
	}
	if !in.Interactive {
		return acpsdk.AuthenticateResponse{}, errMCPAuthNeedsInteractive
	}
	if in.SessionID == "" {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("auth method %q requires _meta.auth.sessionId", methodID)
	}
	entry, err := pa.rehydrateForPrompt(reqCtx, in.SessionID)
	if err != nil {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("session %s: %w", in.SessionID, err)
	}
	if entry == nil {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("session not found: %s", in.SessionID)
	}
	entry.touch(time.Now())
	mgr := entry.mcpManager
	if mgr == nil {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("no MCP servers configured in session %s", in.SessionID)
	}
	login := pa.mcpLogin
	if login == nil {
		login = defaultMCPLogin
	}
	firlog.Info("acp mcp oauth interactive: start", "server", server, "sessionId", in.SessionID)
	return pa.startPendingLogin(reqCtx, pendingLogin{
		methodID: methodID,
		label:    server,
		login: func(ctx context.Context, cb pinoauth.LoginCallbacks) error {
			return login(ctx, mgr, server, cb)
		},
		onSuccess: func() {
			entry.touch(time.Now())
			firlog.Info("acp mcp oauth interactive: login completed", "server", server, "sessionId", in.SessionID)
		},
	})
}
