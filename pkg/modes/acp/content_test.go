package acp

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestExtractPromptContent(t *testing.T) {
	blocks := []acpsdk.ContentBlock{
		{Text: &acpsdk.ContentBlockText{Text: "hello", Type: "text"}},
		{Text: &acpsdk.ContentBlockText{Text: "world", Type: "text"}},
		{Image: &acpsdk.ContentBlockImage{Data: "abc", MimeType: "image/png", Type: "image"}},
		{ResourceLink: &acpsdk.ContentBlockResourceLink{Uri: "file:///foo/bar", Type: "resource_link"}},
	}

	text, images := ExtractPromptContent(blocks)
	if text != "hello\nworld\n@/foo/bar" {
		t.Errorf("text = %q", text)
	}
	if len(images) != 1 || images[0].Data != "abc" {
		t.Errorf("images = %v", images)
	}
}

func TestMarkdownEscape(t *testing.T) {
	got := MarkdownEscape("hello world")
	want := "```\nhello world\n```"
	if got != want {
		t.Errorf("MarkdownEscape = %q, want %q", got, want)
	}

	got = MarkdownEscape("```code```")
	if got[:4] != "````" {
		t.Errorf("should use 4+ backticks for content with ```, got %q", got)
	}
}
