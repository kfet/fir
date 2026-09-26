package providers

import (
	"testing"
	"time"
)

// Regression: Go's default http.Transport sets TLSHandshakeTimeout to 10s,
// which is too aggressive against api.anthropic.com on slow links and
// surfaces as "net/http: TLS handshake timeout". Make sure our shared
// transport bumps it.
func TestSharedTransportTLSHandshakeTimeout(t *testing.T) {
	if sharedTransport == nil {
		t.Fatal("sharedTransport is nil")
	}
	if got, want := sharedTransport.TLSHandshakeTimeout, 60*time.Second; got < want {
		t.Errorf("TLSHandshakeTimeout = %v, want >= %v", got, want)
	}
	if got, want := sharedTransport.MaxIdleConnsPerHost, 20; got < want {
		t.Errorf("MaxIdleConnsPerHost = %d, want >= %d", got, want)
	}
	if got, want := sharedTransport.IdleConnTimeout, 10*time.Minute; got < want {
		t.Errorf("IdleConnTimeout = %v, want >= %v", got, want)
	}
	// DefaultSSEClient and DoJSONRequest must use the shared transport so
	// the bumped timeout actually applies.
	// (via providerTransport, which delegates non-unix requests to it).
	if _, ok := DefaultSSEClient.HTTPClient.Transport.(providerTransport); !ok {
		t.Error("DefaultSSEClient does not use providerTransport")
	}
	if defaultHTTPClient.Transport != httpTransport {
		t.Error("defaultHTTPClient does not use providerTransport")
	}
}
