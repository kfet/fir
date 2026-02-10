// Ported from: packages/coding-agent/src/modes/interactive/components/tree-selector.ts
// Upstream hash: e5f6a7b8
package components

import (
	"fmt"
	"strings"

	"github.com/kfet/pi-go/pkg/core"
	"github.com/kfet/pi-go/pkg/modes/interactive/theme"
	"github.com/kfet/pi-go/pkg/tui"
	tuicomp "github.com/kfet/pi-go/pkg/tui/components"
)

// TreeFilterMode controls tree display filtering.
type TreeFilterMode string

const (
	TreeFilterDefault     TreeFilterMode = "default"
	TreeFilterNoTools     TreeFilterMode = "no-tools"
	TreeFilterUserOnly    TreeFilterMode = "user-only"
	TreeFilterLabeledOnly TreeFilterMode = "labeled-only"
	TreeFilterAll         TreeFilterMode = "all"
)

// gutterInfo tracks connector lines in tree display.
type gutterInfo struct {
	position int
	show     bool
}

// flatNode is a flattened tree node for display.
type flatNode struct {
	node          *core.SessionTreeNode
	indent        int
	showConnector bool
	isLast        bool
	gutters       []gutterInfo
}

// treeList is the internal tree display with navigation.
type treeList struct {
	flatNodes     []flatNode
	filtered      []flatNode
	selectedIndex int
	currentLeafID string
	maxVisible    int
	filterMode    TreeFilterMode

	OnSelect func(nodeID string)
	OnCancel func()
}

func newTreeList(root []*core.SessionTreeNode, currentLeafID string) *treeList {
	tl := &treeList{
		currentLeafID: currentLeafID,
		maxVisible:    15,
		filterMode:    TreeFilterDefault,
	}
	tl.buildFlatList(root)
	tl.filtered = tl.flatNodes
	tl.selectCurrent()
	return tl
}

func (tl *treeList) buildFlatList(roots []*core.SessionTreeNode) {
	tl.flatNodes = nil
	for i, root := range roots {
		isLast := i == len(roots)-1
		tl.walkNode(root, 0, nil, isLast)
	}
}

func (tl *treeList) walkNode(node *core.SessionTreeNode, indent int, gutters []gutterInfo, isLast bool) {
	fn := flatNode{
		node:          node,
		indent:        indent,
		showConnector: indent > 0,
		isLast:        isLast,
		gutters:       append([]gutterInfo{}, gutters...),
	}
	tl.flatNodes = append(tl.flatNodes, fn)

	for i, child := range node.Children {
		childIsLast := i == len(node.Children)-1
		continues := indent > 0 && !isLast
		childGutters := append(gutters, gutterInfo{position: indent, show: continues})
		tl.walkNode(child, indent+1, childGutters, childIsLast)
	}
}

func (tl *treeList) selectCurrent() {
	if tl.currentLeafID == "" {
		return
	}
	for i, fn := range tl.filtered {
		if fn.node.Entry != nil && fn.node.Entry.ID == tl.currentLeafID {
			tl.selectedIndex = i
			return
		}
	}
}

// SelectedNodeID returns the ID of the selected node.
func (tl *treeList) SelectedNodeID() string {
	if tl.selectedIndex >= 0 && tl.selectedIndex < len(tl.filtered) {
		fn := tl.filtered[tl.selectedIndex]
		if fn.node.Entry != nil {
			return fn.node.Entry.ID
		}
	}
	return ""
}

func (tl *treeList) Invalidate() {}

func (tl *treeList) Render(width int) []string {
	t := theme.GetTheme()

	if len(tl.filtered) == 0 {
		return []string{t.Fg("muted", "  No nodes to display")}
	}

	// Calculate visible range
	start := tl.selectedIndex - tl.maxVisible/2
	if start > len(tl.filtered)-tl.maxVisible {
		start = len(tl.filtered) - tl.maxVisible
	}
	if start < 0 {
		start = 0
	}
	end := start + tl.maxVisible
	if end > len(tl.filtered) {
		end = len(tl.filtered)
	}

	var lines []string
	for i := start; i < end; i++ {
		fn := tl.filtered[i]
		isSelected := i == tl.selectedIndex
		isCurrent := fn.node.Entry != nil && fn.node.Entry.ID == tl.currentLeafID

		// Build tree prefix
		var prefix strings.Builder
		for _, g := range fn.gutters {
			_ = g.position
			if g.show {
				prefix.WriteString("│ ")
			} else {
				prefix.WriteString("  ")
			}
		}
		if fn.showConnector {
			if fn.isLast {
				prefix.WriteString("└─")
			} else {
				prefix.WriteString("├─")
			}
		}

		// Node label
		label := ""
		if fn.node.Entry != nil {
			label = fn.node.Label
			if label == "" {
				label = fn.node.Entry.ID[:8]
			}
		}

		// Cursor
		cursor := "  "
		if isSelected {
			cursor = t.Fg("accent", "▶ ")
		}

		// Current marker
		currentMark := ""
		if isCurrent {
			currentMark = t.Fg("success", " ●")
		}

		// Color
		if isSelected {
			label = t.Bold(label)
		} else if isCurrent {
			label = t.Fg("accent", label)
		}

		line := fmt.Sprintf("%s%s%s%s", cursor, prefix.String(), label, currentMark)
		lines = append(lines, tui.TruncateToWidth(line, width, "…", false))
	}

	// Scroll indicator
	if start > 0 || end < len(tl.filtered) {
		lines = append(lines, t.Fg("dim", fmt.Sprintf("  (%d/%d)", tl.selectedIndex+1, len(tl.filtered))))
	}

	return lines
}

func (tl *treeList) HandleInput(data string) {
	if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectUp) {
		if tl.selectedIndex > 0 {
			tl.selectedIndex--
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectDown) {
		if tl.selectedIndex < len(tl.filtered)-1 {
			tl.selectedIndex++
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectConfirm) {
		if tl.OnSelect != nil {
			id := tl.SelectedNodeID()
			if id != "" {
				tl.OnSelect(id)
			}
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel) {
		if tl.OnCancel != nil {
			tl.OnCancel()
		}
	}
}

// TreeSelectorComponent renders a conversation tree selector.
type TreeSelectorComponent struct {
	tui.Container
	treeList *treeList
}

// NewTreeSelectorComponent creates a new tree selector.
func NewTreeSelectorComponent(
	roots []*core.SessionTreeNode,
	currentLeafID string,
	onSelect func(nodeID string),
	onCancel func(),
) *TreeSelectorComponent {
	t := theme.GetTheme()
	c := &TreeSelectorComponent{}

	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(tuicomp.NewText(t.Bold("Conversation Tree"), 0, 0, nil))
	c.AddChild(tuicomp.NewSpacer(1))

	c.treeList = newTreeList(roots, currentLeafID)
	c.treeList.OnSelect = onSelect
	c.treeList.OnCancel = onCancel
	c.AddChild(c.treeList)

	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(NewDynamicBorder(nil))

	return c
}

// HandleInput processes keyboard input.
func (c *TreeSelectorComponent) HandleInput(data string) {
	c.treeList.HandleInput(data)
}

// SelectedNodeID returns the ID of the selected tree node.
func (c *TreeSelectorComponent) SelectedNodeID() string {
	return c.treeList.SelectedNodeID()
}

// GetTreeList returns the internal tree list for testing.
func (c *TreeSelectorComponent) GetTreeList() *treeList {
	return c.treeList
}
