package providers

import (
	"net/http"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestBuildRequestHeaders_MergeOrder(t *testing.T) {
	model := &ai.Model{
		Headers: map[string]string{
			"Authorization": "model-override",
			"X-Model":       "yes",
		},
	}
	options := &ai.StreamOptions{
		Headers: map[string]string{
			"Authorization": "options-override",
			"X-Options":     "yes",
		},
	}
	auth := map[string]string{
		"Authorization": "Bearer base-key",
		"X-Auth":        "yes",
	}

	headers := BuildRequestHeaders(auth, model, options)

	// options wins over model wins over auth
	if headers["Authorization"] != "options-override" {
		t.Errorf("Authorization = %q, want %q", headers["Authorization"], "options-override")
	}
	if headers["X-Auth"] != "yes" {
		t.Errorf("X-Auth = %q, want %q", headers["X-Auth"], "yes")
	}
	if headers["X-Model"] != "yes" {
		t.Errorf("X-Model = %q, want %q", headers["X-Model"], "yes")
	}
	if headers["X-Options"] != "yes" {
		t.Errorf("X-Options = %q, want %q", headers["X-Options"], "yes")
	}
}

func TestBuildRequestHeaders_NilOptions(t *testing.T) {
	model := &ai.Model{
		Headers: map[string]string{"X-Model": "yes"},
	}
	auth := map[string]string{"Authorization": "Bearer key"}

	headers := BuildRequestHeaders(auth, model, nil)

	if headers["Authorization"] != "Bearer key" {
		t.Errorf("Authorization = %q, want %q", headers["Authorization"], "Bearer key")
	}
	if headers["X-Model"] != "yes" {
		t.Errorf("X-Model = %q, want %q", headers["X-Model"], "yes")
	}
}

func TestBuildRequestHeaders_NilModelHeaders(t *testing.T) {
	model := &ai.Model{} // no headers
	auth := map[string]string{"Authorization": "Bearer key"}

	headers := BuildRequestHeaders(auth, model, nil)

	if headers["Authorization"] != "Bearer key" {
		t.Errorf("Authorization = %q, want %q", headers["Authorization"], "Bearer key")
	}
	if len(headers) != 1 {
		t.Errorf("len = %d, want 1", len(headers))
	}
}

func TestBuildRequestHeaders_SkipPrefixes(t *testing.T) {
	model := &ai.Model{}
	options := &ai.StreamOptions{
		Headers: map[string]string{
			"x-anthropic-thinking-level":  "high",
			"x-anthropic-thinking-budget": "8192",
			"x-custom":                    "keep",
		},
	}

	headers := BuildRequestHeaders(nil, model, options, "x-anthropic-thinking-")

	if _, ok := headers["x-anthropic-thinking-level"]; ok {
		t.Error("x-anthropic-thinking-level should have been skipped")
	}
	if _, ok := headers["x-anthropic-thinking-budget"]; ok {
		t.Error("x-anthropic-thinking-budget should have been skipped")
	}
	if headers["x-custom"] != "keep" {
		t.Errorf("x-custom = %q, want %q", headers["x-custom"], "keep")
	}
}

func TestApplyHeaders(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	ApplyHeaders(req, map[string]string{
		"Authorization": "Bearer test",
		"X-Custom":      "value",
	})

	if got := req.Header.Get("Authorization"); got != "Bearer test" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer test")
	}
	if got := req.Header.Get("X-Custom"); got != "value" {
		t.Errorf("X-Custom = %q, want %q", got, "value")
	}
}
