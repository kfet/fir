package oauth

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// placeholderRe matches our __PLACEHOLDER__ pattern.
var placeholderRe = regexp.MustCompile(`__[A-Z][A-Z0-9_]*__`)

func TestRenderAuthPage_AllPlaceholdersReplaced(t *testing.T) {
	// Build lookup from the authoritative list in callback_server.go.
	known := make(map[string]bool, len(allPlaceholders))
	for _, p := range allPlaceholders {
		known[p] = true
	}

	// Every placeholder in the HTML template must be in allPlaceholders.
	for _, p := range placeholderRe.FindAllString(callbackPageHTML, -1) {
		if !known[p] {
			t.Errorf("template contains placeholder %q not in allPlaceholders — add it to callback_server.go", p)
		}
	}

	// Every entry in allPlaceholders must appear in the template.
	for _, p := range allPlaceholders {
		if !strings.Contains(callbackPageHTML, p) {
			t.Errorf("allPlaceholders lists %q but it does not appear in the template", p)
		}
	}

	// Render and confirm zero placeholders remain.
	result := renderAuthPage("Test Title", "✓", "Test Heading", "Test message")
	if remaining := placeholderRe.FindAllString(result, -1); len(remaining) > 0 {
		t.Errorf("rendered page still contains placeholders: %v", remaining)
	}

	// Confirm the values actually appear in the output.
	for _, want := range []string{"Test Title", "Test Heading", "Test message"} {
		if !strings.Contains(result, want) {
			t.Errorf("rendered page missing expected text %q", want)
		}
	}
}

func TestRenderAuthPage_HTMLEscaping(t *testing.T) {
	result := renderAuthPage("<script>alert(1)</script>", "⚠️", "a&b", "x<y")
	for _, bad := range []string{"<script>", "</script>"} {
		if strings.Contains(result, bad) {
			t.Errorf("rendered page contains unescaped HTML %q", bad)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "a&amp;b", "x&lt;y"} {
		if !strings.Contains(result, want) {
			t.Errorf("rendered page missing escaped text %q", want)
		}
	}
}

// TestRenderAuthPages writes all OAuth callback page variants to a temp dir.
// Run with: go test ./pkg/ai/oauth/ -run TestRenderAuthPages -v
// Then open the files in a browser to inspect.
func TestRenderAuthPages(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "fir-oauth-pages")
	os.MkdirAll(dir, 0o755)

	pages := []struct {
		name, title, icon, heading, message string
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
