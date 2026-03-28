package oauth

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRenderAuthPages writes all OAuth callback page variants to a temp dir.
// Run with: go test ./pkg/ai/oauth/ -run TestRenderAuthPages -v
// Then open the files in a browser to inspect.
func TestRenderAuthPages(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "fir-oauth-pages")
	os.MkdirAll(dir, 0o755)

	pages := []struct {
		name    string
		title   string
		icon    string
		heading string
		message string
	}{
		{"success.html", "Authentication Successful", "✓", "You're all set", "You can close this window and return to the terminal."},
		{"error.html", "Authentication Failed", "⚠️", "Authentication Failed", "Error: access_denied"},
		{"csrf.html", "Authentication Failed", "🚫", "Authentication Failed", "State mismatch — please try again."},
		{"missing.html", "Authentication Failed", "⚠️", "Authentication Failed", "Missing authorization code."},
	}

	for _, p := range pages {
		path := filepath.Join(dir, p.name)
		html := renderAuthPage(p.title, p.icon, p.heading, p.message)
		if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
			t.Fatalf("write %s: %v", p.name, err)
		}
		t.Logf("wrote %s", path)
	}
	t.Logf("\nOpen in browser: open %s/*.html", dir)
}
