package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedactHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.42")
	h.Add("Set-Cookie", "a=b")
	h.Set("Authorization", "Bearer x")
	h.Set("X-Api-Key", "k")
	h.Set("X-Session-Token", "t")
	h.Add("Via", "a")
	h.Add("Via", "b")
	got := RedactHeaders(h)
	want := map[string]string{"anthropic-ratelimit-unified-5h-utilization": "0.42", "via": "a, b"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q want %q", k, got[k], v)
		}
	}
}

func TestRedactHeaderMap(t *testing.T) {
	got := RedactHeaderMap(map[string]string{"X-RateLimit-Remaining": "3", "Cookie": "c", "openai-auth-x": "y"})
	if len(got) != 1 || got["x-ratelimit-remaining"] != "3" {
		t.Fatalf("got %v", got)
	}
}

func TestProviderTransportReportsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Ratelimit-Remaining", "7")
		w.Header().Set("Set-Cookie", "s=1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	var got map[string]string
	ctx := WithResponseHeaderSink(context.Background(), func(h map[string]string) { got = h })
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got["x-ratelimit-remaining"] != "7" || got["set-cookie"] != "" {
		t.Fatalf("got %v", got)
	}

	// No sink: no panic, nothing recorded.
	got = nil
	req, _ = http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err = defaultHTTPClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != nil {
		t.Fatalf("recorded without sink: %v", got)
	}
	if ResponseHeaderSinkFrom(context.Background()) != nil {
		t.Fatal("unexpected sink")
	}
}
