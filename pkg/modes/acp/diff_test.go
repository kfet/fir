package acp

import (
	"testing"
)

func TestParseDiffForAcp(t *testing.T) {
	diff := " 1 context\n-2 old line\n+2 new line\n 3 context"
	content, locations := ParseDiffForAcp(diff, "test.go", 2)
	if len(content) != 1 || len(locations) != 1 {
		t.Fatalf("expected 1 content + 1 location, got %d + %d", len(content), len(locations))
	}
	if locations[0].Path != "test.go" || *locations[0].Line != 2 {
		t.Errorf("location = %v", locations[0])
	}

	c, l := ParseDiffForAcp("", "", 0)
	if len(c) != 0 || len(l) != 0 {
		t.Error("empty input should return empty")
	}
}

func TestIsPathWithinDirectory(t *testing.T) {
	if !IsPathWithinDirectory("/home/user/project/file.go", "/home/user/project") {
		t.Error("file within directory should return true")
	}
	if IsPathWithinDirectory("/etc/passwd", "/home/user/project") {
		t.Error("file outside directory should return false")
	}
	if IsPathWithinDirectory("/home/user/project/../etc/passwd", "/home/user/project") {
		t.Error("path traversal should return false")
	}
}
