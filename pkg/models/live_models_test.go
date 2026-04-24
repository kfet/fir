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
	if models[0] != "gpt-4o" || models[1] != "gpt-4o-mini" {
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
	if len(models) != 1 || models[0] != "claude-sonnet-4-20250514" {
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
	if len(models) != 2 || models[0] != "gemini-2.0-flash" || models[1] != "gemini-2.5-pro" {
		t.Errorf("unexpected models: %v", models)
	}
}

func TestLiveModelState_Permissive(t *testing.T) {
	s := newLiveModelState()
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
	s := newLiveModelState()
	s.set([]string{"model-a", "model-b"}, []*ai.Model{
		{ID: "model-a", Provider: "test", Name: "Model A", ContextWindow: 128000, MaxTokens: 4096},
		{ID: "model-b", Provider: "test", Name: "Model B", ContextWindow: 128000, MaxTokens: 4096},
	})

	if !s.has("model-a") {
		t.Error("expected model-a to be available")
	}
	if s.has("model-c") {
		t.Error("expected model-c to be unavailable")
	}
	if m := s.get("model-a"); m == nil || m.Name != "Model A" {
		t.Error("expected get() to return synthesised model")
	}
	if m := s.get("model-c"); m != nil {
		t.Error("expected get() to return nil for unknown")
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
	state := newLiveModelState()
	state.set([]string{"real-model"}, []*ai.Model{
		{ID: "real-model", Provider: "anthropic", Name: "Real Model", Api: "anthropic-messages", BaseURL: "https://api.anthropic.com", ContextWindow: 128000, MaxTokens: 4096},
	})
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

	models := []*ai.Model{
		{ID: "model-a", Provider: "test", Name: "Model A", ContextWindow: 128000, MaxTokens: 4096},
		{ID: "model-b", Provider: "test", Name: "Model B", ContextWindow: 128000, MaxTokens: 4096},
	}
	registry.saveLiveCache(tmpDir, "test-provider", []string{"model-a", "model-b"}, models)

	_, loaded, ok := registry.loadLiveCache(tmpDir, "test-provider")
	if !ok {
		t.Fatal("expected cache to load")
	}
	if len(loaded) != 2 || loaded[0].ID != "model-a" {
		t.Errorf("unexpected cached models: %v", loaded)
	}

	// Verify expired cache is rejected
	cachePath := liveCachePath(tmpDir, "test-provider")
	data, _ := os.ReadFile(cachePath)
	var cache liveModelCache
	json.Unmarshal(data, &cache)
	cache.FetchedAt = time.Now().Add(-2 * time.Hour)
	data, _ = json.Marshal(cache)
	os.WriteFile(cachePath, data, 0o644)

	_, _, ok = registry.loadLiveCache(tmpDir, "test-provider")
	if ok {
		t.Error("expected expired cache to be rejected")
	}
}

func TestRefreshLive_ClearsStateAndDiskCache(t *testing.T) {
	tmpDir := t.TempDir()
	authStore := auth.NewAuthStorage("")
	registry := NewModelRegistry(authStore, "")

	// Seed disk cache and in-memory state as if a previous fetch happened.
	registry.saveLiveCache(tmpDir, "prov-a", []string{"m1"}, []*ai.Model{
		{ID: "m1", Provider: "prov-a", Name: "M1", ContextWindow: 128000, MaxTokens: 4096},
	})
	registry.saveLiveCache(tmpDir, "prov-b", []string{"m2"}, []*ai.Model{
		{ID: "m2", Provider: "prov-b", Name: "M2", ContextWindow: 128000, MaxTokens: 4096},
	})

	// Write an unrelated file that must NOT be deleted.
	keepPath := filepath.Join(tmpDir, "other-file.json")
	if err := os.WriteFile(keepPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry.liveModelsMu.Lock()
	registry.liveCacheDir = tmpDir
	registry.liveModels["prov-a"] = newLiveModelState()
	registry.liveModels["prov-b"] = newLiveModelState()
	registry.liveModelsMu.Unlock()

	registry.RefreshLive(context.Background())

	// In-memory state cleared (no providers have auth, so no re-fetch populates it).
	registry.liveModelsMu.RLock()
	n := len(registry.liveModels)
	registry.liveModelsMu.RUnlock()
	if n != 0 {
		t.Errorf("expected liveModels cleared, got %d entries", n)
	}

	// Disk caches for live-models-* removed.
	if _, err := os.Stat(liveCachePath(tmpDir, "prov-a")); !os.IsNotExist(err) {
		t.Errorf("expected prov-a cache removed, err=%v", err)
	}
	if _, err := os.Stat(liveCachePath(tmpDir, "prov-b")); !os.IsNotExist(err) {
		t.Errorf("expected prov-b cache removed, err=%v", err)
	}
	// Unrelated file preserved.
	if _, err := os.Stat(keepPath); err != nil {
		t.Errorf("unrelated file was deleted: %v", err)
	}
}
