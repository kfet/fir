// Test utilities for provider tests.
// Upstream hash: 1caadb2e
package providers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// testdataDir returns the path to the testdata directory.
func testdataDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "testdata")
}

// loadFixture reads a fixture file from testdata/.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdataDir(), name))
	if err != nil {
		t.Fatalf("failed to read fixture %q: %v", name, err)
	}
	return data
}

// tryNewServer attempts to create an httptest.Server, skipping the test
// if the port cannot be bound (e.g., in sandboxed environments).
func tryNewServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	// httptest.NewServer panics if it can't listen — recover and skip
	var srv *httptest.Server
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Skipf("cannot start httptest server: %v", r)
			}
		}()
		srv = httptest.NewServer(handler)
	}()
	return srv
}

// mockSSEServer returns an httptest.Server that serves a canned SSE response.
// The fixture file should contain the raw SSE event stream body.
func mockSSEServer(t *testing.T, fixture string) *httptest.Server {
	t.Helper()
	data := loadFixture(t, fixture)
	return tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
}

// mockJSONServer returns an httptest.Server that serves a canned JSON response.
func mockJSONServer(t *testing.T, statusCode int, body []byte) *httptest.Server {
	t.Helper()
	return tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		w.Write(body)
	}))
}

// mockSSEServerFunc returns an httptest.Server that calls a handler function.
// Useful for tests that need to inspect the request or vary responses.
func mockSSEServerFunc(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return tryNewServer(t, handler)
}

// captureRequest returns an httptest.Server that captures the request body
// and responds with the given SSE fixture.
func captureRequest(t *testing.T, fixture string) (*httptest.Server, *[]byte) {
	t.Helper()
	data := loadFixture(t, fixture)
	var captured []byte
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
		}
		captured = body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	return srv, &captured
}

// testModel creates a test model pointing to the given server URL.
func testModel(serverURL string, api ai.Api, provider ai.Provider) *ai.Model {
	return &ai.Model{
		ID:            "test-model",
		Name:          "Test Model",
		Api:           api,
		Provider:      provider,
		BaseURL:       serverURL,
		Reasoning:     false,
		Input:         []ai.InputModality{ai.InputText},
		Cost:          ai.ModelCost{Input: 3.0, Output: 15.0, CacheRead: 0.3, CacheWrite: 3.75},
		ContextWindow: 200000,
		MaxTokens:     8192,
	}
}

// collectEvents drains all events from a stream and returns them.
func collectEvents(t *testing.T, stream *ai.AssistantMessageEventStream) []ai.AssistantMessageEvent {
	t.Helper()
	var events []ai.AssistantMessageEvent
	for evt := range stream.Events {
		events = append(events, evt)
	}
	return events
}

// --- Fixture files ---
// Fixtures are stored in testdata/ as raw SSE event streams.
// To record new fixtures, use FIR_RECORD_FIXTURES=1 with live tests.

func TestTestdataDirExists(t *testing.T) {
	dir := testdataDir()
	info, err := os.Stat(dir)
	if err != nil {
		// Create the testdata dir if it doesn't exist
		if os.IsNotExist(err) {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatalf("failed to create testdata dir: %v", err)
			}
			return
		}
		t.Fatalf("failed to stat testdata dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("testdata is not a directory")
	}
}
