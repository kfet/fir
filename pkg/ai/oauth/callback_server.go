// Shared OAuth callback server used by multiple Google OAuth providers.
package oauth

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"sync"
)

// startOAuthCallbackServer starts a local HTTP server to receive an OAuth callback.
// route is the path to listen on (e.g., "/oauth-callback").
// addr is the listener address (e.g., "127.0.0.1:51121").
func startOAuthCallbackServer(ctx context.Context, route, addr string) (server *http.Server, resultCh <-chan *callbackResult, err error) {
	ch := make(chan *callbackResult, 1)
	var once sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
		errParam := r.URL.Query().Get("error")
		if errParam != "" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(400)
			fmt.Fprintf(w, "<html><body><h1>Authentication Failed</h1><p>Error: %s</p><p>You can close this window.</p></body></html>", html.EscapeString(errParam))
			return
		}

		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		if code != "" && state != "" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body><h1>Authentication Successful</h1><p>You can close this window and return to the terminal.</p></body></html>")
			once.Do(func() {
				ch <- &callbackResult{Code: code, State: state}
			})
		} else {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(400)
			fmt.Fprint(w, "<html><body><h1>Authentication Failed</h1><p>Missing code or state parameter.</p></body></html>")
		}
	})

	srv := &http.Server{Handler: mux}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("starting callback server on %s: %w", addr, err)
	}

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

	return srv, ch, nil
}
