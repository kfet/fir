package tui

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

func TestDetectCapabilities_Default(t *testing.T) {
	// In test env, typically no terminal env vars set
	caps := DetectCapabilities()
	// Just verify it returns without error
	if caps.Images == ImageProtocolKitty || caps.Images == ImageProtocolITerm2 {
		t.Log("Detected image protocol in test environment (OK if running in such terminal)")
	}
}

func TestGetCapabilities_Cached(t *testing.T) {
	ResetCapabilitiesCache()
	caps1 := GetCapabilities()
	caps2 := GetCapabilities()
	if caps1.Images != caps2.Images || caps1.TrueColor != caps2.TrueColor {
		t.Error("cached capabilities should be identical")
	}
}

func TestIsImageLine(t *testing.T) {
	tests := []struct {
		line   string
		expect bool
	}{
		{"\x1b_Ga=T,f=100;data\x1b\\", true},
		{"\x1b]1337;File=inline=1:data\x07", true},
		{"just plain text", false},
		{"", false},
		{"\x1b[31mred text", false},
		// Multi-row: cursor-up prefix then kitty
		{"\x1b[1A\x1b_Gmore...\x1b\\", true},
	}

	for _, tt := range tests {
		if got := IsImageLine(tt.line); got != tt.expect {
			t.Errorf("IsImageLine(%q) = %v, want %v", tt.line, got, tt.expect)
		}
	}
}

func TestAllocateImageID(t *testing.T) {
	id1 := AllocateImageID()
	id2 := AllocateImageID()
	if id1 < 1 || id2 < 1 {
		t.Error("IDs should be >= 1")
	}
	// Very unlikely to be equal
	if id1 == id2 {
		t.Log("Warning: two random IDs were equal (extremely unlikely)")
	}
}

func TestEncodeKitty_Small(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("small"))
	result := EncodeKitty(data, 40, 10, 0)
	if !strings.HasPrefix(result, "\x1b_G") {
		t.Error("expected Kitty prefix")
	}
	if !strings.HasSuffix(result, "\x1b\\") {
		t.Error("expected Kitty suffix")
	}
	if !strings.Contains(result, "a=T") {
		t.Error("expected a=T param")
	}
	if !strings.Contains(result, "c=40") {
		t.Error("expected c=40")
	}
	if !strings.Contains(result, "r=10") {
		t.Error("expected r=10")
	}
}

func TestEncodeKitty_Chunked(t *testing.T) {
	// Create data > 4096 chars
	bigData := strings.Repeat("A", 5000)
	result := EncodeKitty(bigData, 0, 0, 42)
	// Should have m=1 (continuation) and m=0 (last)
	if !strings.Contains(result, "m=1") {
		t.Error("expected m=1 for chunked data")
	}
	if !strings.Contains(result, "m=0") {
		t.Error("expected m=0 for last chunk")
	}
	if !strings.Contains(result, "i=42") {
		t.Error("expected image ID i=42")
	}
}

func TestDeleteKittyImage(t *testing.T) {
	result := DeleteKittyImage(123)
	if !strings.Contains(result, "d=I,i=123") {
		t.Errorf("unexpected delete sequence: %q", result)
	}
}

func TestDeleteAllKittyImages(t *testing.T) {
	result := DeleteAllKittyImages()
	if !strings.Contains(result, "d=A") {
		t.Errorf("unexpected delete-all sequence: %q", result)
	}
}

func TestEncodeITerm2(t *testing.T) {
	data := "aGVsbG8="
	result := EncodeITerm2(data, "80", "auto", "test.png", true)
	if !strings.HasPrefix(result, "\x1b]1337;File=") {
		t.Error("expected iTerm2 prefix")
	}
	if !strings.HasSuffix(result, "\x07") {
		t.Error("expected BEL suffix")
	}
	if !strings.Contains(result, "inline=1") {
		t.Error("expected inline=1")
	}
	if !strings.Contains(result, "width=80") {
		t.Error("expected width=80")
	}
}

func TestEncodeITerm2_NoAspect(t *testing.T) {
	result := EncodeITerm2("data", "", "", "", false)
	if !strings.Contains(result, "preserveAspectRatio=0") {
		t.Error("expected preserveAspectRatio=0")
	}
}

func TestCalculateImageRows(t *testing.T) {
	dims := ImageDimensions{WidthPx: 800, HeightPx: 600}
	cells := CellDimensions{WidthPx: 10, HeightPx: 20}
	rows := CalculateImageRows(dims, 40, cells)
	// 40 cells * 10px = 400px target width
	// scale = 400/800 = 0.5
	// scaled height = 600 * 0.5 = 300px
	// rows = ceil(300/20) = 15
	if rows != 15 {
		t.Errorf("expected 15 rows, got %d", rows)
	}
}

func TestCalculateImageRows_MinOne(t *testing.T) {
	dims := ImageDimensions{WidthPx: 10000, HeightPx: 1}
	cells := CellDimensions{WidthPx: 10, HeightPx: 20}
	rows := CalculateImageRows(dims, 40, cells)
	if rows < 1 {
		t.Errorf("expected at least 1 row, got %d", rows)
	}
}

