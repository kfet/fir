package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/ai/oauth"
	firlog "github.com/kfet/fir/pkg/log"
)

// Two-call interactive OAuth protocol.
//
// Reuses the existing oauth.LoginCallbacks plumbing — same Login flow as
// the TUI runs in cmd/fir/login.go. The only difference is the transport:
// the URL goes out in the response of the first `authenticate` call, and
// the pasted redirect URL comes in via the request of a second
// `authenticate` call. ACP clients that don't set _meta.auth.interactive
// continue to use the legacy blocking branch in authenticateOAuth.
//
// Wire format:
//
//	Call 1 request:  { methodId, _meta: { auth: { interactive: true } } }
//	Call 1 response: { _meta: { auth: { state: "needs_redirect", url, instructions } } }
//	   or            { _meta: { auth: { state: "ok" } } }            (cached creds)
//	   or JSON-RPC error                                              (login failed early)
//
//	Call 2 request:  { methodId, _meta: { auth: { interactive: true, redirect: "<url>" } } }
//	Call 2 response: { _meta: { auth: { state: "ok" } } } or error
//
//	Cancel:          { methodId, _meta: { auth: { interactive: true, cancel: true } } }
//	                 → cancels any in-flight login for methodId; response state: "cancelled".
//
// Concurrency: each call-1 gets a fresh opaque id, so multiple flows
// for the same methodId can run in parallel without interfering.

const (
	authStateNeedsRedirect = "needs_redirect"
	authStateOK            = "ok"
	authStateCancelled     = "cancelled"
)

// pendingAuthTimeout bounds how long a parked Login goroutine waits for
// the user to paste the redirect URL. Belt-and-braces — relays should
// cancel explicitly when the user gives up.
const pendingAuthTimeout = 10 * time.Minute

// pendingAuth holds the state of an in-flight interactive OAuth login.
type pendingAuth struct {
	id       string // unique per pending; surfaced as _meta.auth.id on call 1
	methodID string
	cancel   context.CancelFunc

	// authInfo is the URL+instructions captured from OnAuth. Buffered(1)
	// so the goroutine never blocks if the RPC handler isn't waiting yet.
	authInfo chan oauth.AuthInfo
	// paste receives the redirect URL (or raw code) submitted via call 2.
	paste chan string
	// done receives the final result of authStorage.Login.
	done chan error
}

// authMetaIn is the parsed form of the request `_meta.auth` object.
type authMetaIn struct {
	Interactive bool
	ID          string // pending login id (echo of what call 1 returned)
	Redirect    string
	Cancel      bool
}

// parseAuthMeta extracts the auth fields from an authenticate request's _meta.
// Returns zero-value (Interactive=false) for any shape we don't recognise so
// callers fall through to the legacy blocking branch.
func parseAuthMeta(meta any) authMetaIn {
	out := authMetaIn{}
	m, ok := meta.(map[string]any)
	if !ok {
		return out
	}
	a, ok := m["auth"].(map[string]any)
	if !ok {
		return out
	}
	if v, ok := a["interactive"].(bool); ok {
		out.Interactive = v
	}
	if v, ok := a["id"].(string); ok {
		out.ID = v
	}
	if v, ok := a["redirect"].(string); ok {
		out.Redirect = v
	}
	if v, ok := a["cancel"].(bool); ok {
		out.Cancel = v
	}
	return out
}

// authResponseMeta builds the `_meta.auth` payload for a response.
func authResponseMeta(state, id, url, instructions string) map[string]any {
	a := map[string]any{"state": state}
	if id != "" {
		a["id"] = id
	}
	if url != "" {
		a["url"] = url
	}
	if instructions != "" {
		a["instructions"] = instructions
	}
	return map[string]any{"auth": a}
}

