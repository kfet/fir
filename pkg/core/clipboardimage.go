// Ported from: packages/coding-agent/src/utils/clipboard-image.ts
// Upstream hash: 1caadb2e
package core

import "strings"

// ClipboardImage represents image data from the clipboard.
type ClipboardImage struct {
	Bytes    []byte
	MimeType string
}

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

// ReadClipboardImage reads an image from the system clipboard.
// TODO: Implement native clipboard reading for each platform.
func ReadClipboardImage() *ClipboardImage {
	// Not yet implemented — requires platform-specific clipboard access.
	// On macOS: use pasteboard APIs
	// On Linux: use wl-paste or xclip
	// On Windows: use PowerShell or Win32 APIs
	return nil
}