func TestGetPngDimensions(t *testing.T) {
	// Construct minimal PNG header
	header := make([]byte, 24)
	header[0] = 0x89
	header[1] = 0x50 // P
	header[2] = 0x4e // N
	header[3] = 0x47 // G
	binary.BigEndian.PutUint32(header[16:20], 640)
	binary.BigEndian.PutUint32(header[20:24], 480)

	b64 := base64.StdEncoding.EncodeToString(header)
	dims := GetPngDimensions(b64)
	if dims == nil {
		t.Fatal("expected non-nil dimensions")
	}
	if dims.WidthPx != 640 || dims.HeightPx != 480 {
		t.Errorf("expected 640x480, got %dx%d", dims.WidthPx, dims.HeightPx)
	}
}

func TestGetPngDimensions_Invalid(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("not a png"))
	dims := GetPngDimensions(b64)
	if dims != nil {
		t.Error("expected nil for non-PNG data")
	}
}

func TestGetPngDimensions_TooShort(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47})
	dims := GetPngDimensions(b64)
	if dims != nil {
		t.Error("expected nil for too-short data")
	}
}

func TestGetJpegDimensions(t *testing.T) {
	// Construct minimal JPEG with SOF0 marker
	data := make([]byte, 20)
	data[0] = 0xff
	data[1] = 0xd8 // SOI
	data[2] = 0xff
	data[3] = 0xc0 // SOF0
	data[4] = 0x00
	data[5] = 0x11                              // length
	data[6] = 0x08                              // precision
	binary.BigEndian.PutUint16(data[7:9], 480)  // height
	binary.BigEndian.PutUint16(data[9:11], 640) // width

	b64 := base64.StdEncoding.EncodeToString(data)
	dims := GetJpegDimensions(b64)
	if dims == nil {
		t.Fatal("expected non-nil dimensions")
	}
	if dims.WidthPx != 640 || dims.HeightPx != 480 {
		t.Errorf("expected 640x480, got %dx%d", dims.WidthPx, dims.HeightPx)
	}
}

func TestGetJpegDimensions_Invalid(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("not jpeg"))
	if GetJpegDimensions(b64) != nil {
		t.Error("expected nil for non-JPEG data")
	}
}

func TestGetGifDimensions(t *testing.T) {
	data := make([]byte, 10)
	copy(data, "GIF89a")
	binary.LittleEndian.PutUint16(data[6:8], 320)
	binary.LittleEndian.PutUint16(data[8:10], 240)

	b64 := base64.StdEncoding.EncodeToString(data)
	dims := GetGifDimensions(b64)
	if dims == nil {
		t.Fatal("expected non-nil dimensions")
	}
	if dims.WidthPx != 320 || dims.HeightPx != 240 {
		t.Errorf("expected 320x240, got %dx%d", dims.WidthPx, dims.HeightPx)
	}
}

func TestGetGifDimensions_87a(t *testing.T) {
	data := make([]byte, 10)
	copy(data, "GIF87a")
	binary.LittleEndian.PutUint16(data[6:8], 100)
	binary.LittleEndian.PutUint16(data[8:10], 50)

	b64 := base64.StdEncoding.EncodeToString(data)
	dims := GetGifDimensions(b64)
	if dims == nil || dims.WidthPx != 100 || dims.HeightPx != 50 {
		t.Errorf("expected 100x50, got %v", dims)
	}
}

func TestGetGifDimensions_Invalid(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("not a gif"))
	if GetGifDimensions(b64) != nil {
		t.Error("expected nil for non-GIF data")
	}
}

func TestGetImageDimensions_PNG(t *testing.T) {
	header := make([]byte, 24)
	header[0] = 0x89
	header[1] = 0x50
	header[2] = 0x4e
	header[3] = 0x47
	binary.BigEndian.PutUint32(header[16:20], 100)
	binary.BigEndian.PutUint32(header[20:24], 200)

	b64 := base64.StdEncoding.EncodeToString(header)
	dims := GetImageDimensions(b64, "image/png")
	if dims == nil || dims.WidthPx != 100 || dims.HeightPx != 200 {
		t.Errorf("expected 100x200, got %v", dims)
	}
}

func TestGetImageDimensions_Unknown(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("data"))
	if GetImageDimensions(b64, "image/bmp") != nil {
		t.Error("expected nil for unknown type")
	}
}

func TestImageFallback(t *testing.T) {
	result := ImageFallback("image/png", nil, "")
	if result != "[Image: [image/png]]" {
		t.Errorf("unexpected fallback: %q", result)
	}

	dims := &ImageDimensions{WidthPx: 800, HeightPx: 600}
	result = ImageFallback("image/jpeg", dims, "photo.jpg")
	if result != "[Image: photo.jpg [image/jpeg] 800x600]" {
		t.Errorf("unexpected fallback: %q", result)
	}
}

func TestRenderImage_NoProtocol(t *testing.T) {
	// Reset so we get fresh detection (likely no protocol in test env)
	ResetCapabilitiesCache()
	dims := ImageDimensions{WidthPx: 100, HeightPx: 100}
	result := RenderImage("data", dims, ImageRenderOptions{})
	// In test environment, typically no image protocol available
	if result != nil {
		t.Log("Image protocol available in test env, got result")
	}
}
