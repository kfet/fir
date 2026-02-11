package tools

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func createTestImage(w, h int) string {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestResizeImage_SmallImage_NoResize(t *testing.T) {
	b64 := createTestImage(100, 100)
	result := ResizeImage(b64, "image/png", nil)

	if result.WasResized {
		t.Error("expected no resize for small image")
	}
	if result.OriginalWidth != 100 || result.OriginalHeight != 100 {
		t.Errorf("wrong original dimensions: %dx%d", result.OriginalWidth, result.OriginalHeight)
	}
	if result.Width != 100 || result.Height != 100 {
		t.Errorf("wrong dimensions: %dx%d", result.Width, result.Height)
	}
}

func TestResizeImage_LargeImage_Resized(t *testing.T) {
	b64 := createTestImage(3000, 2000)
	result := ResizeImage(b64, "image/png", nil)

	if !result.WasResized {
		t.Error("expected resize for large image")
	}
	if result.Width > 2000 {
		t.Errorf("width %d exceeds max 2000", result.Width)
	}
	if result.Height > 2000 {
		t.Errorf("height %d exceeds max 2000", result.Height)
	}
	if result.OriginalWidth != 3000 || result.OriginalHeight != 2000 {
		t.Errorf("wrong original dimensions: %dx%d", result.OriginalWidth, result.OriginalHeight)
	}
	if result.Data == "" {
		t.Error("expected non-empty data")
	}
}

func TestResizeImage_CustomOptions(t *testing.T) {
	b64 := createTestImage(500, 500)
	result := ResizeImage(b64, "image/png", &ResizeImageOptions{
		MaxWidth:  200,
		MaxHeight: 200,
	})

	if !result.WasResized {
		t.Error("expected resize with custom max dimensions")
	}
	if result.Width > 200 || result.Height > 200 {
		t.Errorf("dimensions %dx%d exceed custom max 200x200", result.Width, result.Height)
	}
}

func TestResizeImage_InvalidBase64(t *testing.T) {
	result := ResizeImage("not-valid-base64!!!", "image/png", nil)
	// Should return original data unchanged
	if result.Data != "not-valid-base64!!!" {
		t.Error("expected original data returned for invalid base64")
	}
	if result.WasResized {
		t.Error("expected no resize for invalid input")
	}
}

func TestFormatDimensionNote(t *testing.T) {
	// Not resized
	r := ResizedImage{WasResized: false}
	if note := FormatDimensionNote(r); note != "" {
		t.Errorf("expected empty note for non-resized, got %q", note)
	}

	// Resized
	r = ResizedImage{
		WasResized:     true,
		OriginalWidth:  3000,
		OriginalHeight: 2000,
		Width:          1500,
		Height:         1000,
	}
	note := FormatDimensionNote(r)
	if note == "" {
		t.Error("expected non-empty note for resized image")
	}
	if !contains(note, "3000x2000") || !contains(note, "1500x1000") {
		t.Errorf("note missing dimensions: %s", note)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
