// Ported from: packages/tui/src/terminal-image.ts
// Upstream hash: 1caadb2e
package tui

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"sync"
)

// ImageProtocol identifies the terminal image protocol.
type ImageProtocol string

const (
	ImageProtocolKitty  ImageProtocol = "kitty"
	ImageProtocolITerm2 ImageProtocol = "iterm2"
	ImageProtocolNone   ImageProtocol = ""
)

// TerminalCapabilities describes what the terminal supports.
type TerminalCapabilities struct {
	Images     ImageProtocol
	TrueColor  bool
	Hyperlinks bool
}

// CellDimensions holds cell pixel dimensions.
type CellDimensions struct {
	WidthPx  int
	HeightPx int
}

// ImageDimensions holds image pixel dimensions.
type ImageDimensions struct {
	WidthPx  int
	HeightPx int
}

// ImageRenderOptions controls image rendering.
type ImageRenderOptions struct {
	MaxWidthCells       int
	MaxHeightCells      int
	PreserveAspectRatio *bool
	ImageID             int
}

var (
	capMu              sync.Mutex
	cachedCapabilities *TerminalCapabilities
	cellDims           = CellDimensions{WidthPx: 9, HeightPx: 18}
)

// GetCellDimensions returns the current cell pixel dimensions.
func GetCellDimensions() CellDimensions {
	return cellDims
}

// SetCellDimensions updates the cell pixel dimensions.
func SetCellDimensions(dims CellDimensions) {
	cellDims = dims
}

// DetectCapabilities detects terminal image/color capabilities from environment.
func DetectCapabilities() TerminalCapabilities {
	termProgram := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	termEnv := strings.ToLower(os.Getenv("TERM"))
	colorTerm := strings.ToLower(os.Getenv("COLORTERM"))

	if os.Getenv("KITTY_WINDOW_ID") != "" || termProgram == "kitty" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if termProgram == "ghostty" || strings.Contains(termEnv, "ghostty") || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if os.Getenv("WEZTERM_PANE") != "" || termProgram == "wezterm" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if os.Getenv("ITERM_SESSION_ID") != "" || termProgram == "iterm.app" {
		return TerminalCapabilities{Images: ImageProtocolITerm2, TrueColor: true, Hyperlinks: true}
	}
	if termProgram == "vscode" || termProgram == "alacritty" {
		return TerminalCapabilities{Images: ImageProtocolNone, TrueColor: true, Hyperlinks: true}
	}

	trueColor := colorTerm == "truecolor" || colorTerm == "24bit"
	return TerminalCapabilities{Images: ImageProtocolNone, TrueColor: trueColor, Hyperlinks: true}
}

// GetCapabilities returns cached terminal capabilities.
func GetCapabilities() TerminalCapabilities {
	capMu.Lock()
	defer capMu.Unlock()
	if cachedCapabilities == nil {
		caps := DetectCapabilities()
		cachedCapabilities = &caps
	}
	return *cachedCapabilities
}

// ResetCapabilitiesCache clears the cached capabilities.
func ResetCapabilitiesCache() {
	capMu.Lock()
	defer capMu.Unlock()
	cachedCapabilities = nil
}

const (
	kittyPrefix  = "\x1b_G"
	iterm2Prefix = "\x1b]1337;File="
)

// IsImageLine returns true if the line contains an image escape sequence.
func IsImageLine(line string) bool {
	return strings.Contains(line, kittyPrefix) || strings.Contains(line, iterm2Prefix)
}

// AllocateImageID generates a random Kitty graphics protocol image ID.
func AllocateImageID() int {
	return rand.Intn(math.MaxInt) + 1
}

