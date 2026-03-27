// Shared OAuth callback server used by multiple OAuth providers.
package oauth

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"sync"
)

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
		errParam := r.URL.Query().Get("error")
		if errParam != "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(400)
			fmt.Fprintf(w, "<!doctype html><html><body><h1>Authentication Failed</h1><p>Error: %s</p><p>You can close this window.</p></body></html>", html.EscapeString(errParam))
			return
		}

		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")

		if expectedState != "" && state != expectedState {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(400)
			fmt.Fprint(w, "<!doctype html><html><body><h1>Authentication Failed</h1><p>State mismatch — possible CSRF attack. Please try again.</p></body></html>")
			return
		}

		if code != "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<!doctype html><html><body><h1>Authentication Successful</h1><p>You can close this window and return to the terminal.</p></body></html>")
			once.Do(func() {
				ch <- &CallbackResult{Code: code, State: state}
			})
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(400)
			fmt.Fprint(w, "<!doctype html><html><body><h1>Authentication Failed</h1><p>Missing authorization code.</p></body></html>")
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
