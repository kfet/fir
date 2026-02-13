package core

import (
	"strings"
	"testing"
)

func TestHyperlink(t *testing.T) {
	link := Hyperlink("https://example.com", "click here")
	if !strings.Contains(link, "\x1b]8;;https://example.com\x07") {
		t.Errorf("missing OSC 8 open: %q", link)
	}
	if !strings.Contains(link, "click here") {
		t.Errorf("missing visible text: %q", link)
	}
	if !strings.HasSuffix(link, "\x1b]8;;\x07") {
		t.Errorf("missing OSC 8 close: %q", link)
	}
}