// EncodeKitty encodes image data as a Kitty graphics protocol sequence.
func EncodeKitty(base64Data string, columns, rows, imageID int) string {
	const chunkSize = 4096

	params := []string{"a=T", "f=100", "q=2"}
	if columns > 0 {
		params = append(params, fmt.Sprintf("c=%d", columns))
	}
	if rows > 0 {
		params = append(params, fmt.Sprintf("r=%d", rows))
	}
	if imageID > 0 {
		params = append(params, fmt.Sprintf("i=%d", imageID))
	}

	paramStr := strings.Join(params, ",")

	if len(base64Data) <= chunkSize {
		return fmt.Sprintf("\x1b_G%s;%s\x1b\\", paramStr, base64Data)
	}

	var sb strings.Builder
	offset := 0
	isFirst := true

	for offset < len(base64Data) {
		end := offset + chunkSize
		if end > len(base64Data) {
			end = len(base64Data)
		}
		chunk := base64Data[offset:end]
		isLast := end >= len(base64Data)

		if isFirst {
			fmt.Fprintf(&sb, "\x1b_G%s,m=1;%s\x1b\\", paramStr, chunk)
			isFirst = false
		} else if isLast {
			fmt.Fprintf(&sb, "\x1b_Gm=0;%s\x1b\\", chunk)
		} else {
			fmt.Fprintf(&sb, "\x1b_Gm=1;%s\x1b\\", chunk)
		}

		offset += chunkSize
	}

	return sb.String()
}

// DeleteKittyImage returns the escape sequence to delete a Kitty image by ID.
func DeleteKittyImage(imageID int) string {
	return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d\x1b\\", imageID)
}

// DeleteAllKittyImages returns the escape sequence to delete all visible Kitty images.
func DeleteAllKittyImages() string {
	return "\x1b_Ga=d,d=A\x1b\\"
}

// EncodeITerm2 encodes image data as an iTerm2 inline image sequence.
func EncodeITerm2(base64Data string, width, height string, name string, preserveAspectRatio bool) string {
	params := []string{"inline=1"}
	if width != "" {
		params = append(params, "width="+width)
	}
	if height != "" {
		params = append(params, "height="+height)
	}
	if name != "" {
		nameB64 := base64.StdEncoding.EncodeToString([]byte(name))
		params = append(params, "name="+nameB64)
	}
	if !preserveAspectRatio {
		params = append(params, "preserveAspectRatio=0")
	}

	return fmt.Sprintf("\x1b]1337;File=%s:%s\x07", strings.Join(params, ";"), base64Data)
}

// CalculateImageRows calculates how many terminal rows an image needs.
func CalculateImageRows(imageDims ImageDimensions, targetWidthCells int, cellDims CellDimensions) int {
	targetWidthPx := float64(targetWidthCells) * float64(cellDims.WidthPx)
	scale := targetWidthPx / float64(imageDims.WidthPx)
	scaledHeightPx := float64(imageDims.HeightPx) * scale
	rows := int(math.Ceil(scaledHeightPx / float64(cellDims.HeightPx)))
	if rows < 1 {
		return 1
	}
	return rows
}

// GetPngDimensions extracts dimensions from a base64-encoded PNG.
func GetPngDimensions(base64Data string) *ImageDimensions {
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil || len(data) < 24 {
		return nil
	}
	// PNG magic: 0x89 P N G
	if data[0] != 0x89 || data[1] != 0x50 || data[2] != 0x4e || data[3] != 0x47 {
		return nil
	}
	width := binary.BigEndian.Uint32(data[16:20])
	height := binary.BigEndian.Uint32(data[20:24])
	return &ImageDimensions{WidthPx: int(width), HeightPx: int(height)}
}

// GetJpegDimensions extracts dimensions from a base64-encoded JPEG.
func GetJpegDimensions(base64Data string) *ImageDimensions {
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil || len(data) < 2 {
		return nil
	}
	if data[0] != 0xff || data[1] != 0xd8 {
		return nil
	}
	offset := 2
	for offset < len(data)-9 {
		if data[offset] != 0xff {
			offset++
			continue
		}
		marker := data[offset+1]
		if marker >= 0xc0 && marker <= 0xc2 {
			height := binary.BigEndian.Uint16(data[offset+5 : offset+7])
			width := binary.BigEndian.Uint16(data[offset+7 : offset+9])
			return &ImageDimensions{WidthPx: int(width), HeightPx: int(height)}
		}
		if offset+3 >= len(data) {
			return nil
		}
		length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		if length < 2 {
			return nil
		}
		offset += 2 + length
	}
	return nil
}

