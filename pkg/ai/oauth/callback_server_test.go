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

	srv, resultCh, actualAddr, err := StartOAuthCallbackServer(ctx, "/oauth-callback", "127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("StartOAuthCallbackServer error: %v", err)
	}
	defer srv.Close()

	resp, err := http.Get(fmt.Sprintf("http://%s/oauth-callback?code=testcode&state=teststate", actualAddr))
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

	select {
	case result := <-resultCh:
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

func TestStartOAuthCallbackServer_StateValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	expectedState := "correct-state"
	srv, resultCh, actualAddr, err := StartOAuthCallbackServer(ctx, "/oauth-callback", "127.0.0.1:0", expectedState)
	if err != nil {
		t.Fatalf("StartOAuthCallbackServer error: %v", err)
	}
	defer srv.Close()

	// Wrong state should be rejected
	resp, err := http.Get(fmt.Sprintf("http://%s/oauth-callback?code=testcode&state=wrong-state", actualAddr))
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("expected 400 for wrong state, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "State mismatch") {
		t.Errorf("expected state mismatch message, got: %s", body)
	}

	// Correct state should succeed
	resp2, err := http.Get(fmt.Sprintf("http://%s/oauth-callback?code=testcode&state=%s", actualAddr, expectedState))
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != 200 {
		t.Errorf("expected 200 for correct state, got %d", resp2.StatusCode)
	}

	select {
	case result := <-resultCh:
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if result.Code != "testcode" {
			t.Errorf("expected code 'testcode', got %q", result.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callback result")
	}
}

func TestStartOAuthCallbackServer_ErrorParam(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, _, actualAddr, err := StartOAuthCallbackServer(ctx, "/oauth-callback", "127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("StartOAuthCallbackServer error: %v", err)
	}
	defer srv.Close()

	resp, err := http.Get(fmt.Sprintf("http://%s/oauth-callback?error=access_denied", actualAddr))
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

	srv, _, actualAddr, err := StartOAuthCallbackServer(ctx, "/oauth-callback", "127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("StartOAuthCallbackServer error: %v", err)
	}
	defer srv.Close()

	resp, err := http.Get(fmt.Sprintf("http://%s/oauth-callback?error=<script>alert(1)</script>", actualAddr))
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if strings.Contains(bodyStr, "<script>") {
		t.Error("XSS: error param was not HTML-escaped")
	}
	if !strings.Contains(bodyStr, "&lt;script&gt;") {
		t.Errorf("expected HTML-escaped script tag, got: %s", bodyStr)
	}
}

func TestStartOAuthCallbackServer_MissingCode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, _, actualAddr, err := StartOAuthCallbackServer(ctx, "/oauth-callback", "127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("StartOAuthCallbackServer error: %v", err)
	}
	defer srv.Close()

	resp, err := http.Get(fmt.Sprintf("http://%s/oauth-callback?state=foo", actualAddr))
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Missing authorization code") {
		t.Errorf("expected missing code message, got: %s", body)
	}
}

func TestStartOAuthCallbackServer_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	srv, resultCh, _, err := StartOAuthCallbackServer(ctx, "/oauth-callback", "127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("StartOAuthCallbackServer error: %v", err)
	}
	_ = srv

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
