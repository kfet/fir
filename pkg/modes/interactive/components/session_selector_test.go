package components

import (
	"strings"
	"testing"
	"time"

	"github.com/kfet/pi-go/pkg/core"
)

func testSessions() []core.SessionListInfo {
	now := time.Now()
	return []core.SessionListInfo{
		{
			Path: "/sessions/s1.jsonl", ID: "s1", Cwd: "/home/user/project",
			Name: "Main session", Modified: now.Add(-1 * time.Hour), MessageCount: 5,
			FirstMessage: "Hello world",
		},
		{
			Path: "/sessions/s2.jsonl", ID: "s2", Cwd: "/home/user/project",
			Name: "Feature branch", Modified: now.Add(-30 * time.Minute), MessageCount: 3,
			ParentSessionPath: "/sessions/s1.jsonl",
			FirstMessage:      "Working on feature",
		},
		{
			Path: "/sessions/s3.jsonl", ID: "s3", Cwd: "/home/user/other",
			Name: "", Modified: now.Add(-2 * time.Hour), MessageCount: 1,
			FirstMessage: "Quick test",
		},
	}
}

func TestBuildSessionTree(t *testing.T) {
	sessions := testSessions()
	tree := buildSessionTree(sessions)

	// Should have 2 roots (s1 and s3; s2 is child of s1)
	if len(tree) != 2 {
		t.Fatalf("expected 2 roots, got %d", len(tree))
	}

	// Find s1 root
	var s1Root *sessionTreeNode
	for _, r := range tree {
		if r.session.ID == "s1" {
			s1Root = r
			break
		}
	}
	if s1Root == nil {
		t.Fatal("expected to find s1 root")
	}
	if len(s1Root.children) != 1 {
		t.Fatalf("expected 1 child for s1, got %d", len(s1Root.children))
	}
	if s1Root.children[0].session.ID != "s2" {
		t.Errorf("expected child s2, got %s", s1Root.children[0].session.ID)
	}
}

func TestFlattenSessionTree(t *testing.T) {
	sessions := testSessions()
	tree := buildSessionTree(sessions)
	flat := flattenSessionTree(tree)

	if len(flat) != 3 {
		t.Fatalf("expected 3 flat nodes, got %d", len(flat))
	}

	// Check depths
	for _, f := range flat {
		if f.session.ID == "s2" && f.depth != 1 {
			t.Errorf("expected depth 1 for s2, got %d", f.depth)
		}
	}
}

func TestFormatSessionDate(t *testing.T) {
	tests := []struct {
		dur    time.Duration
		expect string
	}{
		{0, "now"},
		{5 * time.Minute, "5m"},
		{2 * time.Hour, "2h"},
		{3 * 24 * time.Hour, "3d"},
		{14 * 24 * time.Hour, "2w"},
	}
	for _, tt := range tests {
		t.Run(tt.expect, func(t *testing.T) {
			got := formatSessionDate(time.Now().Add(-tt.dur))
			if got != tt.expect {
				t.Errorf("expected %q, got %q", tt.expect, got)
			}
		})
	}
}

func TestShortenPath_Session(t *testing.T) {
	// Empty path
	if shortenPath("") != "" {
		t.Error("expected empty for empty")
	}
	// Non-home path stays the same
	if shortenPath("/usr/local/bin") != "/usr/local/bin" {
		t.Error("expected /usr/local/bin unchanged")
	}
}

func TestSessionSelectorComponent_Render(t *testing.T) {
	comp := NewSessionSelectorComponent(
		testSessions(), SessionScopeCurrent,
		func(path string) {}, func() {},
	)
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected render output")
	}
	found := false
	for _, line := range lines {
		if strings.Contains(line, "Resume Session") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Resume Session' in output")
	}
}

func TestSessionList_Filter(t *testing.T) {
	sl := newSessionList()
	sl.SetSessions(testSessions())

	// All sessions present
	if len(sl.filteredSessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sl.filteredSessions))
	}

	// Filter by name
	sl.applyFilter("Feature")
	count := 0
	for _, f := range sl.filteredSessions {
		if f.session.ID == "s2" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 matching session, got %d", count)
	}
}

func TestSessionSelectorComponent_SelectPath(t *testing.T) {
	comp := NewSessionSelectorComponent(
		testSessions(), SessionScopeAll,
		func(path string) {}, func() {},
	)
	path := comp.SelectedPath()
	if path == "" {
		t.Error("expected a selected path")
	}
}