// GetGifDimensions extracts dimensions from a base64-encoded GIF.
func GetGifDimensions(base64Data string) *ImageDimensions {
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil || len(data) < 10 {
		return nil
	}
	sig := string(data[:6])
	if sig != "GIF87a" && sig != "GIF89a" {
		return nil
	}
	width := binary.LittleEndian.Uint16(data[6:8])
	height := binary.LittleEndian.Uint16(data[8:10])
	return &ImageDimensions{WidthPx: int(width), HeightPx: int(height)}
}

// GetWebpDimensions extracts dimensions from a base64-encoded WebP.
func GetWebpDimensions(base64Data string) *ImageDimensions {
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil || len(data) < 30 {
		return nil
	}
	if string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil
	}
	chunk := string(data[12:16])
	switch chunk {
	case "VP8 ":
		if len(data) < 30 {
			return nil
		}
		width := int(binary.LittleEndian.Uint16(data[26:28])) & 0x3fff
		height := int(binary.LittleEndian.Uint16(data[28:30])) & 0x3fff
		return &ImageDimensions{WidthPx: width, HeightPx: height}
	case "VP8L":
		if len(data) < 25 {
			return nil
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		width := int(bits&0x3fff) + 1
		height := int((bits>>14)&0x3fff) + 1
		return &ImageDimensions{WidthPx: width, HeightPx: height}
	case "VP8X":
		if len(data) < 30 {
			return nil
		}
		width := int(data[24]) | int(data[25])<<8 | int(data[26])<<16 + 1
		height := int(data[27]) | int(data[28])<<8 | int(data[29])<<16 + 1
		return &ImageDimensions{WidthPx: width, HeightPx: height}
	}
	return nil
}

// GetImageDimensions extracts dimensions from a base64-encoded image by MIME type.
func GetImageDimensions(base64Data, mimeType string) *ImageDimensions {
	switch mimeType {
	case "image/png":
		return GetPngDimensions(base64Data)
	case "image/jpeg":
		return GetJpegDimensions(base64Data)
	case "image/gif":
		return GetGifDimensions(base64Data)
	case "image/webp":
		return GetWebpDimensions(base64Data)
	}
	return nil
}

// ImageRenderResult is the result of rendering an image.
type ImageRenderResult struct {
	Sequence string
	Rows     int
	ImageID  int
}

// RenderImage renders an image using the detected terminal protocol.
func RenderImage(base64Data string, imageDims ImageDimensions, opts ImageRenderOptions) *ImageRenderResult {
	caps := GetCapabilities()
	if caps.Images == ImageProtocolNone {
		return nil
	}

	maxWidth := opts.MaxWidthCells
	if maxWidth <= 0 {
		maxWidth = 80
	}
	rows := CalculateImageRows(imageDims, maxWidth, GetCellDimensions())

	switch caps.Images {
	case ImageProtocolKitty:
		seq := EncodeKitty(base64Data, maxWidth, rows, opts.ImageID)
		return &ImageRenderResult{Sequence: seq, Rows: rows, ImageID: opts.ImageID}
	case ImageProtocolITerm2:
		preserve := true
		if opts.PreserveAspectRatio != nil {
			preserve = *opts.PreserveAspectRatio
		}
		seq := EncodeITerm2(base64Data, fmt.Sprintf("%d", maxWidth), "auto", "", preserve)
		return &ImageRenderResult{Sequence: seq, Rows: rows}
	}
	return nil
}

// ImageFallback returns a text placeholder for images that can't be displayed.
func ImageFallback(mimeType string, dims *ImageDimensions, filename string) string {
	var parts []string
	if filename != "" {
		parts = append(parts, filename)
	}
	parts = append(parts, fmt.Sprintf("[%s]", mimeType))
	if dims != nil {
		parts = append(parts, fmt.Sprintf("%dx%d", dims.WidthPx, dims.HeightPx))
	}
	return fmt.Sprintf("[Image: %s]", strings.Join(parts, " "))
}
