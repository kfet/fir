package providers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func vertexTestModel() *ai.Model {
	return &ai.Model{
		ID:            "gemini-2.5-pro",
		Name:          "Gemini 2.5 Pro",
		API:           ai.ApiGoogleVertex,
		Provider:      ai.ProviderGoogleVertex,
		BaseURL:       "",
		Reasoning:     true,
		Input:         []ai.InputModality{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{Input: 1.25, Output: 10.0},
		ContextWindow: 1000000,
		MaxTokens:     65536,
	}
}

func TestResolveVertexProject(t *testing.T) {
	// From env
	os.Setenv("GOOGLE_CLOUD_PROJECT", "my-project")
	defer os.Unsetenv("GOOGLE_CLOUD_PROJECT")

	got := resolveVertexProject(nil)
	if got != "my-project" {
		t.Errorf("expected 'my-project', got %q", got)
	}

	// From options header (takes precedence)
	opts := &ai.StreamOptions{Headers: map[string]string{"x-vertex-project": "opt-project"}}
	got = resolveVertexProject(opts)
	if got != "opt-project" {
		t.Errorf("expected 'opt-project', got %q", got)
	}
	// Header should be removed after reading
	if _, exists := opts.Headers["x-vertex-project"]; exists {
		t.Error("expected x-vertex-project header to be removed")
	}
}

func TestResolveVertexProject_Fallback(t *testing.T) {
	os.Unsetenv("GOOGLE_CLOUD_PROJECT")
	os.Setenv("GCLOUD_PROJECT", "fallback-project")
	defer os.Unsetenv("GCLOUD_PROJECT")

	got := resolveVertexProject(nil)
	if got != "fallback-project" {
		t.Errorf("expected 'fallback-project', got %q", got)
	}
}

func TestResolveVertexLocation(t *testing.T) {
	os.Setenv("GOOGLE_CLOUD_LOCATION", "us-central1")
	defer os.Unsetenv("GOOGLE_CLOUD_LOCATION")

	got := resolveVertexLocation(nil)
	if got != "us-central1" {
		t.Errorf("expected 'us-central1', got %q", got)
	}

	// From options header
	opts := &ai.StreamOptions{Headers: map[string]string{"x-vertex-location": "europe-west1"}}
	got = resolveVertexLocation(opts)
	if got != "europe-west1" {
		t.Errorf("expected 'europe-west1', got %q", got)
	}
}

func TestResolveVertexLocation_Empty(t *testing.T) {
	os.Unsetenv("GOOGLE_CLOUD_LOCATION")
	got := resolveVertexLocation(nil)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestLoadADCFromFile_Valid(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "credentials.json")
	creds := map[string]string{
		"client_id":     "test-client-id",
		"client_secret": "test-secret",
		"refresh_token": "test-refresh",
		"type":          "authorized_user",
	}
	data, _ := json.Marshal(creds)
	os.WriteFile(credFile, data, 0600)

	result, err := loadADCFromFile(credFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ClientID != "test-client-id" {
		t.Errorf("expected client_id 'test-client-id', got %q", result.ClientID)
	}
	if result.RefreshToken != "test-refresh" {
		t.Errorf("expected refresh_token 'test-refresh', got %q", result.RefreshToken)
	}
}

func TestLoadADCFromFile_UnsupportedType(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "credentials.json")
	creds := map[string]string{
		"type": "service_account",
	}
	data, _ := json.Marshal(creds)
	os.WriteFile(credFile, data, 0600)

	_, err := loadADCFromFile(credFile)
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestLoadADCFromFile_Missing(t *testing.T) {
	_, err := loadADCFromFile("/nonexistent/path/credentials.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadADCFromFile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "credentials.json")
	os.WriteFile(credFile, []byte("{invalid"), 0600)

	_, err := loadADCFromFile(credFile)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestStreamGoogleVertex_NoProject(t *testing.T) {
	model := vertexTestModel()
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GCLOUD_PROJECT", "")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "us-central1")
	// No ApiKey — forces ADC path which requires project.
	opts := &ai.StreamOptions{Headers: map[string]string{}}

	result := StreamGoogleVertex(context.Background(), model, ai.Context{}, opts)
	var lastEvt *ai.AssistantMessageEvent
	for evt := range result.Events {
		e := evt
		lastEvt = &e
	}
	if lastEvt == nil || lastEvt.Type != ai.EventError {
		t.Error("expected error for missing project")
	}
	if lastEvt != nil && !strings.Contains(lastEvt.Error.ErrorMessage, "project") {
		t.Errorf("expected project error, got: %s", lastEvt.Error.ErrorMessage)
	}
}

func TestStreamGoogleVertex_NoLocation(t *testing.T) {
	model := vertexTestModel()
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")
	// No ApiKey — forces ADC path which requires location.
	opts := &ai.StreamOptions{Headers: map[string]string{}}

	result := StreamGoogleVertex(context.Background(), model, ai.Context{}, opts)
	var lastEvt *ai.AssistantMessageEvent
	for evt := range result.Events {
		e := evt
		lastEvt = &e
	}
	if lastEvt == nil || lastEvt.Type != ai.EventError {
		t.Error("expected error for missing location")
	}
	if lastEvt != nil && !strings.Contains(lastEvt.Error.ErrorMessage, "location") {
		t.Errorf("expected location error, got: %s", lastEvt.Error.ErrorMessage)
	}
}

func TestRegisterGoogleVertex(t *testing.T) {
	reg := ai.NewRegistry()
	RegisterGoogleVertex(reg)

	provider := reg.GetApiProvider(ai.ApiGoogleVertex)
	if provider == nil {
		t.Fatal("expected google-vertex provider to be registered")
	}
}

func TestResolveVertexAPIKey(t *testing.T) {
	t.Run("from options", func(t *testing.T) {
		t.Setenv("GOOGLE_CLOUD_API_KEY", "")
		opts := &ai.StreamOptions{APIKey: "options-key"}
		if got := resolveVertexAPIKey(opts); got != "options-key" {
			t.Errorf("got %q, want %q", got, "options-key")
		}
	})
	t.Run("from env", func(t *testing.T) {
		t.Setenv("GOOGLE_CLOUD_API_KEY", "env-key")
		if got := resolveVertexAPIKey(nil); got != "env-key" {
			t.Errorf("got %q, want %q", got, "env-key")
		}
	})
	t.Run("options takes precedence over env", func(t *testing.T) {
		t.Setenv("GOOGLE_CLOUD_API_KEY", "env-key")
		opts := &ai.StreamOptions{APIKey: "options-key"}
		if got := resolveVertexAPIKey(opts); got != "options-key" {
			t.Errorf("got %q, want %q", got, "options-key")
		}
	})
	t.Run("empty when not set", func(t *testing.T) {
		t.Setenv("GOOGLE_CLOUD_API_KEY", "")
		if got := resolveVertexAPIKey(nil); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})
}
