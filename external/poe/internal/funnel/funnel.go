// Package funnel provides an optional tsnet-based HTTPS listener that
// exposes the bridge to the public internet via Tailscale Funnel.
package funnel

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"tailscale.com/tsnet"
)

// Config controls the tsnet Funnel listener.
type Config struct {
	Hostname   string // tailnet device name (e.g. "poe-bridge"), required
	StateDir   string // where tsnet persists state, required
	FunnelPort int    // public port (443, 8443, or 10000); default 443
}

// Listener holds the running tsnet server and the Funnel listener.
type Listener struct {
	srv *tsnet.Server
	ln  net.Listener
}

// Listen starts a tsnet node and returns a Funnel listener. TS_AUTHKEY
// must be set in the environment (or tsnet state must already exist from
// a previous run).
func Listen(ctx context.Context, cfg Config) (*Listener, error) {
	if cfg.Hostname == "" {
		return nil, fmt.Errorf("funnel: Hostname is required")
	}
	if cfg.StateDir == "" {
		return nil, fmt.Errorf("funnel: StateDir is required")
	}
	if cfg.FunnelPort == 0 {
		cfg.FunnelPort = 443
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("funnel: mkdir %s: %w", cfg.StateDir, err)
	}

	s := &tsnet.Server{
		Hostname: cfg.Hostname,
		Dir:      cfg.StateDir,
	}

	if os.Getenv("TS_AUTHKEY") == "" {
		stateFile := filepath.Join(cfg.StateDir, "tailscaled.state")
		if _, err := os.Stat(stateFile); os.IsNotExist(err) {
			log.Printf("funnel: TS_AUTHKEY not set and no state in %s — tsnet will require interactive login", cfg.StateDir)
		}
	}

	if err := s.Start(); err != nil {
		return nil, fmt.Errorf("funnel: tsnet start: %w", err)
	}

	ln, err := s.ListenFunnel("tcp", fmt.Sprintf(":%d", cfg.FunnelPort))
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("funnel: listen :%d: %w", cfg.FunnelPort, err)
	}

	log.Printf("funnel: listening on https://%s.ts.net:%d", cfg.Hostname, cfg.FunnelPort)
	return &Listener{srv: s, ln: ln}, nil
}

// Addr returns the listener's network address.
func (l *Listener) Addr() net.Addr { return l.ln.Addr() }

// Serve runs http.Serve on the Funnel listener. Funnel handles TLS
// termination, so no TLS config is needed here.
func (l *Listener) Serve(handler http.Handler) error {
	return http.Serve(l.ln, handler)
}

// Close shuts down the listener and the tsnet server.
func (l *Listener) Close() error {
	l.ln.Close()
	return l.srv.Close()
}
