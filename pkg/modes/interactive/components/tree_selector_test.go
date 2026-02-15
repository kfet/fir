package components

import (
	"strings"
	"testing"

	"github.com/kfet/tau/pkg/core"
)

func testSessionTree() []*core.SessionTreeNode {
	root := &core.SessionTreeNode{
		Entry: &core.SessionEntry{ID: "root-001"},
		Label: "Initial prompt",
		Children: []*core.SessionTreeNode{
			{
				Entry: &core.SessionEntry{ID: "child-001"},
				Label: "Response A",
			},
			{
				Entry: &core.SessionEntry{ID: "child-002"},
				Label: "Response B",
				Children: []*core.SessionTreeNode{
					{
						Entry: &core.SessionEntry{ID: "grandchild-001"},
						Label: "Follow-up",
					},
				},
			},
		},
	}
	return []*core.SessionTreeNode{root}
}

func TestTreeList_BuildFlatList(t *testing.T) {
	roots := testSessionTree()
	tl := newTreeList(roots, "")

	// root + 2 children + 1 grandchild = 4
	if len(tl.flatNodes) != 4 {
		t.Fatalf("expected 4 flat nodes, got %d", len(tl.flatNodes))
	}

	// Root should have indent 0
	if tl.flatNodes[0].indent != 0 {
		t.Errorf("expected root indent 0, got %d", tl.flatNodes[0].indent)
	}

	// Children should have indent 1
	if tl.flatNodes[1].indent != 1 {
		t.Errorf("expected child indent 1, got %d", tl.flatNodes[1].indent)
	}

	// Grandchild should have indent 2
	if tl.flatNodes[3].indent != 2 {
		t.Errorf("expected grandchild indent 2, got %d", tl.flatNodes[3].indent)
	}
}

func TestTreeList_SelectCurrent(t *testing.T) {
	roots := testSessionTree()
	tl := newTreeList(roots, "child-002")

	if tl.SelectedNodeID() != "child-002" {
		t.Errorf("expected selected 'child-002', got %q", tl.SelectedNodeID())
	}
}

func TestTreeSelectorComponent_Render(t *testing.T) {
	roots := testSessionTree()
	comp := NewTreeSelectorComponent(roots, "root-001", func(string) {}, func() {})

	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected render output")
	}

	found := false
	for _, line := range lines {
		if strings.Contains(line, "Conversation Tree") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Conversation Tree' in output")
	}
}

func TestTreeSelectorComponent_Navigation(t *testing.T) {
	roots := testSessionTree()
	comp := NewTreeSelectorComponent(roots, "root-001", func(string) {}, func() {})

	// Should start at root-001
	if comp.SelectedNodeID() != "root-001" {
		t.Errorf("expected root-001, got %q", comp.SelectedNodeID())
	}

	// Move down
	comp.HandleInput("\x1b[B") // down arrow
	if comp.SelectedNodeID() != "child-001" {
		t.Errorf("expected child-001 after down, got %q", comp.SelectedNodeID())
	}
}

func TestTreeList_EmptyTree(t *testing.T) {
	tl := newTreeList(nil, "")
	if len(tl.flatNodes) != 0 {
		t.Errorf("expected 0 nodes for empty tree, got %d", len(tl.flatNodes))
	}
	lines := tl.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected at least empty message")
	}
}

func TestTreeList_Callbacks(t *testing.T) {
	roots := testSessionTree()
	tl := newTreeList(roots, "root-001")

	selected := ""
	tl.OnSelect = func(id string) { selected = id }

	// Confirm selection
	tl.HandleInput("\r") // enter
	if selected != "root-001" {
		t.Errorf("expected root-001 selected, got %q", selected)
	}

	cancelled := false
	tl.OnCancel = func() { cancelled = true }
	tl.HandleInput("\x1b") // escape
	if !cancelled {
		t.Error("expected cancel callback")
	}
}
