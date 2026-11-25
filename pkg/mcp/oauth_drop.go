package mcp

// OAuth code drop-box: a public callback that stores the authorization
// response so fir can poll for it from anywhere agentbox is reachable.
//
// A loopback redirect_uri (http://127.0.0.1:<port>/...) only works when the
// browser runs on fir's host; on a phone the final page fails and the user has
// to paste its address back. An authorization server that advertises
// "agentbox_oauth_drop_endpoint" in its metadata hosts a drop-box instead:
//
//   - Slot id = lowercase hex sha256 of a 256-bit poll secret fir keeps.
//   - Case B (logging in to the drop-box host itself): the authorization
//     server accepts any redirect_uri under its own drop prefix and opens the
//     slot when the owner approves. No token is needed.
//   - Case A (any other server with dynamic client registration): fir opens
//     the slot first with POST <endpoint><id> and a Bearer token for the
//     drop-box host, then uses <endpoint><id> as the redirect_uri.
//   - fir long-polls GET <endpoint><id>/poll with header Agentbox-Drop-Secret;
//     200 {"query": "<raw query>"} delivers the result, 204 means pending,
//     404 means unknown, expired or consumed.
//
// PKCE and state are unchanged: the dropped code is useless without fir's
// verifier, and state is still checked. When no drop endpoint is known, the
// slot cannot be opened, or registration rejects the redirect_uri, fir falls
// back to the loopback listener plus paste.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/pinoauth"
)

const (
	// dropMetadataKey is the authorization-server (and protected-resource)
	// metadata field naming the drop-box endpoint.
	dropMetadataKey = "agentbox_oauth_drop_endpoint"
	// dropSecretHeader carries the poll secret whose sha256 is the slot id.
	dropSecretHeader = "Agentbox-Drop-Secret"
)

// dropPollRetryDelay is the pause after a failed poll request before trying
// again. A variable so tests can shorten it.
var dropPollRetryDelay = 2 * time.Second

// dropPollClient is the HTTP client for drop-box polls. The server long-polls
// for up to 25 s, so the timeout must exceed that.
func dropPollClient(base *http.Client) *http.Client {
	return &http.Client{
		Timeout:   40 * time.Second,
		Transport: base.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// fetchDropEndpoint reads the drop endpoint advertised in the JSON metadata
// document at docURL. It returns "" when the document cannot be read or names
// no usable endpoint.
func fetchDropEndpoint(ctx context.Context, hc *http.Client, docURL string) string {
	if docURL == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, docURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		firlog.Debug("mcp oauth: drop endpoint lookup failed", "url", docURL, "err", err)
		return ""
	}
	defer drainAndClose(resp)
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var doc map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return ""
	}
	raw, _ := doc[dropMetadataKey].(string)
	return validDropEndpoint(raw, docURL)
}

// validDropEndpoint returns raw when it is a usable drop endpoint advertised
// by the document at docURL: https (or loopback, for development), on the same
// host as the document, with a path ending in "/". Anything else is ignored —
// a metadata document cannot point fir's callbacks at a different host.
func validDropEndpoint(raw, docURL string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/") {
		return ""
	}
	doc, err := url.Parse(docURL)
	if err != nil || !strings.EqualFold(u.Host, doc.Host) {
		return ""
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return ""
	}
	return raw
}

// dropSlot is one drop-box slot fir is waiting on.
type dropSlot struct {
	endpoint string
	id       string
	secret   string
	// bearer is the drop-box host's access token, set for Case A slots.
	bearer string
	// state is the OAuth state the dropped response must carry.
	state string
}

// dropSlotTTL is how long the server keeps a slot; fir stops waiting after it.
const dropSlotTTL = 10 * time.Minute

// newDropSlot makes a slot with a fresh 256-bit poll secret.
func newDropSlot(endpoint, bearer, state string) (*dropSlot, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generate drop-box secret: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(secret))
	return &dropSlot{endpoint: endpoint, id: hex.EncodeToString(sum[:]), secret: secret, bearer: bearer, state: state}, nil
}

// redirectURI is the callback URL the authorization server sends the browser to.
func (s *dropSlot) redirectURI() string { return s.endpoint + s.id }

// open creates the slot on the server (Case A).
func (s *dropSlot) open(ctx context.Context, hc *http.Client) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.redirectURI(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.bearer)
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("open drop-box slot: %w", err)
	}
	defer drainAndClose(resp)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("open drop-box slot: HTTP %d", resp.StatusCode)
	}
	return nil
}

// errDropSlotGone reports that the drop-box no longer knows the slot.
var errDropSlotGone = errors.New("the drop-box slot expired or is unknown; run the login again")