// authenticateOAuthInteractive handles the two-call interactive OAuth flow.
// reqCtx is the ACP request context for the call currently being served — it
// governs the wait inside this RPC, but never the parked Login goroutine.
func (pa *firAgent) authenticateOAuthInteractive(reqCtx context.Context, method *ExtendedAuthMethod, in authMetaIn) (acpsdk.AuthenticateResponse, error) {
	if in.Cancel {
		pa.cancelPendingAuth(in.ID)
		return acpsdk.AuthenticateResponse{Meta: authResponseMeta(authStateCancelled, in.ID, "", "")}, nil
	}

	// Call 2: a redirect was supplied → submit to the parked Login.
	if in.Redirect != "" {
		return pa.completePendingAuth(reqCtx, in.ID, in.Redirect)
	}

	// Call 1: start a new login flow.
	return pa.startPendingAuth(reqCtx, method)
}

// startPendingAuth launches the Login goroutine and waits until it either
// produces an auth URL (via OnAuth) or finishes outright (cached creds /
// early error).
func (pa *firAgent) startPendingAuth(reqCtx context.Context, method *ExtendedAuthMethod) (acpsdk.AuthenticateResponse, error) {
	providerID := strings.TrimPrefix(method.Id, "oauth-")

	pa.mu.Lock()
	authStorage := pa.authStorage
	pa.mu.Unlock()
	if authStorage == nil {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("auth storage not initialized")
	}
	if oauth.GetProvider(providerID) == nil {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("unknown OAuth provider: %s", providerID)
	}

	loginCtx, cancel := context.WithTimeout(context.Background(), pendingAuthTimeout)
	pending := &pendingAuth{
		id:       newPendingID(),
		methodID: method.Id,
		cancel:   cancel,
		authInfo: make(chan oauth.AuthInfo, 1),
		paste:    make(chan string, 1),
		done:     make(chan error, 1),
	}

	pa.registerPendingAuth(pending)

	// Run the Login flow with ACP-shaped callbacks. OnAuth surfaces the URL
	// to the RPC handler (which returns it to the relay); OnManualCodeInput
	// blocks until call 2 supplies the pasted redirect URL.
	go func() {
		err := authStorage.Login(providerID, oauth.LoginCallbacks{
			Ctx: loginCtx,
			OnAuth: func(info oauth.AuthInfo) {
				select {
				case pending.authInfo <- info:
				default:
					// Already delivered (or channel full): drop.
				}
			},
			OnManualCodeInput: func() (string, error) {
				select {
				case v, ok := <-pending.paste:
					if !ok {
						return "", loginCtx.Err()
					}
					return v, nil
				case <-loginCtx.Done():
					return "", loginCtx.Err()
				}
			},
			OnProgress: func(message string) {
				firlog.Info("acp oauth progress", "method", method.Id, "id", pending.id, "message", message)
			},
		})
		pending.done <- err
	}()

	// Wait for the first signal: either OnAuth delivered a URL, the login
	// finished early (cached creds / immediate error), or the request ctx
	// was cancelled by the relay.
	select {
	case info := <-pending.authInfo:
		firlog.Info("acp oauth interactive: auth url ready", "method", method.Id, "id", pending.id, "url", info.URL)
		return acpsdk.AuthenticateResponse{
			Meta: authResponseMeta(authStateNeedsRedirect, pending.id, info.URL, info.Instructions),
		}, nil
	case err := <-pending.done:
		// Login finished without ever producing a URL.
		pa.unregisterPendingAuth(pending.id, pending)
		cancel()
		if err != nil {
			return acpsdk.AuthenticateResponse{}, fmt.Errorf("oauth login failed for %s: %w", providerID, err)
		}
		pa.refreshAllModelRegistries()
		return acpsdk.AuthenticateResponse{Meta: authResponseMeta(authStateOK, pending.id, "", "")}, nil
	case <-reqCtx.Done():
		// Caller went away before we got a URL. Tear down — without a URL
		// there's nothing meaningful to resume on a follow-up call.
		pa.cancelPendingAuth(pending.id)
		return acpsdk.AuthenticateResponse{}, reqCtx.Err()
	}
}

