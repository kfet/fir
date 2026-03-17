package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
)

func TestOpenAIModelLister(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "gpt-4o"},
				{"id": "gpt-4o-mini"},
			},
		})
	}))
	defer srv.Close()

	lister := &openAIModelLister{}
	models, err := lister.ListModels(context.Background(), srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "gpt-4o" || models[1].ID != "gpt-4o-mini" {
		t.Errorf("unexpected model IDs: %v", models)
	}
}

func TestAnthropicModelLister(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("unexpected auth: %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("unexpected version header")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "claude-sonnet-4-20250514"},
			},
		})
	}))
	defer srv.Close()

	lister := &anthropicModelLister{}
	models, err := lister.ListModels(context.Background(), srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "claude-sonnet-4-20250514" {
		t.Errorf("unexpected models: %v", models)
	}
}

func TestGoogleModelLister(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "test-key" {
			t.Errorf("unexpected key: %s", r.URL.Query().Get("key"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{
				{"name": "models/gemini-2.0-flash"},
				{"name": "models/gemini-2.5-pro"},
			},
		})
	}))
	defer srv.Close()

	lister := &googleModelLister{}
	models, err := lister.ListModels(context.Background(), srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "gemini-2.0-flash" || models[1].ID != "gemini-2.5-pro" {
		t.Errorf("unexpected models: %v", models)
	}
}

func TestLiveModelState_Permissive(t *testing.T) {
	s := &liveModelState{}
	// Before fetch, should be permissive
	if !s.has("anything") {
		t.Error("expected permissive before fetch")
	}
	// After error, should be permissive
	s.setError(nil)
	if !s.has("anything") {
		t.Error("expected permissive after error")
	}
}

func TestLiveModelState_Filters(t *testing.T) {
	s := &liveModelState{}
	s.set([]LiveModelInfo{{ID: "model-a"}, {ID: "model-b"}})

	if !s.has("model-a") {
		t.Error("expected model-a to be available")
	}
	if s.has("model-c") {
		t.Error("expected model-c to be unavailable")
	}
}

func TestGetAvailable_WithLiveFiltering(t *testing.T) {
	authStore := auth.NewAuthStorage("")
	t.Setenv("ANTHROPIC_API_KEY", "test")

	ai.RegisterModel(&ai.Model{ID: "real-model", Provider: "anthropic", Api: "anthropic-messages", BaseURL: "https://api.anthropic.com"})
	ai.RegisterModel(&ai.Model{ID: "fake-model", Provider: "anthropic", Api: "anthropic-messages", BaseURL: "https://api.anthropic.com"})

	registry := NewModelRegistry(authStore, "")

	// Before live fetch, both should appear
	avail := registry.GetAvailable()
	foundReal, foundFake := false, false
	for _, m := range avail {
		if m.ID == "real-model" {
			foundReal = true
		}
		if m.ID == "fake-model" {
			foundFake = true
		}
	}
	if !foundReal || !foundFake {
		t.Error("expected both models before live fetch")
	}

	// Simulate live fetch completing with only real-model
	state := &liveModelState{}
	state.set([]LiveModelInfo{{ID: "real-model"}})
	registry.liveModelsMu.Lock()
	registry.liveModels["anthropic"] = state
	registry.liveModelsMu.Unlock()

	avail = registry.GetAvailable()
	foundReal, foundFake = false, false
	for _, m := range avail {
		if m.ID == "real-model" {
			foundReal = true
		}
		if m.ID == "fake-model" {
			foundFake = true
		}
	}
	if !foundReal {
		t.Error("expected real-model after live fetch")
	}
	if foundFake {
		t.Error("expected fake-model to be filtered out after live fetch")
	}
}

func TestLiveCachePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	authStore := auth.NewAuthStorage("")
	registry := NewModelRegistry(authStore, "")

	models := []LiveModelInfo{{ID: "model-a"}, {ID: "model-b"}}
	registry.saveLiveCache(tmpDir, "test-provider", models)

	loaded, ok := registry.loadLiveCache(tmpDir, "test-provider")
	if !ok {
		t.Fatal("expected cache to load")
	}
	if len(loaded) != 2 || loaded[0].ID != "model-a" {
		t.Errorf("unexpected cached models: %v", loaded)
	}

	// Verify expired cache is rejected
	cachePath := filepath.Join(tmpDir, "live-models-test-provider.json")
	data, _ := os.ReadFile(cachePath)
	var cache liveModelCache
	json.Unmarshal(data, &cache)
	cache.FetchedAt = time.Now().Add(-2 * time.Hour)
	data, _ = json.Marshal(cache)
	os.WriteFile(cachePath, data, 0o644)

	_, ok = registry.loadLiveCache(tmpDir, "test-provider")
	if ok {
		t.Error("expected expired cache to be rejected")
	}
}