// await long-polls until the slot delivers the authorization response, ctx
// ends, or the slot is gone. It sends the result on out, or records the
// failure in *failure and closes out.
func (s *dropSlot) await(ctx context.Context, hc *http.Client, out chan<- *pinoauth.CallbackResult, failure *error) {
	client := dropPollClient(hc)
	ctx, cancel := context.WithTimeout(ctx, dropSlotTTL)
	defer cancel()
	for {
		start := time.Now()
		res, done, err := s.pollOnce(ctx, client)
		if done {
			if err != nil {
				*failure = err
				close(out)
				return
			}
			out <- res
			return
		}
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				*failure = errDropSlotGone
				close(out)
			}
			return
		}
		if err != nil {
			firlog.Debug("mcp oauth: drop-box poll failed; retrying", "err", err)
		}
		// Pause after a failure or a pending answer that came back early,
		// so a server that does not long-poll is not hammered.
		if err != nil || time.Since(start) < dropPollRetryDelay {
			select {
			case <-ctx.Done():
				continue
			case <-time.After(dropPollRetryDelay):
			}
		}
	}
}

// pollOnce issues one poll. done reports a final outcome: a result, or an
// error that retrying cannot fix.
func (s *dropSlot) pollOnce(ctx context.Context, hc *http.Client) (*pinoauth.CallbackResult, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.redirectURI()+"/poll", nil)
	if err != nil {
		return nil, true, err
	}
	req.Header.Set(dropSecretHeader, s.secret)
	if s.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+s.bearer)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer drainAndClose(resp)
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, false, nil
	case http.StatusNotFound:
		if s.bearer == "" {
			// Case B: the server opens the slot only when the owner
			// approves, so until then it is unknown. Keep waiting.
			return nil, false, nil
		}
		return nil, true, errDropSlotGone
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, true, fmt.Errorf("drop-box poll: HTTP %d; the drop-box host token is no longer accepted", resp.StatusCode)
	default:
		return nil, false, fmt.Errorf("drop-box poll: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return nil, true, fmt.Errorf("drop-box poll: decode response: %w", err)
	}
	q, err := url.ParseQuery(body.Query)
	if err != nil {
		return nil, true, fmt.Errorf("drop-box poll: parse query: %w", err)
	}
	if e := q.Get("error"); e != "" {
		if d := q.Get("error_description"); d != "" {
			e += ": " + d
		}
		return nil, true, fmt.Errorf("authorization failed: %s", e)
	}
	if q.Get("code") == "" {
		return nil, true, errors.New("drop-box delivered no authorization code")
	}
	if q.Get("state") != s.state {
		return nil, true, errors.New("drop-box delivered an authorization response with the wrong state")
	}
	return &pinoauth.CallbackResult{Code: q.Get("code"), State: q.Get("state")}, true, nil
}

// dropAccess names a drop-box usable for Case A: its endpoint and a live
// access token for its host.
type dropAccess struct {
	Endpoint string
	Token    string
}

// dropAccessFunc finds a Case A drop-box, or returns ok=false when none is
// available. It must not block on a login in progress.
type dropAccessFunc func(ctx context.Context) (dropAccess, bool)

// dropAccess reports this server's drop-box endpoint and a live access token,
// when its credential's issuer advertises one. A token near expiry is
// refreshed first; a failed refresh yields ok=false. It fails fast when this
// server's own login is in progress.
func (sa *serverAuth) dropAccess(ctx context.Context) (dropAccess, bool) {
	if sa.mode() == AuthModeBearer {
		return dropAccess{}, false
	}
	if err := sa.acquire(ctx); err != nil {
		return dropAccess{}, false
	}
	defer sa.mu.unlock()
	sa.ensureLoadedLocked()
	if sa.cred == nil || sa.cred.Token == nil || sa.cred.Issuer == "" {
		return dropAccess{}, false
	}
	if sa.dropEndpoint == "" || sa.dropIssuer != sa.cred.Issuer {
		sa.dropIssuer = sa.cred.Issuer
		sa.dropEndpoint = ""
		if cands, err := asCandidates(sa.cred.Issuer); err == nil {
			for _, c := range cands {
				if ep := fetchDropEndpoint(ctx, sa.hc, c); ep != "" {
					sa.dropEndpoint = ep
					break
				}
			}
		}
	}
	if sa.dropEndpoint == "" {
		return dropAccess{}, false
	}
	if sa.cred.Token.AccessToken == "" || sa.cred.Token.ExpiresWithin(tokenRefreshWindow) {
		if err := sa.refreshLocked(ctx, true); err != nil || sa.cred == nil || sa.cred.Token == nil {
			firlog.Debug("mcp oauth: drop-box host token refresh failed", "server", sa.name, "err", err)
			return dropAccess{}, false
		}
	}
	return dropAccess{Endpoint: sa.dropEndpoint, Token: sa.cred.Token.AccessToken}, true
}

// dropAccessFor returns a dropAccessFunc that searches the other configured
// servers, in name order, for a drop-box with a live token.
func (m *Manager) dropAccessFor(self string) dropAccessFunc {
	return func(ctx context.Context) (dropAccess, bool) {
		cfgs := m.configsSnapshot()
		names := make([]string, 0, len(cfgs))
		for n := range cfgs {
			if n != self {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			sa, err := m.serverAuthFor(n, cfgs[n])
			if err != nil || sa == nil {
				continue
			}
			if da, ok := sa.dropAccess(ctx); ok {
				return da, true
			}
		}
		return dropAccess{}, false
	}
}
