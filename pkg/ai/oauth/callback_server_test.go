package oauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStartOAuthCallbackServer_SuccessfulAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, resultCh, _, err := StartOAuthCallbackServer(ctx, "/oauth-callback", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("StartOAuthCallbackServer error: %v", err)
	}
	defer srv.Close()

	// Get the actual address (port 0 picks a random available port)
	// We need to extract the port from the server — use a helper approach
	// Since we passed port 0, the server listened on a random port.
	// Unfortunately the server doesn't expose the address easily.
	// Let's use a fixed port for testing instead.
	_ = resultCh
	srv.Close()
	cancel()

	// Retry with a deterministic approach
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	addr := fmt.Sprintf("127.0.0.1:%d", 51199) // unlikely to conflict
	srv2, resultCh2, _, err := StartOAuthCallbackServer(ctx2, "/oauth-callback", addr)
	if err != nil {
		t.Skipf("could not listen on %s (port in use): %v", addr, err)
	}
	defer srv2.Close()

	// Make a successful callback request
	resp, err := http.Get(fmt.Sprintf("http://%s/oauth-callback?code=testcode&state=teststate", addr))
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Authentication Successful") {
		t.Errorf("expected success message, got: %s", body)
	}

	// Channel should receive the result
	select {
	case result := <-resultCh2:
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if result.Code != "testcode" {
			t.Errorf("expected code 'testcode', got %q", result.Code)
		}
		if result.State != "teststate" {
			t.Errorf("expected state 'teststate', got %q", result.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback result")
	}
}

func TestStartOAuthCallbackServer_ErrorParam(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := "127.0.0.1:51198"
	srv, _, _, err := StartOAuthCallbackServer(ctx, "/oauth-callback", addr)
	if err != nil {
		t.Skipf("could not listen on %s: %v", addr, err)
	}
	defer srv.Close()

	resp, err := http.Get(fmt.Sprintf("http://%s/oauth-callback?error=access_denied", addr))
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Authentication Failed") {
		t.Errorf("expected failure message, got: %s", body)
	}
	if !strings.Contains(string(body), "access_denied") {
		t.Errorf("expected error param in body, got: %s", body)
	}
}

func TestStartOAuthCallbackServer_ErrorParamXSSEscaped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := "127.0.0.1:51197"
	srv, _, _, err := StartOAuthCallbackServer(ctx, "/oauth-callback", addr)
	if err != nil {
		t.Skipf("could not listen on %s: %v", addr, err)
	}
	defer srv.Close()

	resp, err := http.Get(fmt.Sprintf("http://%s/oauth-callback?error=<script>alert(1)</script>", addr))
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// The script tag should be escaped, not rendered as HTML
	if strings.Contains(bodyStr, "<script>") {
		t.Error("XSS: error param was not HTML-escaped")
	}
	if !strings.Contains(bodyStr, "&lt;script&gt;") {
		t.Errorf("expected HTML-escaped script tag, got: %s", bodyStr)
	}
}

func TestStartOAuthCallbackServer_MissingCodeOrState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := "127.0.0.1:51196"
	srv, _, _, err := StartOAuthCallbackServer(ctx, "/oauth-callback", addr)
	if err != nil {
		t.Skipf("could not listen on %s: %v", addr, err)
	}
	defer srv.Close()

	// Missing both code and state
	resp, err := http.Get(fmt.Sprintf("http://%s/oauth-callback", addr))
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Missing code or state") {
		t.Errorf("expected missing params message, got: %s", body)
	}
}

func TestStartOAuthCallbackServer_MissingStateOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := "127.0.0.1:51195"
	srv, _, _, err := StartOAuthCallbackServer(ctx, "/oauth-callback", addr)
	if err != nil {
		t.Skipf("could not listen on %s: %v", addr, err)
	}
	defer srv.Close()

	resp, err := http.Get(fmt.Sprintf("http://%s/oauth-callback?code=abc", addr))
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("expected 400 when state missing, got %d", resp.StatusCode)
	}
}

func TestStartOAuthCallbackServer_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	addr := "127.0.0.1:51194"
	srv, resultCh, _, err := StartOAuthCallbackServer(ctx, "/oauth-callback", addr)
	if err != nil {
		t.Skipf("could not listen on %s: %v", addr, err)
	}
	_ = srv

	// Cancel context — should close the server and channel
	cancel()

	select {
	case result, ok := <-resultCh:
		if ok && result != nil {
			t.Error("expected closed channel or nil result after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close after cancel")
	}
}
