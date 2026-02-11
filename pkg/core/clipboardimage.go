// Ported from: packages/coding-agent/src/utils/clipboard-image.ts
// Upstream hash: 1caadb2e
package core

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ClipboardImage represents image data from the clipboard.
type ClipboardImage struct {
	Bytes    []byte
	MimeType string
}

var supportedImageMimeTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

const (
	defaultListTimeout = 1 * time.Second
	defaultReadTimeout = 3 * time.Second
	defaultMaxBuffer   = 50 * 1024 * 1024 // 50 MB
)

// ExtensionForImageMimeType returns the file extension for an image MIME type, or "".
func ExtensionForImageMimeType(mimeType string) string {
	base := baseMimeType(mimeType)
	switch base {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return ""
	}
}

func baseMimeType(mimeType string) string {
	parts := strings.SplitN(mimeType, ";", 2)
	return strings.TrimSpace(strings.ToLower(parts[0]))
}

func selectPreferredImageMimeType(mimeTypes []string) string {
	type normalized struct {
		raw  string
		base string
	}
	var items []normalized
	for _, t := range mimeTypes {
		trimmed := strings.TrimSpace(t)
		if trimmed == "" {
			continue
		}
		items = append(items, normalized{raw: trimmed, base: baseMimeType(trimmed)})
	}

	// Prefer supported types in order
	for _, preferred := range supportedImageMimeTypes {
		for _, item := range items {
			if item.base == preferred {
				return item.raw
			}
		}
	}

	// Fall back to any image/* type
	for _, item := range items {
		if strings.HasPrefix(item.base, "image/") {
			return item.raw
		}
	}
	return ""
}

func isSupportedImageMimeType(mimeType string) bool {
	base := baseMimeType(mimeType)
	for _, t := range supportedImageMimeTypes {
		if t == base {
			return true
		}
	}
	return false
}

// IsWaylandSession returns true if the session is Wayland.
func IsWaylandSession() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("XDG_SESSION_TYPE") == "wayland"
}

// runClipboardCommand runs a command with a timeout and returns its stdout.
func runClipboardCommand(name string, args []string, timeout time.Duration) ([]byte, bool) {
	cmd := exec.Command(name, args...)
	// Don't inherit stdin
	cmd.Stdin = nil

	done := make(chan struct{})
	var out []byte
	var err error

	go func() {
		out, err = cmd.Output()
		close(done)
	}()

	select {
	case <-done:
		if err != nil {
			return nil, false
		}
		return out, true
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return nil, false
	}
}

// readClipboardImageViaWlPaste reads an image from Wayland clipboard using wl-paste.
func readClipboardImageViaWlPaste() *ClipboardImage {
	listOut, ok := runClipboardCommand("wl-paste", []string{"--list-types"}, defaultListTimeout)
	if !ok {
		return nil
	}

	types := splitLines(string(listOut))
	selectedType := selectPreferredImageMimeType(types)
	if selectedType == "" {
		return nil
	}

	data, ok := runClipboardCommand("wl-paste", []string{"--type", selectedType, "--no-newline"}, defaultReadTimeout)
	if !ok || len(data) == 0 {
		return nil
	}

	return &ClipboardImage{Bytes: data, MimeType: baseMimeType(selectedType)}
}

// readClipboardImageViaXclip reads an image from X11 clipboard using xclip.
func readClipboardImageViaXclip() *ClipboardImage {
	targetsOut, ok := runClipboardCommand("xclip", []string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, defaultListTimeout)

	var candidateTypes []string
	if ok {
		candidateTypes = splitLines(string(targetsOut))
	}

	preferred := ""
	if len(candidateTypes) > 0 {
		preferred = selectPreferredImageMimeType(candidateTypes)
	}

	var tryTypes []string
	if preferred != "" {
		tryTypes = append([]string{preferred}, supportedImageMimeTypes...)
	} else {
		tryTypes = append([]string{}, supportedImageMimeTypes...)
	}

	for _, mimeType := range tryTypes {
		data, ok := runClipboardCommand("xclip", []string{"-selection", "clipboard", "-t", mimeType, "-o"}, defaultReadTimeout)
		if ok && len(data) > 0 {
			return &ClipboardImage{Bytes: data, MimeType: baseMimeType(mimeType)}
		}
	}
	return nil
}

// readClipboardImageViaMacOS reads an image from macOS clipboard using osascript.
func readClipboardImageViaMacOS() *ClipboardImage {
	// Use osascript to check if clipboard has an image and write it to a temp file
	script := `use framework "AppKit"
set pb to current application's NSPasteboard's generalPasteboard()
set imgTypes to {current application's NSPasteboardTypePNG, current application's NSPasteboardTypeTIFF}
set bestType to pb's availableTypeFromArray:imgTypes
if bestType is missing value then
	return "none"
end if
set imgData to pb's dataForType:bestType
if imgData is missing value then
	return "none"
end if
-- Convert to PNG via NSBitmapImageRep
set bitmapRep to current application's NSBitmapImageRep's imageRepWithData:imgData
set pngData to bitmapRep's representationUsingType:(current application's NSBitmapImageFileTypePNG) properties:(missing value)
set tmpPath to (do shell script "mktemp /tmp/pi-clipboard-XXXXXX.png")
pngData's writeToFile:tmpPath atomically:true
return tmpPath`

	out, ok := runClipboardCommand("osascript", []string{"-e", script}, defaultReadTimeout)
	if !ok {
		return nil
	}

	path := strings.TrimSpace(string(out))
	if path == "" || path == "none" {
		return nil
	}

	data, err := os.ReadFile(path)
	_ = os.Remove(path) // Clean up temp file
	if err != nil || len(data) == 0 {
		return nil
	}

	return &ClipboardImage{Bytes: data, MimeType: "image/png"}
}

// ReadClipboardImage reads an image from the system clipboard.
// Supports macOS (via osascript/AppKit) and Linux (via wl-paste or xclip).
func ReadClipboardImage() *ClipboardImage {
	// Termux doesn't support clipboard images
	if os.Getenv("TERMUX_VERSION") != "" {
		return nil
	}

	switch runtime.GOOS {
	case "darwin":
		return readClipboardImageViaMacOS()
	case "linux":
		if IsWaylandSession() {
			if img := readClipboardImageViaWlPaste(); img != nil {
				return img
			}
			return readClipboardImageViaXclip()
		}
		return readClipboardImageViaXclip()
	default:
		return nil
	}
}

func splitLines(s string) []string {
	lines := strings.Split(s, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
