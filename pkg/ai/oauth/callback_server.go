// Shared OAuth callback server used by multiple OAuth providers.
package oauth

import (
	"context"
	_ "embed"
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"
	"sync"
)

//go:embed callback_page.html
var callbackPageHTML string

// Placeholder tokens used in callback_page.html.
const (
	phTitle   = "__TITLE__"
	phIcon    = "__ICON__"
	phHeading = "__HEADING__"
	phMessage = "__MESSAGE__"
)

// allPlaceholders is the complete set of tokens that renderAuthPage replaces.
// Tests verify this stays in sync with the HTML template.
var allPlaceholders = []string{phTitle, phIcon, phHeading, phMessage}

// renderAuthPage renders the callback page with the given content.
// Uses simple string replacement to avoid html/template (which adds ~6MB to
// the binary due to reflect-driven linker retention of large indirect deps).
func renderAuthPage(title, icon, heading, message string) string {
	r := strings.NewReplacer(
		phTitle, html.EscapeString(title),
		phIcon, icon,
		phHeading, html.EscapeString(heading),
		phMessage, html.EscapeString(message),
	)
	return r.Replace(callbackPageHTML)
}

// CallbackResult holds the result from the OAuth callback server.
type CallbackResult struct {
	Code  string
	State string
}

// StartOAuthCallbackServer starts a local HTTP server to receive an OAuth callback.
// route is the path to listen on (e.g., "/oauth-callback").
// addr is the listener address (e.g., "127.0.0.1:51121").
// expectedState, if non-empty, is validated server-side: requests with a
// mismatched state parameter receive a 400 response and are not forwarded.
// Returns the server, a channel for the result, and the actual listener address
// (which may differ from addr if port 0 was used).
func StartOAuthCallbackServer(ctx context.Context, route, addr, expectedState string) (server *http.Server, resultCh <-chan *CallbackResult, actualAddr string, err error) {
	ch := make(chan *CallbackResult, 1)
	var once sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		errParam := r.URL.Query().Get("error")
		if errParam != "" {
			w.WriteHeader(400)
			fmt.Fprint(w, renderAuthPage("Authentication Failed", "⚠️", "Authentication Failed", "Error: "+errParam))
			return
		}

		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")

		if expectedState != "" && state != expectedState {
			w.WriteHeader(400)
			fmt.Fprint(w, renderAuthPage("Authentication Failed", "🚫", "Authentication Failed", "State mismatch — please try again."))
			return
		}

		if code != "" {
			fmt.Fprint(w, renderAuthPage("Authentication Successful", "✓", "You're all set", "You can close this window and return to the terminal."))
			once.Do(func() {
				ch <- &CallbackResult{Code: code, State: state}
			})
		} else {
			w.WriteHeader(400)
			fmt.Fprint(w, renderAuthPage("Authentication Failed", "⚠️", "Authentication Failed", "Missing authorization code."))
		}
	})

	srv := &http.Server{Handler: mux}

	ln, listenErr := net.Listen("tcp", addr)
	if listenErr != nil {
		return nil, nil, "", fmt.Errorf("starting callback server on %s: %w", addr, listenErr)
	}

	resolvedAddr := ln.Addr().String()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			once.Do(func() { close(ch) })
		}
	}()

	go func() {
		<-ctx.Done()
		srv.Close()
		once.Do(func() { close(ch) })
	}()

	return srv, ch, resolvedAddr, nil
}
