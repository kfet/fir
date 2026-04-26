package providers

import (
	"net/http"
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
	if got, want := sharedTransport.TLSHandshakeTimeout, 30*time.Second; got < want {
		t.Errorf("TLSHandshakeTimeout = %v, want >= %v", got, want)
	}
	// DefaultSSEClient and DoJSONRequest must use the shared transport so
	// the bumped timeout actually applies.
	c, ok := DefaultSSEClient.HTTPClient.Transport.(*http.Transport)
	if !ok || c != sharedTransport {
		t.Error("DefaultSSEClient does not use sharedTransport")
	}
}
