package resources

import (
	"testing"
)

func TestParseFrontmatter_WithFrontmatter(t *testing.T) {
	content := "---\nname: test\ndescription: A test skill\n---\n\nBody content here"
	result := ParseFrontmatter(content)

	if result.Frontmatter["name"] != "test" {
		t.Errorf("name = %v, want test", result.Frontmatter["name"])
	}
	if result.Frontmatter["description"] != "A test skill" {
		t.Errorf("description = %v, want 'A test skill'", result.Frontmatter["description"])
	}
	if result.Body != "Body content here" {
		t.Errorf("body = %q, want 'Body content here'", result.Body)
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	content := "Just a body\nwith multiple lines"
	result := ParseFrontmatter(content)

	if len(result.Frontmatter) != 0 {
		t.Errorf("frontmatter = %v, want empty", result.Frontmatter)
	}
	if result.Body != content {
		t.Errorf("body = %q, want %q", result.Body, content)
	}
}

func TestParseFrontmatter_EmptyFrontmatter(t *testing.T) {
	content := "---\n---\nBody"
	result := ParseFrontmatter(content)

	if len(result.Frontmatter) != 0 {
		t.Errorf("frontmatter = %v, want empty", result.Frontmatter)
	}
	if result.Body != "Body" {
		t.Errorf("body = %q, want 'Body'", result.Body)
	}
}

func TestParseFrontmatter_UnclosedFrontmatter(t *testing.T) {
	content := "---\nname: test\nNo closing delimiter"
	result := ParseFrontmatter(content)

	// Should treat as no frontmatter
	if len(result.Frontmatter) != 0 {
		t.Errorf("frontmatter = %v, want empty", result.Frontmatter)
	}
	if result.Body != content {
		t.Errorf("body = %q, want %q", result.Body, content)
	}
}

func TestParseFrontmatter_CRLFNewlines(t *testing.T) {
	content := "---\r\nname: test\r\n---\r\nBody"
	result := ParseFrontmatter(content)

	if result.Frontmatter["name"] != "test" {
		t.Errorf("name = %v, want test", result.Frontmatter["name"])
	}
	if result.Body != "Body" {
		t.Errorf("body = %q, want 'Body'", result.Body)
	}
}

func TestParseFrontmatter_BooleanValue(t *testing.T) {
	content := "---\nbuiltin: true\n---\nBody"
	result := ParseFrontmatter(content)

	if result.Frontmatter["builtin"] != true {
		t.Errorf("builtin = %v, want true", result.Frontmatter["builtin"])
	}
}

func TestParseFrontmatter_InvalidYAML(t *testing.T) {
	content := "---\n: : : invalid\n---\nBody"
	result := ParseFrontmatter(content)

	// Should return empty frontmatter on invalid YAML
	if result.Body != "Body" {
		t.Errorf("body = %q, want 'Body'", result.Body)
	}
}

func TestStripFrontmatter(t *testing.T) {
	content := "---\nname: test\n---\nBody content"
	body := StripFrontmatter(content)
	if body != "Body content" {
		t.Errorf("body = %q, want 'Body content'", body)
	}
}

func TestStripFrontmatter_NoFrontmatter(t *testing.T) {
	content := "Just body"
	body := StripFrontmatter(content)
	if body != "Just body" {
		t.Errorf("body = %q, want 'Just body'", body)
	}
}
