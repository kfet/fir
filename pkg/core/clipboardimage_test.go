package core

import "testing"

func TestExtensionForImageMimeType(t *testing.T) {
	tests := []struct {
		mimeType string
		want     string
	}{
		{"image/png", "png"},
		{"image/jpeg", "jpg"},
		{"image/webp", "webp"},
		{"image/gif", "gif"},
		{"image/png; charset=utf-8", "png"},
		{"IMAGE/PNG", "png"},
		{"image/bmp", ""},
		{"text/plain", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := ExtensionForImageMimeType(tt.mimeType)
		if got != tt.want {
			t.Errorf("ExtensionForImageMimeType(%q) = %q, want %q", tt.mimeType, got, tt.want)
		}
	}
}

func TestBaseMimeType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"image/png", "image/png"},
		{"image/png; charset=utf-8", "image/png"},
		{"IMAGE/JPEG", "image/jpeg"},
		{"", ""},
	}

	for _, tt := range tests {
		got := baseMimeType(tt.input)
		if got != tt.want {
			t.Errorf("baseMimeType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReadClipboardImage_ReturnsNil(t *testing.T) {
	// Not implemented yet, should return nil
	result := ReadClipboardImage()
	if result != nil {
		t.Error("expected nil from unimplemented ReadClipboardImage")
	}
}
