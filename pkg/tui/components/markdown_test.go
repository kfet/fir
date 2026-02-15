package components

import (
	"strings"
	"testing"

	"github.com/kfet/tau/pkg/tui"
)

func identityTheme() MarkdownTheme {
	id := func(s string) string { return s }
	return MarkdownTheme{
		Heading:         id,
		Link:            id,
		LinkURL:         id,
		Code:            func(s string) string { return "`" + s + "`" },
		CodeBlock:       id,
		CodeBlockBorder: id,
		Quote:           id,
		QuoteBorder:     id,
		HR:              id,
		ListBullet:      id,
		Bold:            func(s string) string { return "**" + s + "**" },
		Italic:          func(s string) string { return "_" + s + "_" },
		Strikethrough:   func(s string) string { return "~~" + s + "~~" },
		Underline:       func(s string) string { return "__" + s + "__" },
	}
}

func TestMarkdown_Empty(t *testing.T) {
	md := NewMarkdown("", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	if len(lines) != 0 {
		t.Errorf("expected 0 lines for empty, got %d", len(lines))
	}
}

func TestMarkdown_PlainText(t *testing.T) {
	md := NewMarkdown("Hello world", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "Hello world") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'Hello world' in output, got %v", lines)
	}
}

func TestMarkdown_Heading(t *testing.T) {
	md := NewMarkdown("# Title", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "Title") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'Title' in output, got %v", lines)
	}
}

func TestMarkdown_Bold(t *testing.T) {
	md := NewMarkdown("**bold text**", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "**bold text**") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected bold markers in output, got %v", lines)
	}
}

func TestMarkdown_Italic(t *testing.T) {
	md := NewMarkdown("*italic text*", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "_italic text_") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected italic markers in output, got %v", lines)
	}
}

func TestMarkdown_InlineCode(t *testing.T) {
	md := NewMarkdown("Use `code` here", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "`code`") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected inline code in output, got %v", lines)
	}
}

func TestMarkdown_CodeBlock(t *testing.T) {
	md := NewMarkdown("```go\nfmt.Println()\n```", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	foundBorder := false
	foundCode := false
	for _, l := range lines {
		if strings.Contains(l, "```") {
			foundBorder = true
		}
		if strings.Contains(l, "fmt.Println()") {
			foundCode = true
		}
	}
	if !foundBorder {
		t.Error("expected code block border")
	}
	if !foundCode {
		t.Errorf("expected code content, got %v", lines)
	}
}

func TestMarkdown_UnorderedList(t *testing.T) {
	md := NewMarkdown("- item1\n- item2\n- item3", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	items := 0
	for _, l := range lines {
		if strings.Contains(l, "- ") {
			items++
		}
	}
	if items < 3 {
		t.Errorf("expected 3 list items, found %d in %v", items, lines)
	}
}

func TestMarkdown_OrderedList(t *testing.T) {
	md := NewMarkdown("1. first\n2. second\n3. third", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	found1 := false
	for _, l := range lines {
		if strings.Contains(l, "1. ") || strings.Contains(l, "1.") {
			found1 = true
			break
		}
	}
	if !found1 {
		t.Errorf("expected ordered list markers, got %v", lines)
	}
}

func TestMarkdown_Blockquote(t *testing.T) {
	md := NewMarkdown("> quoted text", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "│") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected blockquote border, got %v", lines)
	}
}

func TestMarkdown_HR(t *testing.T) {
	md := NewMarkdown("---", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "─") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected horizontal rule, got %v", lines)
	}
}

func TestMarkdown_Padding(t *testing.T) {
	md := NewMarkdown("hello", 2, 1, identityTheme(), nil)
	lines := md.Render(80)
	// paddingY=1 → at least 3 lines (top, content, bottom)
	if len(lines) < 3 {
		t.Fatalf("expected >= 3 lines with padding, got %d", len(lines))
	}
	// Content line should have left margin
	for _, line := range lines {
		if strings.Contains(line, "hello") {
			if !strings.HasPrefix(line, "  ") {
				t.Errorf("expected left padding, got %q", line)
			}
			break
		}
	}
}

func TestMarkdown_DefaultTextStyle(t *testing.T) {
	style := &DefaultTextStyle{
		Color: func(s string) string { return "[color]" + s + "[/color]" },
	}
	md := NewMarkdown("styled", 0, 0, identityTheme(), style)
	lines := md.Render(80)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "[color]styled[/color]") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected default text style, got %v", lines)
	}
}

func TestMarkdown_Cache(t *testing.T) {
	md := NewMarkdown("cached", 0, 0, identityTheme(), nil)
	lines1 := md.Render(80)
	lines2 := md.Render(80)
	if len(lines1) != len(lines2) {
		t.Error("cache mismatch")
	}
}

func TestMarkdown_Invalidate(t *testing.T) {
	md := NewMarkdown("original", 0, 0, identityTheme(), nil)
	_ = md.Render(80)
	md.SetText("changed")
	lines := md.Render(80)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "changed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'changed' after invalidation, got %v", lines)
	}
}

func TestMarkdown_Link(t *testing.T) {
	md := NewMarkdown("[click](http://example.com)", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	foundLink := false
	foundURL := false
	for _, l := range lines {
		if strings.Contains(l, "click") {
			foundLink = true
		}
		if strings.Contains(l, "example.com") {
			foundURL = true
		}
	}
	if !foundLink {
		t.Error("expected link text")
	}
	if !foundURL {
		t.Error("expected link URL")
	}
}

func TestMarkdown_WidthPadding(t *testing.T) {
	md := NewMarkdown("hi", 0, 0, identityTheme(), nil)
	lines := md.Render(20)
	if len(lines) == 0 {
		t.Fatal("expected output")
	}
	// Lines should be padded to width
	for _, l := range lines {
		w := tui.VisibleWidth(l)
		if w != 20 {
			t.Errorf("expected width 20, got %d for %q", w, l)
		}
	}
}

func TestMarkdown_NestedList(t *testing.T) {
	md := NewMarkdown("- parent\n  - child\n  - child2\n- parent2", 0, 0, identityTheme(), nil)
	lines := md.Render(80)
	if len(lines) < 3 {
		t.Errorf("expected >= 3 lines for nested list, got %d: %v", len(lines), lines)
	}
}
