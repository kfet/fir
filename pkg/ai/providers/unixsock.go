package providers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
)

// Unix-socket base URLs.
//
// A baseUrl of the form unix:///run/boxres/bifrost.sock/anthropic is served
// over the unix socket /run/boxres/bifrost.sock. The socket path is the
// URL path up to and including its first segment ending in ".sock"; the
// remainder (here /anthropic) is the HTTP path sent to the server with
// Host: localhost. A unix URL without a ".sock" segment is rejected.

// SplitUnixURL splits a unix:// URL into socket path and HTTP request path
// (always starting with "/", query preserved). ok is false when u is not a
// unix:// URL.
func SplitUnixURL(u string) (sock, rest string, ok bool, err error) {
	after, found := strings.CutPrefix(u, "unix://")
	if !found {
		return "", "", false, nil
	}
	path, query, hasQuery := strings.Cut(after, "?")
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if strings.HasSuffix(s, ".sock") {
			sock = strings.Join(segs[:i+1], "/")
			rest = "/" + strings.Join(segs[i+1:], "/")
			if hasQuery {
				rest += "?" + query
			}
			return sock, rest, true, nil
		}
	}
	return "", "", true, fmt.Errorf("unix base URL %q: no path segment ending in .sock", u)
}

// unixTransports caches one *http.Transport per socket path so connection
// pools never mix sockets (all share the "localhost" host key).
var unixTransports sync.Map // string -> *http.Transport

func unixTransport(sock string) *http.Transport {
	if t, ok := unixTransports.Load(sock); ok {
		return t.(*http.Transport)
	}
	t := sharedTransport.Clone()
	t.Proxy = nil
	var d net.Dialer
	t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return d.DialContext(ctx, "unix", sock)
	}
	actual, _ := unixTransports.LoadOrStore(sock, t)
	return actual.(*http.Transport)
}

// providerTransport routes unix:// requests over a unix socket and all
// others through sharedTransport.
type providerTransport struct{}

func (providerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "unix" {
		return sharedTransport.RoundTrip(req)
	}
	sock, rest, _, err := SplitUnixURL("unix://" + req.URL.Path)
	if err == nil && req.URL.Host != "" {
		err = fmt.Errorf("unix base URL must be unix:///<socket>, got host %q", req.URL.Host)
	}
	if err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	r := req.Clone(req.Context())
	r.URL.Scheme, r.URL.Host, r.URL.Path, r.URL.RawPath = "http", "localhost", rest, ""
	r.Host = "localhost"
	return unixTransport(sock).RoundTrip(r)
}

// httpTransport is the RoundTripper every provider HTTP client uses.
var httpTransport http.RoundTripper = providerTransport{}

// defaultHTTPClient replaces http.DefaultClient for provider calls so that
// unix:// base URLs work everywhere.
var defaultHTTPClient = &http.Client{Transport: httpTransport}
