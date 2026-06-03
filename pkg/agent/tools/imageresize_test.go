// Ported from: packages/coding-agent/src/utils/image-resize.ts
// Upstream hash: 1caadb2e
package tools

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
)

func createTestImage(w, h int) string {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Fill pixels directly via the Pix slice — 100x faster than Set() per pixel.
	pix := img.Pix
	stride := img.Stride
	for y := 0; y < h; y++ {
		off := y * stride
		g := uint8(y % 256)
		for x := 0; x < w; x++ {
			i := off + x*4
			pix[i] = uint8(x % 256) // R
			pix[i+1] = g            // G
			pix[i+2] = 128          // B
			pix[i+3] = 255          // A
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
	b64 := createTestImage(2100, 1400)
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
	if result.OriginalWidth != 2100 || result.OriginalHeight != 1400 {
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

func TestResizeImage_LastResortFallback(t *testing.T) {
	// A 110×110 image with MaxBytes=1 forces the scale loop to exit early:
	//   scale=1.0 → 110×110 ≥ 100 but encoded size > 1 byte → no return
	//   scale=0.75 → 83×83 < 100 → break
	// The last-resort code then resizes to 25% of targetW/H and returns whatever it gets.
	b64 := createTestImage(110, 110)
	result := ResizeImage(b64, "image/png", &ResizeImageOptions{
		MaxBytes: 1, // impossibly small — guarantees last-resort path
	})

	if !result.WasResized {
		t.Error("expected WasResized=true on last-resort path")
	}
	if result.OriginalWidth != 110 || result.OriginalHeight != 110 {
		t.Errorf("expected original 110×110, got %dx%d", result.OriginalWidth, result.OriginalHeight)
	}
	// Last resort scales to 0.25 of target: round(110*0.25)=28
	if result.Width != 28 || result.Height != 28 {
		t.Errorf("expected last-resort size 28×28, got %dx%d", result.Width, result.Height)
	}
	if result.Data == "" {
		t.Error("expected non-empty data from last-resort path")
	}
	// Should be decodable base64
	raw, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		t.Fatalf("last-resort data is not valid base64: %v", err)
	}
	if len(raw) == 0 {
		t.Error("expected non-empty decoded bytes from last-resort path")
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
	if !strings.Contains(note, "3000x2000") || !strings.Contains(note, "1500x1000") {
		t.Errorf("note missing dimensions: %s", note)
	}
}
