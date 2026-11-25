package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kfet/fir/pkg/auth"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/pinoauth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Client-side OAuth for HTTP MCP servers.
//
// The design has two halves, deliberately separated:
//
//   - oauthRoundTripper (below) is the *automatic* half, and it is entirely
//     silent. It attaches the bearer token, refreshes proactively before
//     expiry, and on a 401 tries a single silent recovery (refresh) then
//     replays the request. It never blocks on a human: a tool call carries the
//     caller's deadline, and a browser prompt inside a request would blow that
//     deadline, leave a half-run login behind, and be uncancellable from
//     above. When a genuine login is the only way forward it records an
//     *AuthRequiredError on the server's auth state and fails the request.
//
//   - serverAuth.Login is the *interactive* half, reached only through
//     Manager.LoginServer — that is, only from a foreground command the user
//     typed (`fir mcp login <server>`, `/mcp login <server>`).
//
// Nothing connects the two automatically. The dial path surfaces the
// AuthRequiredError, whose message names the command to run, and stops there.
// That is a structural guarantee rather than a policy: the Manager holds no
// login-UI hook at all, so no reconnect cycle, background goroutine or
// non-interactive mode (ACP, -p, CI) can start a browser flow. The reasons are
// that MCP servers connect from goroutines that may predate the terminal UI
// and must not touch its widget tree, and that the auto-reconnect loop re-dials
// forever — an automatic prompt there would be a browser-window storm.

// authMutex is a mutex whose acquisition can be bounded by a context.
//
// serverAuth's lock is held for the whole of an interactive login — minutes,
// while a human is in a browser. A plain sync.Mutex would make every
// concurrent request block on it with its own deadline ignored, and an
// http.RoundTrip that never returns cannot be cancelled from above: one tool
// call would freeze the agent session until the login finished. A channel
// semaphore lets the request path wait *with* its context and give up cleanly.
type authMutex struct {
	ch chan struct{}
}

func newAuthMutex() authMutex { return authMutex{ch: make(chan struct{}, 1)} }

// lock acquires unconditionally. Only for callers with no context to honour.
func (m authMutex) lock() { m.ch <- struct{}{} }