// completePendingAuth feeds the supplied redirect URL into the parked Login
// goroutine and waits for it to finish.
func (pa *firAgent) completePendingAuth(reqCtx context.Context, id, redirect string) (acpsdk.AuthenticateResponse, error) {
	if id == "" {
		return acpsdk.AuthenticateResponse{}, errNoPendingAuth
	}
	pending := pa.lookupPendingAuth(id)
	if pending == nil {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("%w: %s", errNoPendingAuth, id)
	}

	// Submit the pasted redirect URL. paste is buffered(1).
	select {
	case pending.paste <- redirect:
	case err := <-pending.done:
		// Goroutine exited before we could submit (e.g. timeout). Surface its result.
		pa.unregisterPendingAuth(id, pending)
		pending.cancel()
		if err != nil {
			return acpsdk.AuthenticateResponse{}, err
		}
		pa.refreshAllModelRegistries()
		return acpsdk.AuthenticateResponse{Meta: authResponseMeta(authStateOK, id, "", "")}, nil
	case <-reqCtx.Done():
		// Caller hung up before we could submit. Cancel the parked goroutine
		// so it doesn't sit on the paste channel forever.
		pa.cancelPendingAuth(id)
		return acpsdk.AuthenticateResponse{}, reqCtx.Err()
	}

	// Wait for Login to finish.
	select {
	case err := <-pending.done:
		pa.unregisterPendingAuth(id, pending)
		pending.cancel()
		if err != nil {
			return acpsdk.AuthenticateResponse{}, fmt.Errorf("oauth login failed for %s: %w", strings.TrimPrefix(pending.methodID, "oauth-"), err)
		}
		pa.refreshAllModelRegistries()
		firlog.Info("acp oauth interactive: login completed", "method", pending.methodID, "id", id)
		return acpsdk.AuthenticateResponse{Meta: authResponseMeta(authStateOK, id, "", "")}, nil
	case <-reqCtx.Done():
		// Submitted the paste but caller hung up before Login finished.
		// Cancel the goroutine and unregister so the entry doesn't orphan
		// until the 10-minute timeout.
		pa.cancelPendingAuth(id)
		return acpsdk.AuthenticateResponse{}, reqCtx.Err()
	}
}

func (pa *firAgent) registerPendingAuth(p *pendingAuth) {
	pa.mu.Lock()
	if pa.pendingAuths == nil {
		pa.pendingAuths = make(map[string]*pendingAuth)
	}
	pa.pendingAuths[p.id] = p
	pa.mu.Unlock()
}

func (pa *firAgent) lookupPendingAuth(id string) *pendingAuth {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	return pa.pendingAuths[id]
}

// unregisterPendingAuth removes p from the map only if it's still the
// registered pending login for id. This avoids removing a replacement
// that a concurrent register has just installed (id collisions don't
// happen in practice, but be defensive).
func (pa *firAgent) unregisterPendingAuth(id string, p *pendingAuth) {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	if pa.pendingAuths[id] == p {
		delete(pa.pendingAuths, id)
	}
}

// cancelPendingAuth cancels and removes any pending login for id. Safe
// to call when none exists.
func (pa *firAgent) cancelPendingAuth(id string) {
	pa.mu.Lock()
	p := pa.pendingAuths[id]
	if p != nil {
		delete(pa.pendingAuths, id)
	}
	pa.mu.Unlock()
	if p == nil {
		return
	}
	p.cancel()
	// Drain the goroutine's final result so it doesn't leak. The goroutine
	// may have already exited (cancel triggers loginCtx.Done() inside the
	// blocking callbacks), so this is best-effort with a short bound.
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		firlog.Warn("acp oauth interactive: cancel timed out waiting for goroutine", "id", id, "method", p.methodID)
	}
}

// newPendingID returns a fresh opaque id for a pending login. Uses crypto
// randomness so two relays talking to the same fir can't accidentally
// collide.
func newPendingID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to time-based id. Login race on the same nanosecond
		// is harmless because our map ops are pointer-checked.
		return fmt.Sprintf("auth-%d", time.Now().UnixNano())
	}
	return "auth-" + hex.EncodeToString(b[:])
}

// errNoPendingAuth is returned when call 2 arrives without a matching call 1.
// Relays should treat any failure here as "ask the user to start over".
var errNoPendingAuth = errors.New("no pending interactive login")
