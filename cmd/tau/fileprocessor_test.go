package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessFileArguments_TextFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hello.txt")
	os.WriteFile(file, []byte("hello world"), 0o644)

	result, err := ProcessFileArguments([]string{"hello.txt"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Images) != 0 {
		t.Errorf("expected 0 images, got %d", len(result.Images))
	}
	if !strings.Contains(result.Text, "hello world") {
		t.Errorf("expected text to contain file content, got %q", result.Text)
	}
	if !strings.Contains(result.Text, `path="hello.txt"`) {
		t.Errorf("expected text to contain file path tag, got %q", result.Text)
	}
}

func TestProcessFileArguments_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "abs.txt")
	os.WriteFile(file, []byte("absolute"), 0o644)

	result, err := ProcessFileArguments([]string{file}, "/some/other/cwd")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "absolute") {
		t.Errorf("expected text to contain file content, got %q", result.Text)
	}
}

func TestProcessFileArguments_ImageFile(t *testing.T) {
	dir := t.TempDir()
	// Write a minimal PNG-like blob (contents don't matter for this test)
	pngData := []byte{0x89, 0x50, 0x4E, 0x47}
	file := filepath.Join(dir, "test.png")
	os.WriteFile(file, pngData, 0o644)

	result, err := ProcessFileArguments([]string{"test.png"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "" {
		t.Errorf("expected empty text for image-only input, got %q", result.Text)
	}
	if len(result.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(result.Images))
	}
	if result.Images[0].MimeType != "image/png" {
		t.Errorf("expected mime type image/png, got %q", result.Images[0].MimeType)
	}
	if result.Images[0].Data != base64.StdEncoding.EncodeToString(pngData) {
		t.Error("image data mismatch")
	}
}

func TestProcessFileArguments_MixedFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Title"), 0o644)
	os.WriteFile(filepath.Join(dir, "photo.jpg"), []byte{0xFF, 0xD8}, 0o644)

	result, err := ProcessFileArguments([]string{"readme.md", "photo.jpg"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "# Title") {
		t.Error("expected text file content")
	}
	if len(result.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(result.Images))
	}
	if result.Images[0].MimeType != "image/jpeg" {
		t.Errorf("expected image/jpeg, got %q", result.Images[0].MimeType)
	}
}

func TestProcessFileArguments_MultipleTextFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("aaa"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bbb"), 0o644)

	result, err := ProcessFileArguments([]string{"a.txt", "b.txt"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "aaa") || !strings.Contains(result.Text, "bbb") {
		t.Errorf("expected both file contents, got %q", result.Text)
	}
	if !strings.Contains(result.Text, `path="a.txt"`) || !strings.Contains(result.Text, `path="b.txt"`) {
		t.Errorf("expected both file path tags, got %q", result.Text)
	}
}

func TestProcessFileArguments_NonexistentFile(t *testing.T) {
	_, err := ProcessFileArguments([]string{"nonexistent.txt"}, t.TempDir())
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestProcessFileArguments_NonexistentImage(t *testing.T) {
	_, err := ProcessFileArguments([]string{"missing.png"}, t.TempDir())
	if err == nil {
		t.Error("expected error for nonexistent image")
	}
}

func TestProcessFileArguments_Empty(t *testing.T) {
	result, err := ProcessFileArguments(nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "" {
		t.Errorf("expected empty text, got %q", result.Text)
	}
	if len(result.Images) != 0 {
		t.Errorf("expected 0 images, got %d", len(result.Images))
	}
}

func TestProcessFileArguments_AllImageExtensions(t *testing.T) {
	dir := t.TempDir()
	exts := map[string]string{
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".webp": "image/webp",
	}
	for ext, wantMime := range exts {
		fname := "test" + ext
		os.WriteFile(filepath.Join(dir, fname), []byte{0x00}, 0o644)

		result, err := ProcessFileArguments([]string{fname}, dir)
		if err != nil {
			t.Fatalf("ext %s: %v", ext, err)
		}
		if len(result.Images) != 1 {
			t.Fatalf("ext %s: expected 1 image, got %d", ext, len(result.Images))
		}
		if result.Images[0].MimeType != wantMime {
			t.Errorf("ext %s: expected %q, got %q", ext, wantMime, result.Images[0].MimeType)
		}
	}
}
