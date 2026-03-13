package clipboard

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

func TestSelectPreferredImageMimeType(t *testing.T) {
	tests := []struct {
		name  string
		types []string
		want  string
	}{
		{"prefers png", []string{"text/plain", "image/png", "image/jpeg"}, "image/png"},
		{"prefers jpeg when no png", []string{"text/plain", "image/jpeg"}, "image/jpeg"},
		{"falls back to any image", []string{"text/plain", "image/bmp"}, "image/bmp"},
		{"empty list", []string{}, ""},
		{"no image types", []string{"text/plain", "application/json"}, ""},
		{"handles whitespace", []string{"  image/png  ", "image/jpeg"}, "image/png"},
		{"skips empty strings", []string{"", "image/png", ""}, "image/png"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectPreferredImageMimeType(tt.types)
			if got != tt.want {
				t.Errorf("selectPreferredImageMimeType() = %q, want %q", got, tt.want)
			}
		})
	}
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

func TestIsSupportedImageMimeType(t *testing.T) {
	tests := []struct {
		mimeType string
		want     bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/webp", true},
		{"image/gif", true},
		{"image/bmp", false},
		{"text/plain", false},
		{"IMAGE/PNG", true},
		{"image/png; charset=utf-8", true},
	}

	for _, tt := range tests {
		got := isSupportedImageMimeType(tt.mimeType)
		if got != tt.want {
			t.Errorf("isSupportedImageMimeType(%q) = %v, want %v", tt.mimeType, got, tt.want)
		}
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"a\nb\nc", 3},
		{"a\n\nb", 2},
		{"  a  \n  b  ", 2},
		{"", 0},
		{"\n\n\n", 0},
	}

	for _, tt := range tests {
		got := splitLines(tt.input)
		if len(got) != tt.want {
			t.Errorf("splitLines(%q) got %d lines, want %d", tt.input, len(got), tt.want)
		}
	}
}

func TestIsWaylandSessionNoPanic(t *testing.T) {
	// Just verify the function doesn't panic - actual result depends on environment
	_ = IsWaylandSession()
}