// lockCtx acquires, or returns ctx.Err() if ctx is done first.
func (m authMutex) lockCtx(ctx context.Context) error {
	select {
	case m.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tryLock acquires without blocking, reporting whether it succeeded. For
// callers that must not wait behind an interactive login (see
// serverAuth.invalidate).
func (m authMutex) tryLock() bool {
	select {
	case m.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (m authMutex) unlock() { <-m.ch }

// tokenRefreshWindow is how far ahead of expiry a token is refreshed. Wide
// enough that a request never races the expiry, short enough that we do not
// churn tokens on a chatty session.
const tokenRefreshWindow = 60 * time.Second

// oauthHTTPTimeout bounds the discovery, registration and token requests fir
// makes on its own behalf. The MCP data path is not affected — that client is
// unbounded because streamable transports hold long-lived SSE responses.
const oauthHTTPTimeout = 30 * time.Second

// callbackRoute is the loopback path the authorization server redirects to.
const callbackRoute = "/oauth/callback"

// AuthRequiredError reports that an MCP server needs an interactive OAuth
// login that fir cannot perform in the current context. Its message names the
// command that fixes it, so it is safe to surface directly to the user.
type AuthRequiredError struct {
	// Server is the MCP server name as it appears in mcp.json.
	Server string
	// Resource is the canonical server URL (RFC 8707 resource indicator).
	Resource string
	// Reason explains why existing credentials could not be used.
	Reason string
	// Cause is the underlying failure, if any.
	Cause error
}

func (e *AuthRequiredError) Error() string {
	msg := fmt.Sprintf("MCP server %q requires OAuth authentication", e.Server)
	if e.Reason != "" {
		msg += " (" + e.Reason + ")"
	}
	return msg + "; " + loginHint(e.Server)
}

// loginHint names both forms of the login command. The slash form leads
// because this message is nearly always read inside a session; the terminal
// form follows for the cases (ACP, plain CLI) where there is no prompt to
// type it at.
func loginHint(server string) string {
	return fmt.Sprintf("run: /mcp login %s (or: fir mcp login %s)", server, server)
}

func (e *AuthRequiredError) Unwrap() error { return e.Cause }

// serverAuth holds the OAuth state for one MCP server. It outlives individual
// connections — the reconnect loop re-dials through the same instance — so the
// cached token and the discovered challenge both survive a disconnect.
type serverAuth struct {
	name     string
	resource string
	cfg      AuthConfig
	store    credentialStore
	// hc is the plain HTTP client used for discovery, registration and token
	// requests. It must not route through oauthRoundTripper or a 401 during
	// discovery would recurse.
	hc *http.Client

	mu     authMutex
	loaded bool
	cred   *oauthCredential
	// synced is the fingerprint of the stored credential as of our last load
	// or write. A stored row with a different fingerprint was written by
	// someone else — another fir process, or another Manager in this one —
	// and supersedes whatever we hold in memory.
	synced string
	// loginInFlight is true while an interactive login holds mu. Requests
	// check it to fail fast with a clear message instead of burning their
	// whole deadline waiting on a human.
	loginInFlight atomic.Bool
	// gen increments on every credential change. RoundTrip captures it
	// alongside the token; handleUnauthorized uses it to tell "another
	// goroutine already recovered, just retry" from "the token I sent is
	// genuinely bad". Comparing tokens instead would misfire when the
	// rejected request was sent unauthenticated.
	gen       uint64
	challenge challengeInfo
	pending   *AuthRequiredError
	// lastRequired is the most recent unresolved login requirement. Unlike
	// pending (consumed by the dial path) it persists until a successful dial
	// or login, so health reporting sees mid-session refresh failures too.
	lastRequired atomic.Pointer[AuthRequiredError]
	// notify, when set, is called (in its own goroutine) whenever
	// lastRequired changes. Set once by Manager.serverAuthFor before use.
	notify func()
	// dropEndpoint caches the drop-box endpoint advertised by dropIssuer,
	// this server's credential issuer. Guarded by mu. See oauth_drop.go.
	dropEndpoint string
	dropIssuer   string
}

// reasonNoCredential is the AuthRequiredError reason for a server that has
// never been logged in to (as opposed to an expired or revoked credential).
const reasonNoCredential = "no stored credential"

// setLastRequired updates lastRequired and fires notify on change.
func (sa *serverAuth) setLastRequired(err *AuthRequiredError) {
	if old := sa.lastRequired.Swap(err); old != err && sa.notify != nil {
		go sa.notify()
	}
}

// newServerAuth builds the auth state for a server. It returns nil when the
// server needs no auth handling at all (stdio transport, or mode "none").
func newServerAuth(name string, cfg ServerConfig, store credentialStore) (*serverAuth, error) {
	if err := cfg.Auth.Validate(); err != nil {
		return nil, fmt.Errorf("server %q: %w", name, err)
	}
	if cfg.Auth.ResolveMode() == AuthModeNone {
		// Nothing is ever sent, so there is no credential to protect and no
		// transport requirement to enforce.
		return nil, nil
	}
	resource, err := canonicalResource(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("server %q: %w", name, err)
	}
	if err := requireSecureTransport(resource); err != nil {
		return nil, fmt.Errorf("server %q: %w", name, err)
	}
	sa := &serverAuth{
		name:     name,
		resource: resource,
		store:    store,
		mu:       newAuthMutex(),
		hc:       &http.Client{Timeout: oauthHTTPTimeout},
	}
	if cfg.Auth != nil {
		sa.cfg = *cfg.Auth
	}
	if sa.cfg.AllowPrivateNetwork {
		// The SDK's newDiscoveryClient (oauthex/oauth2.go:104-113) only
		// installs its hardened defaultDiscoveryTransport — the one whose
		// Control hook refuses private/loopback/link-local peers — when the
		// caller's client has no *http.Transport with a non-nil DialContext
		// or DialTLSContext. A non-nil DialContext is therefore the SDK's
		// documented opt-out signal: it means "the caller owns dialing".
		//
		// So hand it the stdlib default transport with the stdlib default
		// dialer written out explicitly, purely so DialContext is non-nil.
		// Behaviour is otherwise identical to http.DefaultTransport.
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.DialContext = (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext
		sa.hc.Transport = tr
	}
	return sa, nil
}

// requireSecureTransport rejects a credential-bearing MCP endpoint that is not
// HTTPS, unless it is on the loopback interface.
//
// Every token fir attaches — OAuth bearer or static PAT — rides the data path.
// Over plain http:// to a routable host that is cleartext on the wire, and
// `{"url": "http://mcp.internal:8080/mcp"}` is exactly what someone wires up on
// a LAN. oauthex already enforces this for the discovery and token legs; this
// closes the data leg. Loopback stays open so local dev servers work, matching
// oauthex's own rule and RFC 8252's treatment of loopback as a trusted channel.
//
// The escape hatch is `"auth": {"mode": "none"}`, which sends no credentials at
// all and is therefore unaffected.
func requireSecureTransport(resource string) error {
	u, err := url.Parse(resource)
	if err != nil {
		return fmt.Errorf("parse server url: %w", err)
	}
	if u.Scheme == "https" || isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("refusing to send credentials to %q over plaintext %s; "+
		"use https, a loopback address, or set auth.mode to \"none\" if the server needs no credentials",
		resource, u.Scheme)
}

// isLoopbackHost reports whether host (no port) names the loopback interface.
// "localhost" is accepted by name because that is what a dev config writes;
// everything else must parse as a loopback IP.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	// url.Hostname() already strips the brackets from an IPv6 literal.
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// mode returns the resolved auth mode.
func (sa *serverAuth) mode() AuthMode { return sa.cfg.ResolveMode() }

// matches reports whether this auth state is still valid for cfg. A changed
// URL or auth block must not reuse a cached token — see the URL-binding note
// in oauth_store.go.
func (sa *serverAuth) matches(cfg ServerConfig) bool {
	resource, err := canonicalResource(cfg.URL)
	if err != nil || resource != sa.resource {
		return false
	}
	var want AuthConfig
	if cfg.Auth != nil {
		want = *cfg.Auth
	}
	return want.Mode == sa.cfg.Mode &&
		want.Token == sa.cfg.Token &&
		want.ClientID == sa.cfg.ClientID &&
		want.ClientSecret == sa.cfg.ClientSecret &&
		want.RedirectURI == sa.cfg.RedirectURI &&
		want.CallbackListen == sa.cfg.CallbackListen &&
		slices.Equal(want.Scopes, sa.cfg.Scopes) &&
		slices.Equal(want.AuthorizationServers, sa.cfg.AuthorizationServers)
}

// ensureLoadedLocked pulls the persisted credential: lazily on first use, and
// again whenever the stored row has changed since we last loaded or wrote it.
// Called with mu held.
//
// The re-check is what lets several fir processes share one credential. With
// rotating refresh tokens, a process that keeps using its in-memory copy after
// another process refreshed holds a spent refresh token; its next refresh
// fails and used to delete the other process's perfectly good credential. It
// is also what lets a running session pick up a token minted out-of-band by
// `fir mcp login` without a manual /mcp reload. The check is a stat of
// auth.json plus a map lookup.
func (sa *serverAuth) ensureLoadedLocked() {
	if !sa.loaded {
		sa.loaded = true
		sa.adoptLocked(sa.store.Load(sa.name, sa.resource))
		return
	}
	if !sa.store.persistent() {
		return
	}
	sa.store.reloadIfStale()
	stored := sa.store.Load(sa.name, sa.resource)
	if fingerprint(stored) == sa.synced {
		return
	}
	firlog.Debug("mcp oauth: adopting credential written elsewhere", "server", sa.name, "present", stored != nil)
	sa.adoptLocked(stored)
}

// adoptLocked installs a credential read from storage. Called with mu held.
func (sa *serverAuth) adoptLocked(stored *oauthCredential) {
	sa.synced = fingerprint(stored)
	sa.cred = stored
	sa.gen++
	if sa.cred != nil && !sa.credIssuerAllowed(sa.cred.Issuer) {
		// The user has since forced a different authorization server. The
		// stored token was minted by the old issuer and must not be reused —
		// matches() only catches that within one process, because a restart
		// rebuilds serverAuth from the new config and has nothing to compare
		// against. Drop it from memory only: the store's resource binding is
		// its own invariant, and leaving the row alone means flipping the
		// forced issuer back restores a still-valid refresh token instead of
		// forcing a gratuitous browser trip. The next login overwrites it.
		firlog.Debug("mcp oauth: discarding credential minted by a non-forced issuer",
			"server", sa.name, "issuer", sa.cred.Issuer)
		sa.cred = nil
	}
	if sa.cred != nil {
		firlog.Debug("mcp oauth: loaded stored credential", "server", sa.name)
	}
}

// credIssuerAllowed reports whether a stored credential's recorded issuer is
// one the current config would use. It is a no-op unless authorization servers
// are forced: without a forced list any issuer is by definition the one
// discovery would have picked.
//
// Comparison is on canonicalResource of both sides, which absorbs the
// cosmetic differences a hand-written config can have against the issuer the
// authorization server reports (trailing slash, default port, case). It cannot
// be looser than that: oauthex enforces the RFC 8414 issuer match at login, so
// a credential minted under a forced entry records exactly that issuer. An
// empty recorded issuer means unknown provenance and is refused.
func (sa *serverAuth) credIssuerAllowed(issuer string) bool {
	if len(sa.cfg.AuthorizationServers) == 0 {
		return true
	}
	got, err := canonicalResource(issuer)
	if err != nil {
		// Includes the empty issuer: canonicalResource rejects a URL with no
		// scheme or host, so unknown provenance lands here.
		return false
	}
	for _, want := range sa.cfg.AuthorizationServers {
		if c, err := canonicalResource(want); err == nil && c == got {
			return true
		}
	}
	return false
}

// acquire takes the auth lock, honouring ctx. It fails immediately when an
// interactive login holds the lock: waiting for a human to finish in a browser
// is never the right thing for a request with a deadline, and "login in
// progress" is a far more useful error than a context timeout.
func (sa *serverAuth) acquire(ctx context.Context) error {
	if sa.loginInFlight.Load() {
		return fmt.Errorf("MCP server %q: an interactive login is in progress; retry once it completes", sa.name)
	}
	if err := sa.mu.lockCtx(ctx); err != nil {
		return fmt.Errorf("MCP server %q: waiting for the auth lock: %w", sa.name, err)
	}
	// A login may have started between the check and the acquire; in that case
	// we now hold the lock and it is waiting, so there is nothing to bail on.
	return nil
}

// accessToken returns the bearer token to attach to an outgoing request (or ""
// to send unauthenticated) plus the credential generation it came from. It
// refreshes proactively when the cached token is within tokenRefreshWindow of
// expiry.
//
// A refresh failure is not fatal here: the request is sent with whatever we
// still hold and the 401 path decides what to do, which keeps the two recovery
// routes in one place.
func (sa *serverAuth) accessToken(ctx context.Context) (string, uint64, error) {
	if sa.mode() == AuthModeBearer {
		return sa.cfg.BearerToken(), 0, nil
	}
	if err := sa.acquire(ctx); err != nil {
		return "", 0, err
	}
	defer sa.mu.unlock()
	sa.ensureLoadedLocked()
	if sa.cred == nil || sa.cred.Token == nil {
		if sa.mode() == AuthModeOAuth {
			// Forced OAuth: do not leak an unauthenticated request.
			return "", sa.gen, sa.requireLoginLocked(reasonNoCredential, nil)
		}
		return "", sa.gen, nil
	}
	if sa.cred.Token.ExpiresWithin(tokenRefreshWindow) {
		if err := sa.refreshLocked(ctx, false); err != nil {
			firlog.Debug("mcp oauth: proactive refresh failed", "server", sa.name, "err", err)
			if sa.cred == nil || sa.cred.Token == nil {
				return "", sa.gen, sa.requireLoginLocked("token refresh failed", err)
			}
		}
	}
	return sa.cred.Token.AccessToken, sa.gen, nil
}

// sameOrigin reports whether u targets the same scheme+host as the credential's
// canonical resource. See oauthRoundTripper.send for why this matters.
func (sa *serverAuth) sameOrigin(u *url.URL) bool {
	if u == nil {
		return false
	}
	got, err := canonicalResource((&url.URL{Scheme: u.Scheme, Host: u.Host}).String())
	if err != nil {
		return false
	}
	want, err := canonicalResource(sa.resource)
	if err != nil {
		return false
	}
	wantURL, err := url.Parse(want)
	if err != nil {
		return false
	}
	return got == (&url.URL{Scheme: wantURL.Scheme, Host: wantURL.Host}).String()
}

// handleUnauthorized reacts to a 401 from the MCP server. It returns the token
// to retry with, or an error when the request must fail. Called with mu NOT
// held; concurrent 401s serialise on mu and a caller whose generation is stale
// simply retries with the token the winner obtained.
func (sa *serverAuth) handleUnauthorized(ctx context.Context, headers []string, sentGen uint64) (string, error) {
	if err := sa.acquire(ctx); err != nil {
		return "", err
	}
	defer sa.mu.unlock()
	sa.ensureLoadedLocked()
	if ci := parseChallenge(headers); ci.ResourceMetadata != "" || ci.Scope != "" {
		sa.challenge = ci
	}

	// Another in-flight request may already have recovered while we waited on
	// the mutex. Its work is newer than ours, so just use the result.
	if sentGen != sa.gen && sa.cred != nil && sa.cred.Token != nil && sa.cred.Token.AccessToken != "" {
		return sa.cred.Token.AccessToken, nil
	}

	if sa.cred != nil && sa.cred.Token != nil && sa.cred.Token.RefreshToken != "" {
		err := sa.refreshLocked(ctx, true)
		if err == nil && sa.cred != nil && sa.cred.Token != nil {
			return sa.cred.Token.AccessToken, nil
		}
		firlog.Debug("mcp oauth: refresh after 401 failed", "server", sa.name, "err", err)
		if err != nil && !isRefreshRejected(err) && !errors.Is(err, errCredentialRemoved) {
			// Network blip, token endpoint down, 5xx: the refresh token may
			// well still be good. Keep it and fail this request; the next
			// one (or the reconnect loop) tries again.
			return "", fmt.Errorf("MCP server %q: token refresh failed: %w", sa.name, err)
		}
		// The authorization server rejected the refresh token (refreshLocked
		// has deleted it from storage), or the stored credential is gone: a
		// full re-login is the only way back.
		return "", sa.requireLoginLocked("stored credentials are no longer valid", err)
	}

	reason := reasonNoCredential
	if sa.cred != nil {
		reason = "stored access token was rejected and no refresh token is available"
		sa.dropCredentialLocked()
	}
	return "", sa.requireLoginLocked(reason, nil)
}

// dropCredentialLocked forgets the cached credential and deletes it from
// storage. Called with mu held.
//
// The delete is compare-and-delete under the auth.json lock: a row another
// process rewrote since we loaded it is someone else's fresh credential and is
// adopted instead of deleted.
func (sa *serverAuth) dropCredentialLocked() {
	failed := sa.synced
	sa.cred = nil
	sa.gen++
	if !sa.store.persistent() {
		return
	}
	var adopted bool
	var fresh *oauthCredential
	err := sa.store.Update(sa.name, sa.resource, func(cur *oauthCredential) (*oauthCredential, bool, error) {
		if fingerprint(cur) != failed {
			adopted, fresh = true, cur
			return nil, false, nil
		}
		return nil, true, nil
	})
	if err != nil {
		firlog.Warn("mcp oauth: failed to delete stale credential", "server", sa.name, "err", err)
		return
	}
	if adopted {
		sa.adoptLocked(fresh)
		return
	}
	sa.synced = ""
}

// requireLoginLocked records and returns an AuthRequiredError. Called with mu held.
func (sa *serverAuth) requireLoginLocked(reason string, cause error) *AuthRequiredError {
	err := &AuthRequiredError{Server: sa.name, Resource: sa.resource, Reason: reason, Cause: cause}
	sa.pending = err
	sa.setLastRequired(err)
	return err
}

// takePending returns and clears any AuthRequiredError recorded by the
// transport, so the dial path can act on it after a failed Connect.
//
// The recorded error is authoritative rather than the one returned from
// RoundTrip: the SDK wraps transport errors through several layers and we do
// not want to depend on every one of them preserving %w.
//
// Known minor: this, clearPending and forget take the lock unconditionally, so
// a background reconnect dial parks here for the duration of an in-flight
// `/mcp login`. That stalls a background goroutine only — no deadlock, nothing
// the user sees — but they should migrate to lockCtx alongside the request
// path.
func (sa *serverAuth) takePending() *AuthRequiredError {
	sa.mu.lock()
	defer sa.mu.unlock()
	p := sa.pending
	sa.pending = nil
	return p
}

// clearPending discards any recorded auth requirement. Called after a
// successful dial so a spurious 401 on the background SSE stream cannot be
// mistaken later for the cause of an unrelated connection failure.
func (sa *serverAuth) clearPending() {
	sa.mu.lock()
	defer sa.mu.unlock()
	sa.pending = nil
	sa.setLastRequired(nil)
}

// invalidate drops the lazily-loaded credential so the next use re-reads it
// from storage. Unlike forget it does not clear the discovered challenge or
// touch storage: this is a cache invalidation, not a logout. It exists so an
// explicit reload picks up a token minted out-of-band by another process
// (`fir mcp login` in a second terminal) — without it, a serverAuth that
// already looked and found nothing never looks again.
//
// Non-blocking: if the auth lock is held — an interactive login is running, or
// a request is mid-refresh — the cache is left alone. Whoever holds the lock is
// about to write a fresher credential than the one on disk anyway, and
// Manager.Reload must never park behind a human in a browser. Skipping is
// degraded, not silent: the reload still re-dials the server, that dial reports
// AuthRequiredError, and a second /mcp reload picks the token up.
//
// gen++ is deliberate. An in-flight request that already sent the old token
// will reach handleUnauthorized with a stale generation and take the "someone
// else recovered, retry with theirs" branch, using whatever was just re-read
// from disk. If that is the freshly minted token, exactly right; if disk still
// holds the same rejected token, the cost is one wasted retry and a plain 401
// instead of an AuthRequiredError, which the next dial corrects. Without gen++
// that request would instead fall through to the drop path and delete a
// just-minted credential from storage — strictly worse.
//
// Note this also re-reads for healthy connected servers, replacing a live
// credential with the persisted one. They are the same unless an earlier
// refresh failed to persist, in which case the reload downgrades the session to
// login-required — the same exposure a restart has.
func (sa *serverAuth) invalidate() {
	if !sa.mu.tryLock() {
		return
	}
	defer sa.mu.unlock()
	sa.cred = nil
	sa.loaded = false
	sa.gen++
}

// forget drops the cached credential without touching storage. The caller
// (Manager.LogoutServer) deletes the persisted copy.
func (sa *serverAuth) forget() {
	sa.mu.lock()
	defer sa.mu.unlock()
	sa.cred = nil
	sa.loaded = false
	sa.pending = nil
	sa.gen++
}

// errCredentialRemoved reports that the stored credential was deleted by
// another process (e.g. `fir mcp logout`) while we held a copy in memory.
var errCredentialRemoved = errors.New("stored credential was removed")

// isRefreshRejected reports whether a refresh failure is the authorization
// server definitively rejecting the grant (invalid_grant and friends), as
// opposed to a transport failure or server error that may clear on retry.
func isRefreshRejected(err error) bool {
	var te *pinoauth.TokenError
	if !errors.As(err, &te) {
		return false
	}
	return te.Code != "" || te.HTTPStatus == http.StatusBadRequest || te.HTTPStatus == http.StatusUnauthorized
}

// refreshLocked exchanges the stored refresh token for a fresh access token
// and persists the result. Called with mu held.
//
// With a persistent store the whole read-decide-refresh-write runs under the
// cross-process auth.json lock, mirroring the provider refresh path:
//
//   - If the stored row is not the one we last saw, another process (or
//     another Manager) already refreshed or re-logged in. Its credential is
//     adopted and no grant is spent — spending ours would replay a rotated
//     refresh token, which servers answer with invalid_grant and, with reuse
//     detection, by revoking the whole token family.
//   - Otherwise refresh, and persist the rotated token before releasing the
//     lock. Dropping a rotated refresh token strands the user at restart.
//   - When dropOnReject is set and the server rejects the grant, the dead row
//     is deleted in the same critical section, so it can never clobber a
//     credential written concurrently by someone else.
//
// No issuer check is needed here: a credential only reaches this point after
// ensureLoadedLocked's gate, so its TokenURL belongs to an issuer the current
// config permits.
func (sa *serverAuth) refreshLocked(ctx context.Context, dropOnReject bool) error {
	if sa.cred == nil || sa.cred.Token == nil || sa.cred.Token.RefreshToken == "" {
		return errors.New("no refresh token")
	}
	if sa.cred.TokenURL == "" {
		return errors.New("no token endpoint recorded for this credential")
	}
	if !sa.store.persistent() {
		tok, err := sa.grant(ctx)
		if err != nil {
			return err
		}
		sa.applyRefreshedLocked(tok)
		sa.synced = fingerprint(sa.cred)
		return nil
	}
	var (
		adopted    bool
		fresh      *oauthCredential
		refreshErr error
		dropped    bool
	)
	err := sa.store.Update(sa.name, sa.resource, func(cur *oauthCredential) (*oauthCredential, bool, error) {
		if fingerprint(cur) != sa.synced {
			adopted, fresh = true, cur
			return nil, false, nil
		}
		tok, err := sa.grant(ctx)
		if err != nil {
			refreshErr = err
			if dropOnReject && isRefreshRejected(err) {
				dropped = true
				return nil, true, nil
			}
			return nil, false, nil
		}
		sa.applyRefreshedLocked(tok)
		return sa.cred, true, nil
	})
	switch {
	case adopted:
		firlog.Debug("mcp oauth: credential refreshed elsewhere; adopting it", "server", sa.name)
		sa.adoptLocked(fresh)
		if sa.cred == nil {
			return errCredentialRemoved
		}
		return nil
	case refreshErr != nil:
		if dropped && err == nil {
			sa.cred = nil
			sa.synced = ""
			sa.gen++
		}
		return refreshErr
	case err != nil:
		// The rotated token lives on in memory; synced still matches the
		// old row on disk, so it is not mistaken for someone else's write.
		return fmt.Errorf("persist refreshed token: %w", err)
	}
	sa.synced = fingerprint(sa.cred)
	firlog.Debug("mcp oauth: refreshed token", "server", sa.name)
	return nil
}

// grant performs the refresh-token grant for the in-memory credential.
func (sa *serverAuth) grant(ctx context.Context) (*pinoauth.Token, error) {
	client := &pinoauth.Client{
		TokenURL:     sa.cred.TokenURL,
		ClientID:     sa.cred.ClientID,
		ClientSecret: sa.cred.ClientSecret,
		HTTPClient:   sa.tokenHTTPClient(),
	}
	return client.Refresh(ctx, pinoauth.RefreshRequest{
		RefreshToken: sa.cred.Token.RefreshToken,
		Extra:        url.Values{"resource": []string{sa.resource}},
	})
}

// applyRefreshedLocked installs a freshly granted token. Called with mu held.
func (sa *serverAuth) applyRefreshedLocked(tok *pinoauth.Token) {
	if tok.RefreshToken == "" {
		// Non-rotating server: keep the refresh token we already hold.
		tok.RefreshToken = sa.cred.Token.RefreshToken
	}
	if tok.Scope == "" {
		tok.Scope = sa.cred.Scope
	}
	sa.cred.Token = tok
	sa.cred.Scope = tok.Scope
	sa.gen++
}

// tokenHTTPClient returns the HTTP client used for token-endpoint requests. It
// refuses redirects, because a redirected token POST would re-send the body
// (carrying client_secret, refresh_token, code_verifier) to the new target.
func (sa *serverAuth) tokenHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   oauthHTTPTimeout,
		Transport: sa.hc.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return pinoauth.ErrRedirectNotAllowed
		},
	}
}

// probeChallenge issues one unauthenticated request to the MCP endpoint to
// collect the WWW-Authenticate challenge from the resulting 401. It is
// entirely best-effort: any failure (server down, no challenge, a 200 because
// the server is not actually protected) leaves the challenge empty and
// discovery falls back to well-known path derivation.
//
// The request is a real `initialize` JSON-RPC envelope — the same first
// message the transport would send — so a server that gates on method or body
// shape still answers the way it would in a normal session.
func (sa *serverAuth) probeChallenge(ctx context.Context) {
	// Known minor: the version is hard-coded "dev" here, matching the
	// clientInfo the SDK transport sends (see dialAndInitialize). Cosmetic —
	// nothing keys on it — but both should carry the real build version.
	const initialize = `{"jsonrpc":"2.0","id":0,"method":"initialize",` +
		`"params":{"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"fir","version":"dev"}}}`

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sa.resource, strings.NewReader(initialize))
	if err != nil {
		firlog.Debug("mcp oauth: challenge probe not built", "server", sa.name, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := sa.hc.Do(req)
	if err != nil {
		firlog.Debug("mcp oauth: challenge probe failed", "server", sa.name, "err", err)
		return
	}
	defer drainAndClose(resp)
	if resp.StatusCode != http.StatusUnauthorized {
		firlog.Debug("mcp oauth: challenge probe was not a 401",
			"server", sa.name, "status", resp.StatusCode)
		return
	}
	ci := parseChallenge(resp.Header.Values("WWW-Authenticate"))
	if ci.ResourceMetadata == "" && ci.Scope == "" {
		return
	}
	sa.mu.lock()
	defer sa.mu.unlock()
	sa.challenge = ci
	firlog.Debug("mcp oauth: challenge probe found metadata",
		"server", sa.name, "resource_metadata", ci.ResourceMetadata)
}

// Login runs the full interactive OAuth flow: discovery, dynamic client
// registration, authorization-code grant with PKCE, token exchange, persist.
//
// It holds the server's auth mutex for the duration — minutes, while a human
// is in a browser — which is what stops a burst of tool calls opening N browser
// windows. Requests arriving meanwhile do not queue behind it: acquire sees
// loginInFlight and fails them fast with a "login in progress" error rather
// than letting them sit on the lock until their deadlines expire.
//
// drops, when non-nil, finds a drop-box on another server for Case A logins
// (see oauth_drop.go).
func (sa *serverAuth) Login(ctx context.Context, ui pinoauth.LoginCallbacks, drops dropAccessFunc) error {
	if err := sa.mu.lockCtx(ctx); err != nil {
		return fmt.Errorf("MCP server %q login: %w", sa.name, err)
	}
	defer sa.mu.unlock()
	sa.loginInFlight.Store(true)
	defer sa.loginInFlight.Store(false)
	sa.pending = nil
	err := sa.loginLocked(ctx, ui, drops)
	if err != nil {
		return fmt.Errorf("MCP server %q login: %w", sa.name, err)
	}
	sa.setLastRequired(nil)
	return nil
}

// callbackTarget resolves where the loopback callback server listens and what
// redirect URI the authorization server is told about. Without a pinned
// auth.redirect_uri it is an ephemeral 127.0.0.1 port and the caller fills the
// URI in from the resolved address — unless FIR_MCP_OAUTH_REDIRECT_BASE names
// a public proxy base, in which case fir picks a free loopback port P and
// uses "<base>/P/callback". With a pinned URI, it is used verbatim — an
// authorization server that does not honour RFC 8252 §7.3 port variance
// compares it literally — and the listener binds its host and port, or
// auth.callback_listen for an https URI served by a proxy.
func (sa *serverAuth) callbackTarget() (redirectURI, route, listenAddr string, err error) {
	if sa.cfg.RedirectURI == "" {
		// A pre-registered client relies on RFC 8252 loopback port variance;
		// a derived public URI would not match its registration.
		base := os.Getenv(envRedirectBase)
		if base == "" || sa.cfg.ClientID != "" {
			return "", callbackRoute, "127.0.0.1:0", nil
		}
		return proxiedCallbackTarget(base)
	}
	u, listen, err := validateRedirectURI(sa.cfg.RedirectURI, sa.cfg.CallbackListen)
	if err != nil {
		return "", "", "", err
	}
	return sa.cfg.RedirectURI, u.Path, listen, nil
}

// reserveLoopbackPort returns a 127.0.0.1 port that is free right now. A
// variable so tests can force the rebind race.
var reserveLoopbackPort = func() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return port, ln.Close()
}

// envRedirectBase names the public base URL a reverse proxy forwards to
// loopback callback servers; see proxiedCallbackTarget.
const envRedirectBase = "FIR_MCP_OAUTH_REDIRECT_BASE"

// proxiedCallbackTarget derives a callback target from a proxy base URL such
// as "https://box.example/.agentbox/oauth-cb": it reserves a free loopback
// port P and returns "<base>/P/callback", whose path the listener on
// 127.0.0.1:P serves. The proxy forwards that path unchanged. The port is
// released before the callback server rebinds it — the same unavoidable race
// a pinned port has.
func proxiedCallbackTarget(base string) (redirectURI, route, listenAddr string, err error) {
	if !strings.HasPrefix(base, "https://") {
		return "", "", "", fmt.Errorf("%s=%q must be an https URL", envRedirectBase, base)
	}
	port, err := reserveLoopbackPort()
	if err != nil {
		return "", "", "", fmt.Errorf("%s: reserve a loopback port: %w", envRedirectBase, err)
	}
	listen := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	uri := fmt.Sprintf("%s/%d/callback", strings.TrimRight(base, "/"), port)
	u, _, err := validateRedirectURI(uri, listen)
	if err != nil {
		return "", "", "", fmt.Errorf("%s=%q: %w", envRedirectBase, base, err)
	}
	return uri, u.Path, listen, nil
}

// startCallbackServer binds the loopback callback server and returns the
// redirect URI it answers. A port fir picked for FIR_MCP_OAUTH_REDIRECT_BASE
// is released before the rebind, so a lost race retries with a fresh port; a
// pinned port has no alternative and fails with the knob to turn.
func (sa *serverAuth) startCallbackServer(ctx context.Context, state string) (*http.Server, <-chan *pinoauth.CallbackResult, string, error) {
	for attempt := 1; ; attempt++ {
		redirectURI, route, listenAddr, err := sa.callbackTarget()
		if err != nil {
			return nil, nil, "", err
		}
		srv, resultCh, addr, err := pinoauth.StartCallbackServer(ctx, route, listenAddr, state)
		if err == nil {
			if redirectURI == "" {
				redirectURI = "http://" + addr + route
			}
			return srv, resultCh, redirectURI, nil
		}
		if redirectURI == "" {
			return nil, nil, "", fmt.Errorf("start loopback callback server: %w", err)
		}
		source := "auth.redirect_uri"
		if sa.cfg.RedirectURI == "" {
			if attempt < 3 && errors.Is(err, syscall.EADDRINUSE) {
				continue
			}
			source = envRedirectBase
		}
		return nil, nil, "", fmt.Errorf("start loopback callback server on %s (for %s %q): %w; "+
			"free that port or register a different redirect URI", listenAddr, source, redirectURI, err)
	}
}

func (sa *serverAuth) loginLocked(ctx context.Context, ui pinoauth.LoginCallbacks, drops dropAccessFunc) error {
	// Refresh first: a stored refresh token that still works needs no browser.
	sa.ensureLoadedLocked()
	if sa.cred != nil && sa.cred.Token != nil && sa.cred.Token.RefreshToken != "" {
		progress(ui, "Refreshing the stored credential…")
		err := sa.refreshLocked(ctx, true)
		if err == nil && sa.cred != nil && sa.cred.Token != nil {
			progress(ui, "Stored credential refreshed; no browser login needed.")
			firlog.Info("mcp oauth: login satisfied by refresh", "server", sa.name)
			return nil
		}
		firlog.Debug("mcp oauth: refresh before login failed", "server", sa.name, "err", err)
	}

	progress(ui, fmt.Sprintf("Discovering OAuth configuration for %s…", sa.resource))
	prm, prmURL, err := discoverResourceMetadataDoc(ctx, sa.hc, sa.resource, sa.challenge)
	if err != nil {
		if len(sa.cfg.AuthorizationServers) == 0 {
			return err
		}
		// Forced issuers: the protected-resource metadata is exactly what the
		// user is routing around, so its absence must not abort the login. It
		// is still attempted rather than skipped, because scopes_supported is
		// useful even when authorization_servers is not; resolveScope
		// tolerates the nil.
		firlog.Debug("mcp oauth: ignoring protected-resource metadata failure, issuers are forced",
			"server", sa.name, "err", err)
		prm, prmURL = nil, ""
	}
	asm, asmURL, err := discoverAuthServerDoc(ctx, sa.hc, sa.resolveIssuers(prm))
	if err != nil {
		return err
	}
	scope := sa.resolveScope(prm)
	state := pinoauth.GenerateState()
	cbCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	clientID, clientSecret := sa.cfg.ClientID, expandEnvRef(sa.cfg.ClientSecret)
	register := func(redirectURI string) error {
		if sa.cfg.ClientID != "" {
			return nil
		}
		progress(ui, "Registering fir with the authorization server…")
		reg, err := registerClient(ctx, sa.hc, asm, redirectURI, scope)
		if err != nil {
			return err
		}
		clientID, clientSecret = reg.ClientID, reg.ClientSecret
		if reg.Scope != "" {
			scope = reg.Scope
		}
		return nil
	}

	var (
		redirectURI string
		resultCh    <-chan *pinoauth.CallbackResult
		dropErr     error
		dropped     bool
	)
	if slot := sa.openDropSlot(ctx, asm, asmURL, prmURL, state, drops); slot != nil {
		if err := register(slot.redirectURI()); err != nil {
			firlog.Debug("mcp oauth: registration rejected the drop-box redirect; using loopback",
				"server", sa.name, "err", err)
		} else {
			ch := make(chan *pinoauth.CallbackResult, 1)
			go slot.await(cbCtx, sa.hc, ch, &dropErr)
			redirectURI, resultCh, dropped = slot.redirectURI(), ch, true
		}
	}
	if resultCh == nil {
		// Bind the loopback listener before registration, which registers
		// its redirect URI verbatim.
		srv, ch, uri, err := sa.startCallbackServer(cbCtx, state)
		if err != nil {
			return err
		}
		defer srv.Close() //nolint:errcheck // best-effort; cbCtx cancel also closes it
		if err := register(uri); err != nil {
			return err
		}
		redirectURI, resultCh = uri, ch
	}

	pkce := pinoauth.GeneratePKCE()
	authURL, err := buildAuthorizeURL(asm.AuthorizationEndpoint, authorizeParams{
		ClientID:    clientID,
		RedirectURI: redirectURI,
		State:       state,
		Scope:       scope,
		Resource:    sa.resource,
		PKCE:        pkce,
	})
	if err != nil {
		return err
	}

	if ui.OnAuth != nil {
		ui.OnAuth(pinoauth.AuthInfo{
			URL:          authURL,
			Instructions: sa.instructions(redirectURI, dropped),
		})
	}
	code, gotState, err := pinoauth.AwaitAuthCode(ctx, resultCh, ui.OnManualCodeInput, ui.OnDismissManualInput)
	if err != nil {
		if errors.Is(err, pinoauth.ErrCallbackClosed) && dropErr != nil {
			err = dropErr
		}
		return fmt.Errorf("waiting for authorization code: %w", err)
	}
	// The loopback server validates state itself; a pasted or dropped code
	// does not go through it, so validate here too. An empty state means the
	// user pasted a bare code, which carries no state to check.
	if gotState != "" && gotState != state {
		return errors.New("authorization state mismatch")
	}

	progress(ui, "Exchanging authorization code…")
	client := &pinoauth.Client{
		TokenURL:     asm.TokenEndpoint,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		HTTPClient:   sa.tokenHTTPClient(),
	}
	tok, err := client.Exchange(ctx, pinoauth.ExchangeRequest{
		Code:         code,
		CodeVerifier: pkce.Verifier,
		RedirectURI:  redirectURI,
		Extra:        url.Values{"resource": []string{sa.resource}},
	})
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}
	if tok.AccessToken == "" {
		return errors.New("token endpoint returned no access_token")
	}
	if tok.Scope == "" {
		tok.Scope = scope
	}

	sa.cred = &oauthCredential{
		Resource:     sa.resource,
		Token:        tok,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     asm.TokenEndpoint,
		AuthURL:      asm.AuthorizationEndpoint,
		Issuer:       asm.Issuer,
		Scope:        tok.Scope,
	}
	sa.loaded = true
	sa.gen++
	if err := sa.store.Save(sa.name, sa.cred); err != nil {
		return fmt.Errorf("persist token: %w", err)
	}
	sa.synced = fingerprint(sa.cred)
	firlog.Info("mcp oauth: login complete", "server", sa.name, "issuer", asm.Issuer)
	return nil
}

// openDropSlot picks a drop-box for this login, or returns nil to use the
// loopback listener. A pinned redirect URI or FIR_MCP_OAUTH_REDIRECT_BASE
// wins. Case B: the server's own metadata advertises a drop endpoint on the
// authorization server's host, which accepts redirects under it without a
// token. Case A: another logged-in server's drop-box, opened with its token;
// it needs dynamic client registration.
func (sa *serverAuth) openDropSlot(ctx context.Context, asm *oauthex.AuthServerMeta, asmURL, prmURL, state string, drops dropAccessFunc) *dropSlot {
	if sa.cfg.RedirectURI != "" || os.Getenv(envRedirectBase) != "" {
		return nil
	}
	ep := fetchDropEndpoint(ctx, sa.hc, asmURL)
	if ep == "" {
		ep = fetchDropEndpoint(ctx, sa.hc, prmURL)
	}
	if ep != "" && sameHost(ep, asm.AuthorizationEndpoint) {
		slot, err := newDropSlot(ep, "", state)
		if err != nil {
			return nil
		}
		firlog.Debug("mcp oauth: using own drop-box", "server", sa.name, "endpoint", ep)
		return slot
	}
	if drops == nil || sa.cfg.ClientID != "" || asm.RegistrationEndpoint == "" {
		return nil
	}
	da, ok := drops(ctx)
	if !ok {
		return nil
	}
	slot, err := newDropSlot(da.Endpoint, da.Token, state)
	if err != nil {
		return nil
	}
	if err := slot.open(ctx, sa.hc); err != nil {
		firlog.Debug("mcp oauth: drop-box slot not opened; using loopback", "server", sa.name, "err", err)
		return nil
	}
	firlog.Debug("mcp oauth: using drop-box", "server", sa.name, "endpoint", da.Endpoint)
	return slot
}

// sameHost reports whether two URLs name the same host and port.
func sameHost(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && ua.Host != "" && strings.EqualFold(ua.Host, ub.Host)
}

// resolveScope picks the scope to request. An explicitly configured scope wins
// (it is an escape hatch for servers whose advertised metadata is wrong), then
// the scope named in the 401 challenge, then the protected-resource metadata's
// scopes_supported. When none apply the parameter is omitted entirely rather
// than sent empty.
func (sa *serverAuth) resolveScope(prm *oauthex.ProtectedResourceMetadata) string {
	if len(sa.cfg.Scopes) > 0 {
		return strings.Join(sa.cfg.Scopes, " ")
	}
	if sa.challenge.Scope != "" {
		return sa.challenge.Scope
	}
	if prm != nil && len(prm.ScopesSupported) > 0 {
		return strings.Join(prm.ScopesSupported, " ")
	}
	return ""
}

// resolveIssuers picks the authorization-server issuers to try, in order. An
// explicitly configured list wins outright — it *replaces* the candidates the
// protected-resource metadata would yield rather than extending them, because
// it exists precisely for servers whose metadata is absent, wrong or
// unreachable. Otherwise the normal chain applies: the PRM's
// authorization_servers, else the resource's own origin.
func (sa *serverAuth) resolveIssuers(prm *oauthex.ProtectedResourceMetadata) []string {
	if len(sa.cfg.AuthorizationServers) > 0 {
		return sa.cfg.AuthorizationServers
	}
	return issuerCandidates(sa.resource, prm)
}

// authorizeParams are the inputs to the authorization-endpoint URL.
type authorizeParams struct {
	ClientID    string
	RedirectURI string
	State       string
	Scope       string
	Resource    string
	PKCE        *pinoauth.PKCEChallenge
}

// buildAuthorizeURL assembles the RFC 6749 §4.1.1 authorization request, with
// PKCE (RFC 7636) and the RFC 8707 resource indicator.
func buildAuthorizeURL(endpoint string, p authorizeParams) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse authorization endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", p.RedirectURI)
	q.Set("state", p.State)
	q.Set("code_challenge", p.PKCE.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("resource", p.Resource)
	if p.Scope != "" {
		q.Set("scope", p.Scope)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func progress(ui pinoauth.LoginCallbacks, msg string) {
	if ui.OnProgress != nil {
		ui.OnProgress(msg)
	}
}

// oauthRoundTripper is the silent half of MCP OAuth: it attaches bearer tokens
// and recovers from a 401 without ever prompting a human. See the package
// comment at the top of this file.
type oauthRoundTripper struct {
	base http.RoundTripper
	sa   *serverAuth
}

func (t *oauthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Capture the body up front so the 401 retry can replay it. The SDK builds
	// its POSTs from a bytes.Reader, so GetBody is always populated there; GET
	// (the standalone SSE stream) and DELETE (session teardown) have no body.
	replayable := req.Body == nil || req.GetBody != nil

	token, gen, err := t.sa.accessToken(req.Context())
	if err != nil {
		return nil, err
	}
	resp, err := t.send(req, token)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	headers := resp.Header.Values("WWW-Authenticate")
	if !replayable {
		return resp, nil
	}
	if t.sa.mode() == AuthModeBearer {
		// A static token has no recovery path and nothing to log in to.
		// Say so instead of sending the user to `fir mcp login`.
		drainAndClose(resp)
		return nil, fmt.Errorf("static bearer token for MCP server %q was rejected (HTTP 401); check auth.token",
			t.sa.name)
	}
	// Drain and close before retrying so the connection can be reused.
	drainAndClose(resp)

	newToken, err := t.sa.handleUnauthorized(req.Context(), headers, gen)
	if err != nil {
		return nil, err
	}
	return t.send(req, newToken)
}

// send dispatches a copy of req with the given bearer token attached. The
// original request is never mutated — net/http may still hold it. The body is
// rewound via GetBody when available so a retry replays it.
//
// The token is attached only when the request targets the origin the
// credential was minted for. net/http's own "strip Authorization on
// cross-host redirect" protection runs *above* the transport, so without this
// check a redirected request would arrive here and be re-stamped with the
// bearer for whatever host it now points at. authHTTPClient also refuses to
// follow redirects; this is the second lock on the same door.
func (t *oauthRoundTripper) send(req *http.Request, token string) (*http.Response, error) {
	r := req.Clone(req.Context())
	if req.Body != nil && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("rewind request body: %w", err)
		}
		r.Body = body
	}
	if token != "" && t.sa.sameOrigin(req.URL) {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return t.base.RoundTrip(r)
}

// drainAndClose consumes a bounded prefix of a response body and closes it, so
// the underlying connection returns to the idle pool instead of being torn
// down. The bound stops a hostile server from making us read forever.
func drainAndClose(resp *http.Response) {
	if resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
}

// authHTTPClient builds the *http.Client an HTTP MCP transport should use.
// Returns nil when the server needs no auth handling, which leaves the SDK's
// default client in place.
func authHTTPClient(sa *serverAuth) *http.Client {
	if sa == nil {
		return nil
	}
	return &http.Client{
		// No Timeout: streamable/SSE transports hold long-lived responses, and
		// http.Client.Timeout covers the whole body read, not just the headers.
		Transport: &oauthRoundTripper{base: http.DefaultTransport, sa: sa},
		// The MCP data path has no legitimate redirect. Following one would
		// re-issue the request (bearer token and all) against a location the
		// server chose; returning the 30x instead makes the SDK fail loudly.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// newCredentialStore adapts an auth.AuthStorage for MCP credential storage.
// A nil storage yields a store that keeps nothing — useful in tests and in
// embedded uses with no agent directory.
func newCredentialStore(storage *auth.AuthStorage) credentialStore {
	return credentialStore{storage: storage}
}

// instructions picks the login instructions: a drop-box login needs nothing
// but the link.
func (sa *serverAuth) instructions(redirectURI string, dropped bool) string {
	if dropped {
		return fmt.Sprintf("Authorize fir to access the MCP server %q. Open the link on any device and approve;"+
			" fir picks up the result automatically.", sa.name)
	}
	return loginInstructions(sa.name, redirectURI)
}

// loginInstructions tells the user how to finish a login. With a loopback
// redirect the final page only loads in a browser on this host; from a phone
// or another machine it fails, and the user must paste that page's address
// (or just its code= value) back. Saying so up front is the difference
// between a 30-second login and a confused user.
func loginInstructions(server, redirectURI string) string {
	msg := fmt.Sprintf("Authorize fir to access the MCP server %q.", server)
	u, err := url.Parse(redirectURI)
	if err != nil || !isLoopbackHost(u.Hostname()) {
		return msg
	}
	return msg + " If you approve on another device, the final page will not load — that is expected:" +
		" copy its full address (or just the code= value) and paste it back here."
}
