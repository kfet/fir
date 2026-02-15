// Ported from: packages/coding-agent/src/modes/interactive/components/tree-selector.ts
// Upstream hash: 1caadb2e
package components

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/kfet/tau/pkg/core"
	"github.com/kfet/tau/pkg/modes/interactive/theme"
	"github.com/kfet/tau/pkg/tui"
	tuicomp "github.com/kfet/tau/pkg/tui/components"
)

// --- Types ---

// gutterInfo tracks vertical connector lines for tree indentation.
type gutterInfo struct {
	position int  // displayIndent level where connector was shown
	show     bool // true = show │, false = show spaces
}

// flatNode is a flattened tree node for display/navigation.
type flatNode struct {
	node           *core.SessionTreeNode
	indent         int  // indentation level (each level = 3 chars)
	showConnector  bool // whether to show ├─ or └─
	isLast         bool // true = last sibling (└─)
	gutters        []gutterInfo
	isVirtualChild bool // true if this is a root under a virtual branching root
}

// filterMode controls which entries are visible.
type filterMode int

const (
	filterDefault filterMode = iota
	filterNoTools
	filterUserOnly
	filterLabeledOnly
	filterAll
)

var filterModes = []filterMode{filterDefault, filterNoTools, filterUserOnly, filterLabeledOnly, filterAll}

// toolCallInfo caches tool call details for display.
type toolCallInfo struct {
	name      string
	arguments map[string]any
}

// --- TreeList ---

// TreeList is the navigable tree list component (internal to TreeSelectorComponent).
type TreeList struct {
	flatNodes       []flatNode
	filteredNodes   []flatNode
	selectedIndex   int
	currentLeafID   string
	maxVisibleLines int
	filter          filterMode
	searchQuery     string
	toolCallMap     map[string]toolCallInfo
	multipleRoots   bool
	activePathIDs   map[string]bool
	lastSelectedID  string

	OnSelect    func(entryID string)
	OnCancel    func()
	OnLabelEdit func(entryID string, currentLabel string)
}

// newTreeList creates a TreeList from the session tree.
func newTreeList(tree []*core.SessionTreeNode, currentLeafID string) *TreeList {
	tl := &TreeList{
		currentLeafID:   currentLeafID,
		maxVisibleLines: 20,
		toolCallMap:     make(map[string]toolCallInfo),
		activePathIDs:   make(map[string]bool),
	}
	tl.multipleRoots = len(tree) > 1
	tl.flatNodes = tl.flattenTree(tree)
	tl.buildActivePath()
	tl.applyFilter()
	tl.selectedIndex = tl.findNearestVisibleIndex(currentLeafID)
	if tl.selectedIndex < len(tl.filteredNodes) {
		tl.lastSelectedID = tl.filteredNodes[tl.selectedIndex].node.Entry.ID
	}
	return tl
}

// SelectedNodeID returns the ID of the currently selected entry.
func (tl *TreeList) SelectedNodeID() string {
	if tl.selectedIndex < len(tl.filteredNodes) {
		return tl.filteredNodes[tl.selectedIndex].node.Entry.ID
	}
	return ""
}

// Invalidate is a no-op (state is always current).
func (tl *TreeList) Invalidate() {}

// --- Active path ---

func (tl *TreeList) buildActivePath() {
	tl.activePathIDs = make(map[string]bool)
	if tl.currentLeafID == "" {
		return
	}
	entryMap := make(map[string]*flatNode)
	for i := range tl.flatNodes {
		entryMap[tl.flatNodes[i].node.Entry.ID] = &tl.flatNodes[i]
	}
	currentID := tl.currentLeafID
	for currentID != "" {
		tl.activePathIDs[currentID] = true
		fn, ok := entryMap[currentID]
		if !ok {
			break
		}
		currentID = fn.node.Entry.ParentID
	}
}

// --- findNearestVisibleIndex ---

func (tl *TreeList) findNearestVisibleIndex(entryID string) int {
	if len(tl.filteredNodes) == 0 {
		return 0
	}
	entryMap := make(map[string]*flatNode)
	for i := range tl.flatNodes {
		entryMap[tl.flatNodes[i].node.Entry.ID] = &tl.flatNodes[i]
	}
	visibleIDToIndex := make(map[string]int)
	for i, fn := range tl.filteredNodes {
		visibleIDToIndex[fn.node.Entry.ID] = i
	}
	currentID := entryID
	for currentID != "" {
		if idx, ok := visibleIDToIndex[currentID]; ok {
			return idx
		}
		fn, ok := entryMap[currentID]
		if !ok {
			break
		}
		currentID = fn.node.Entry.ParentID
	}
	return len(tl.filteredNodes) - 1
}

// --- Flatten ---

type flattenStackItem struct {
	node           *core.SessionTreeNode
	indent         int
	justBranched   bool
	showConnector  bool
	isLast         bool
	gutters        []gutterInfo
	isVirtualChild bool
}

func (tl *TreeList) flattenTree(roots []*core.SessionTreeNode) []flatNode {
	var result []flatNode
	tl.toolCallMap = make(map[string]toolCallInfo)

	if len(roots) == 0 {
		return result
	}

	// Build containsActive map (iterative post-order)
	containsActive := make(map[*core.SessionTreeNode]bool)
	{
		var allNodes []*core.SessionTreeNode
		stack := make([]*core.SessionTreeNode, len(roots))
		copy(stack, roots)
		for len(stack) > 0 {
			node := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			allNodes = append(allNodes, node)
			for i := len(node.Children) - 1; i >= 0; i-- {
				stack = append(stack, node.Children[i])
			}
		}
		for i := len(allNodes) - 1; i >= 0; i-- {
			node := allNodes[i]
			has := tl.currentLeafID != "" && node.Entry.ID == tl.currentLeafID
			for _, child := range node.Children {
				if containsActive[child] {
					has = true
				}
			}
			containsActive[node] = has
		}
	}

	multipleRoots := len(roots) > 1

	// Sort roots: active branch first
	orderedRoots := make([]*core.SessionTreeNode, len(roots))
	copy(orderedRoots, roots)
	sortByActive(orderedRoots, containsActive)

	// Push roots onto stack in reverse order
	var stack []flattenStackItem
	for i := len(orderedRoots) - 1; i >= 0; i-- {
		isLast := i == len(orderedRoots)-1
		indent := 0
		if multipleRoots {
			indent = 1
		}
		stack = append(stack, flattenStackItem{
			node: orderedRoots[i], indent: indent,
			justBranched: multipleRoots, showConnector: multipleRoots,
			isLast: isLast, isVirtualChild: multipleRoots,
		})
	}

	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		// Extract tool calls from assistant messages
		tl.extractToolCalls(item.node.Entry)

		result = append(result, flatNode{
			node:           item.node,
			indent:         item.indent,
			showConnector:  item.showConnector,
			isLast:         item.isLast,
			gutters:        item.gutters,
			isVirtualChild: item.isVirtualChild,
		})

		children := item.node.Children
		multipleChildren := len(children) > 1

		// Order children: active branch first
		orderedChildren := make([]*core.SessionTreeNode, len(children))
		copy(orderedChildren, children)
		sortByActive(orderedChildren, containsActive)

		// Calculate child indent
		var childIndent int
		if multipleChildren {
			childIndent = item.indent + 1
		} else if item.justBranched && item.indent > 0 {
			childIndent = item.indent + 1
		} else {
			childIndent = item.indent
		}

		// Build child gutters
		connectorDisplayed := item.showConnector && !item.isVirtualChild
		currentDisplayIndent := item.indent
		if tl.multipleRoots {
			currentDisplayIndent = max(0, item.indent-1)
		}
		connectorPosition := max(0, currentDisplayIndent-1)
		var childGutters []gutterInfo
		if connectorDisplayed {
			childGutters = append(append([]gutterInfo{}, item.gutters...), gutterInfo{
				position: connectorPosition,
				show:     !item.isLast,
			})
		} else {
			childGutters = item.gutters
		}

		// Push children in reverse
		for i := len(orderedChildren) - 1; i >= 0; i-- {
			childIsLast := i == len(orderedChildren)-1
			stack = append(stack, flattenStackItem{
				node: orderedChildren[i], indent: childIndent,
				justBranched: multipleChildren, showConnector: multipleChildren,
				isLast: childIsLast, gutters: childGutters,
			})
		}
	}

	return result
}

func sortByActive(nodes []*core.SessionTreeNode, containsActive map[*core.SessionTreeNode]bool) {
	// Stable sort: active first, then original order
	prioritized := make([]*core.SessionTreeNode, 0, len(nodes))
	rest := make([]*core.SessionTreeNode, 0, len(nodes))
	for _, n := range nodes {
		if containsActive[n] {
			prioritized = append(prioritized, n)
		} else {
			rest = append(rest, n)
		}
	}
	copy(nodes, append(prioritized, rest...))
}

func (tl *TreeList) extractToolCalls(entry *core.SessionEntry) {
	if entry.Type != "message" || len(entry.RawMessage) == 0 {
		return
	}
	var msg struct {
		Role    string            `json:"role"`
		Content json.RawMessage   `json:"content"`
	}
	if err := json.Unmarshal(entry.RawMessage, &msg); err != nil || msg.Role != "assistant" {
		return
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return
	}
	for _, block := range blocks {
		var tc struct {
			Type      string         `json:"type"`
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(block, &tc); err == nil && tc.Type == "toolCall" {
			tl.toolCallMap[tc.ID] = toolCallInfo{name: tc.Name, arguments: tc.Arguments}
		}
	}
}

// --- Filter ---

func (tl *TreeList) applyFilter() {
	if len(tl.filteredNodes) > 0 && tl.selectedIndex < len(tl.filteredNodes) {
		tl.lastSelectedID = tl.filteredNodes[tl.selectedIndex].node.Entry.ID
	}

	searchTokens := splitSearch(tl.searchQuery)
	var filtered []flatNode

	for i := range tl.flatNodes {
		fn := &tl.flatNodes[i]
		entry := fn.node.Entry
		isCurrentLeaf := entry.ID == tl.currentLeafID

		// Skip assistant messages with only tool calls (no text) unless error/aborted
		if entry.Type == "message" && !isCurrentLeaf {
			if role, hasText, stopReason, _ := tl.parseMessageInfo(entry); role == "assistant" {
				isErrorOrAborted := stopReason != "" && stopReason != "stop" && stopReason != "toolUse"
				if !hasText && !isErrorOrAborted {
					continue
				}
			}
		}

		// Apply filter mode
		passesFilter := true
		isSettingsEntry := entry.Type == "label" || entry.Type == "custom" ||
			entry.Type == "model_change" || entry.Type == "thinking_level_change"

		switch tl.filter {
		case filterUserOnly:
			if entry.Type == "message" {
				role, _, _, _ := tl.parseMessageInfo(entry)
				passesFilter = role == "user"
			} else {
				passesFilter = false
			}
		case filterNoTools:
			if entry.Type == "message" {
				role, _, _, _ := tl.parseMessageInfo(entry)
				passesFilter = !isSettingsEntry && role != "toolResult"
			} else {
				passesFilter = !isSettingsEntry
			}
		case filterLabeledOnly:
			passesFilter = fn.node.Label != ""
		case filterAll:
			passesFilter = true
		default: // filterDefault
			passesFilter = !isSettingsEntry
		}

		if !passesFilter {
			continue
		}

		// Apply search
		if len(searchTokens) > 0 {
			nodeText := strings.ToLower(tl.getSearchableText(fn.node))
			allMatch := true
			for _, tok := range searchTokens {
				if !strings.Contains(nodeText, tok) {
					allMatch = false
					break
				}
			}
			if !allMatch {
				continue
			}
		}

		filtered = append(filtered, *fn)
	}

	tl.filteredNodes = filtered
	tl.recalculateVisualStructure()

	if tl.lastSelectedID != "" {
		tl.selectedIndex = tl.findNearestVisibleIndex(tl.lastSelectedID)
	} else if tl.selectedIndex >= len(tl.filteredNodes) {
		tl.selectedIndex = max(0, len(tl.filteredNodes)-1)
	}

	if len(tl.filteredNodes) > 0 && tl.selectedIndex < len(tl.filteredNodes) {
		tl.lastSelectedID = tl.filteredNodes[tl.selectedIndex].node.Entry.ID
	}
}

func splitSearch(query string) []string {
	var tokens []string
	for _, tok := range strings.Fields(strings.ToLower(query)) {
		if tok != "" {
			tokens = append(tokens, tok)
		}
	}
	return tokens
}

// parseMessageInfo extracts role, hasText, stopReason, errorMessage from a raw message entry.
func (tl *TreeList) parseMessageInfo(entry *core.SessionEntry) (role string, hasText bool, stopReason string, errorMessage string) {
	if len(entry.RawMessage) == 0 {
		return "", false, "", ""
	}
	var msg struct {
		Role         string          `json:"role"`
		StopReason   string          `json:"stopReason"`
		ErrorMessage string          `json:"errorMessage"`
		Content      json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(entry.RawMessage, &msg); err != nil {
		return "", false, "", ""
	}
	hasText = hasTextContent(msg.Content)
	return msg.Role, hasText, msg.StopReason, msg.ErrorMessage
}

func hasTextContent(raw json.RawMessage) bool {
	// Try as string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s) != ""
	}
	// Try as array of content blocks
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return false
	}
	for _, block := range blocks {
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(block, &b); err == nil && b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

// --- Recalculate visual structure for filtered view ---

func (tl *TreeList) recalculateVisualStructure() {
	if len(tl.filteredNodes) == 0 {
		return
	}

	visibleIDs := make(map[string]bool)
	for _, fn := range tl.filteredNodes {
		visibleIDs[fn.node.Entry.ID] = true
	}

	// Build entry map from full tree
	entryMap := make(map[string]*flatNode)
	for i := range tl.flatNodes {
		entryMap[tl.flatNodes[i].node.Entry.ID] = &tl.flatNodes[i]
	}

	findVisibleAncestor := func(nodeID string) string {
		currentID := ""
		if fn, ok := entryMap[nodeID]; ok {
			currentID = fn.node.Entry.ParentID
		}
		for currentID != "" {
			if visibleIDs[currentID] {
				return currentID
			}
			fn, ok := entryMap[currentID]
			if !ok {
				break
			}
			currentID = fn.node.Entry.ParentID
		}
		return ""
	}

	// Build visible tree structure
	visibleParent := make(map[string]string)        // nodeID → ancestor or ""
	visibleChildren := make(map[string][]string)     // parentID → children
	visibleChildren[""] = nil                         // root level

	for _, fn := range tl.filteredNodes {
		nodeID := fn.node.Entry.ID
		ancestorID := findVisibleAncestor(nodeID)
		visibleParent[nodeID] = ancestorID
		visibleChildren[ancestorID] = append(visibleChildren[ancestorID], nodeID)
	}

	// Update multipleRoots
	visibleRootIDs := visibleChildren[""]
	tl.multipleRoots = len(visibleRootIDs) > 1

	// Build filtered node map
	filteredNodeMap := make(map[string]*flatNode)
	for i := range tl.filteredNodes {
		filteredNodeMap[tl.filteredNodes[i].node.Entry.ID] = &tl.filteredNodes[i]
	}

	// DFS over visible tree
	type recalcItem struct {
		nodeID         string
		indent         int
		justBranched   bool
		showConnector  bool
		isLast         bool
		gutters        []gutterInfo
		isVirtualChild bool
	}

	var stack []recalcItem
	for i := len(visibleRootIDs) - 1; i >= 0; i-- {
		isLast := i == len(visibleRootIDs)-1
		indent := 0
		if tl.multipleRoots {
			indent = 1
		}
		stack = append(stack, recalcItem{
			nodeID: visibleRootIDs[i], indent: indent,
			justBranched: tl.multipleRoots, showConnector: tl.multipleRoots,
			isLast: isLast, isVirtualChild: tl.multipleRoots,
		})
	}

	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		fn := filteredNodeMap[item.nodeID]
		if fn == nil {
			continue
		}

		fn.indent = item.indent
		fn.showConnector = item.showConnector
		fn.isLast = item.isLast
		fn.gutters = item.gutters
		fn.isVirtualChild = item.isVirtualChild

		children := visibleChildren[item.nodeID]
		multipleChildren := len(children) > 1

		var childIndent int
		if multipleChildren {
			childIndent = item.indent + 1
		} else if item.justBranched && item.indent > 0 {
			childIndent = item.indent + 1
		} else {
			childIndent = item.indent
		}

		connectorDisplayed := item.showConnector && !item.isVirtualChild
		currentDisplayIndent := item.indent
		if tl.multipleRoots {
			currentDisplayIndent = max(0, item.indent-1)
		}
		connectorPosition := max(0, currentDisplayIndent-1)
		var childGutters []gutterInfo
		if connectorDisplayed {
			childGutters = append(append([]gutterInfo{}, item.gutters...), gutterInfo{
				position: connectorPosition,
				show:     !item.isLast,
			})
		} else {
			childGutters = item.gutters
		}

		for i := len(children) - 1; i >= 0; i-- {
			childIsLast := i == len(children)-1
			stack = append(stack, recalcItem{
				nodeID: children[i], indent: childIndent,
				justBranched: multipleChildren, showConnector: multipleChildren,
				isLast: childIsLast, gutters: childGutters,
			})
		}
	}
}

// --- Searchable text ---

func (tl *TreeList) getSearchableText(node *core.SessionTreeNode) string {
	entry := node.Entry
	var parts []string
	if node.Label != "" {
		parts = append(parts, node.Label)
	}

	switch entry.Type {
	case "message":
		role, _, _, _ := tl.parseMessageInfo(entry)
		parts = append(parts, role)
		parts = append(parts, extractContentText(entry.RawMessage, 200))
	case "custom_message":
		parts = append(parts, entry.CustomType)
		parts = append(parts, extractRawContent(entry.Content, 200))
	case "compaction":
		parts = append(parts, "compaction")
	case "branch_summary":
		parts = append(parts, "branch summary", entry.Summary)
	case "model_change":
		parts = append(parts, "model", entry.ModelID)
	case "thinking_level_change":
		parts = append(parts, "thinking", entry.ThinkingLevel)
	case "custom":
		parts = append(parts, "custom", entry.CustomType)
	case "label":
		parts = append(parts, "label", entry.Label)
	}

	return strings.Join(parts, " ")
}

func extractContentText(rawMsg json.RawMessage, maxLen int) string {
	if len(rawMsg) == 0 {
		return ""
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(rawMsg, &msg); err != nil {
		return ""
	}
	return extractRawContent(msg.Content, maxLen)
}

func extractRawContent(raw json.RawMessage, maxLen int) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if len(s) > maxLen {
			return s[:maxLen]
		}
		return s
	}
	// Try array of content blocks
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var result strings.Builder
	for _, block := range blocks {
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(block, &b); err == nil && b.Type == "text" {
			result.WriteString(b.Text)
			if result.Len() >= maxLen {
				return result.String()[:maxLen]
			}
		}
	}
	return result.String()
}

// --- Render ---

func (tl *TreeList) getFilterLabel() string {
	switch tl.filter {
	case filterNoTools:
		return " [no-tools]"
	case filterUserOnly:
		return " [user]"
	case filterLabeledOnly:
		return " [labeled]"
	case filterAll:
		return " [all]"
	default:
		return ""
	}
}

func (tl *TreeList) Render(width int) []string {
	t := theme.GetTheme()
	var lines []string

	if len(tl.filteredNodes) == 0 {
		lines = append(lines, tui.TruncateToWidth(t.Fg("muted", "  No entries found"), width, "", false))
		lines = append(lines, tui.TruncateToWidth(
			t.Fg("muted", fmt.Sprintf("  (0/0)%s", tl.getFilterLabel())), width, "", false))
		return lines
	}

	startIndex := int(math.Max(0, math.Min(
		float64(tl.selectedIndex-tl.maxVisibleLines/2),
		float64(len(tl.filteredNodes)-tl.maxVisibleLines),
	)))
	endIndex := int(math.Min(float64(startIndex+tl.maxVisibleLines), float64(len(tl.filteredNodes))))

	for i := startIndex; i < endIndex; i++ {
		fn := &tl.filteredNodes[i]
		entry := fn.node.Entry
		isSelected := i == tl.selectedIndex

		cursor := "  "
		if isSelected {
			cursor = t.Fg("accent", "› ")
		}

		displayIndent := fn.indent
		if tl.multipleRoots {
			displayIndent = max(0, fn.indent-1)
		}

		// Build prefix with gutters
		connector := ""
		if fn.showConnector && !fn.isVirtualChild {
			if fn.isLast {
				connector = "└─ "
			} else {
				connector = "├─ "
			}
		}
		connectorPosition := -1
		if connector != "" {
			connectorPosition = displayIndent - 1
		}

		totalChars := displayIndent * 3
		prefixChars := make([]byte, totalChars)
		for c := range prefixChars {
			prefixChars[c] = ' '
		}
		for c := 0; c < totalChars; c++ {
			level := c / 3
			posInLevel := c % 3

			// Check gutters
			foundGutter := false
			for _, g := range fn.gutters {
				if g.position == level {
					foundGutter = true
					if posInLevel == 0 {
						if g.show {
							// Write "│" as UTF-8 bytes
							utf8 := "│"
							// Replace single byte with multi-byte char — we'll build with strings instead
							_ = utf8
						}
					}
					break
				}
			}
			_ = foundGutter
		}

		// Build prefix using strings for proper UTF-8 handling
		var prefix strings.Builder
		for c := 0; c < totalChars; c++ {
			level := c / 3
			posInLevel := c % 3

			foundGutter := false
			for _, g := range fn.gutters {
				if g.position == level {
					foundGutter = true
					if posInLevel == 0 {
						if g.show {
							prefix.WriteString("│")
						} else {
							prefix.WriteByte(' ')
						}
					} else {
						prefix.WriteByte(' ')
					}
					break
				}
			}
			if foundGutter {
				continue
			}

			if connector != "" && level == connectorPosition {
				if posInLevel == 0 {
					if fn.isLast {
						prefix.WriteString("└")
					} else {
						prefix.WriteString("├")
					}
				} else if posInLevel == 1 {
					prefix.WriteString("─")
				} else {
					prefix.WriteByte(' ')
				}
			} else {
				prefix.WriteByte(' ')
			}
		}

		// Active path marker
		pathMarker := ""
		if tl.activePathIDs[entry.ID] {
			pathMarker = t.Fg("accent", "• ")
		}

		label := ""
		if fn.node.Label != "" {
			label = t.Fg("warning", fmt.Sprintf("[%s] ", fn.node.Label))
		}

		content := tl.getEntryDisplayText(fn.node, isSelected)
		line := cursor + t.Fg("dim", prefix.String()) + pathMarker + label + content
		if isSelected {
			line = t.Bg("selectedBg", line)
		}
		lines = append(lines, tui.TruncateToWidth(line, width, "", false))
	}

	lines = append(lines, tui.TruncateToWidth(
		t.Fg("muted", fmt.Sprintf("  (%d/%d)%s", tl.selectedIndex+1, len(tl.filteredNodes), tl.getFilterLabel())),
		width, "", false))

	return lines
}

// --- Entry display text ---

func (tl *TreeList) getEntryDisplayText(node *core.SessionTreeNode, isSelected bool) string {
	t := theme.GetTheme()
	entry := node.Entry
	normalize := func(s string) string {
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.ReplaceAll(s, "\t", " ")
		return strings.TrimSpace(s)
	}

	var result string

	switch entry.Type {
	case "message":
		role, _, stopReason, errorMessage := tl.parseMessageInfo(entry)
		contentText := normalize(extractContentText(entry.RawMessage, 200))

		switch role {
		case "user":
			result = t.Fg("accent", "user: ") + contentText
		case "assistant":
			if contentText != "" {
				result = t.Fg("success", "assistant: ") + contentText
			} else if stopReason == "aborted" {
				result = t.Fg("success", "assistant: ") + t.Fg("muted", "(aborted)")
			} else if errorMessage != "" {
				errMsg := normalize(errorMessage)
				if len(errMsg) > 80 {
					errMsg = errMsg[:80]
				}
				result = t.Fg("success", "assistant: ") + t.Fg("error", errMsg)
			} else {
				result = t.Fg("success", "assistant: ") + t.Fg("muted", "(no content)")
			}
		case "toolResult":
			toolCallID, toolName := tl.parseToolResult(entry)
			if tc, ok := tl.toolCallMap[toolCallID]; ok {
				result = t.Fg("muted", tl.formatToolCall(tc.name, tc.arguments))
			} else {
				if toolName == "" {
					toolName = "tool"
				}
				result = t.Fg("muted", fmt.Sprintf("[%s]", toolName))
			}
		case "bashExecution":
			cmd := tl.parseBashCommand(entry)
			result = t.Fg("dim", fmt.Sprintf("[bash]: %s", normalize(cmd)))
		default:
			result = t.Fg("dim", fmt.Sprintf("[%s]", role))
		}

	case "custom_message":
		content := normalize(extractRawContent(entry.Content, 200))
		result = t.Fg("customMessageLabel", fmt.Sprintf("[%s]: ", entry.CustomType)) + content

	case "compaction":
		tokens := int(math.Round(float64(entry.TokensBefore) / 1000))
		result = t.Fg("borderAccent", fmt.Sprintf("[compaction: %dk tokens]", tokens))

	case "branch_summary":
		result = t.Fg("warning", "[branch summary]: ") + normalize(entry.Summary)

	case "model_change":
		result = t.Fg("dim", fmt.Sprintf("[model: %s]", entry.ModelID))

	case "thinking_level_change":
		result = t.Fg("dim", fmt.Sprintf("[thinking: %s]", entry.ThinkingLevel))

	case "custom":
		result = t.Fg("dim", fmt.Sprintf("[custom: %s]", entry.CustomType))

	case "label":
		lbl := entry.Label
		if lbl == "" {
			lbl = "(cleared)"
		}
		result = t.Fg("dim", fmt.Sprintf("[label: %s]", lbl))
	}

	if isSelected {
		return t.Bold(result)
	}
	return result
}

func (tl *TreeList) parseToolResult(entry *core.SessionEntry) (toolCallID, toolName string) {
	if len(entry.RawMessage) == 0 {
		return "", ""
	}
	var msg struct {
		ToolCallID string `json:"toolCallId"`
		ToolName   string `json:"toolName"`
	}
	json.Unmarshal(entry.RawMessage, &msg)
	return msg.ToolCallID, msg.ToolName
}

func (tl *TreeList) parseBashCommand(entry *core.SessionEntry) string {
	if len(entry.RawMessage) == 0 {
		return ""
	}
	var msg struct {
		Command string `json:"command"`
	}
	json.Unmarshal(entry.RawMessage, &msg)
	return msg.Command
}

func (tl *TreeList) formatToolCall(name string, args map[string]any) string {
	shortenPath := func(p string) string {
		home := os.Getenv("HOME")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		if home != "" && strings.HasPrefix(p, home) {
			return "~" + p[len(home):]
		}
		return p
	}
	getStr := func(key string) string {
		if v, ok := args[key]; ok {
			return fmt.Sprintf("%v", v)
		}
		return ""
	}

	switch name {
	case "read":
		path := shortenPath(getStr("path"))
		if path == "" {
			path = shortenPath(getStr("file_path"))
		}
		display := path
		offset, hasOffset := args["offset"]
		limit, hasLimit := args["limit"]
		if hasOffset || hasLimit {
			start := 1
			if hasOffset {
				if v, ok := offset.(float64); ok {
					start = int(v)
				}
			}
			endStr := ""
			if hasLimit {
				if v, ok := limit.(float64); ok {
					endStr = fmt.Sprintf("-%d", start+int(v)-1)
				}
			}
			display += fmt.Sprintf(":%d%s", start, endStr)
		}
		return fmt.Sprintf("[read: %s]", display)
	case "write":
		return fmt.Sprintf("[write: %s]", shortenPath(getStr("path")))
	case "edit":
		return fmt.Sprintf("[edit: %s]", shortenPath(getStr("path")))
	case "bash":
		rawCmd := getStr("command")
		cmd := strings.ReplaceAll(rawCmd, "\n", " ")
		cmd = strings.ReplaceAll(cmd, "\t", " ")
		cmd = strings.TrimSpace(cmd)
		if len(cmd) > 50 {
			cmd = cmd[:50] + "..."
		}
		return fmt.Sprintf("[bash: %s]", cmd)
	case "grep":
		pattern := getStr("pattern")
		path := shortenPath(getStr("path"))
		if path == "" {
			path = "."
		}
		return fmt.Sprintf("[grep: /%s/ in %s]", pattern, path)
	case "find":
		pattern := getStr("pattern")
		path := shortenPath(getStr("path"))
		if path == "" {
			path = "."
		}
		return fmt.Sprintf("[find: %s in %s]", pattern, path)
	case "ls":
		path := shortenPath(getStr("path"))
		if path == "" {
			path = "."
		}
		return fmt.Sprintf("[ls: %s]", path)
	default:
		argsJSON, _ := json.Marshal(args)
		s := string(argsJSON)
		if len(s) > 40 {
			s = s[:40] + "..."
		}
		return fmt.Sprintf("[%s: %s]", name, s)
	}
}

// --- Input handling ---

func (tl *TreeList) HandleInput(data string) {
	if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectUp) {
		if tl.selectedIndex == 0 {
			tl.selectedIndex = len(tl.filteredNodes) - 1
		} else {
			tl.selectedIndex--
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectDown) {
		if tl.selectedIndex == len(tl.filteredNodes)-1 {
			tl.selectedIndex = 0
		} else {
			tl.selectedIndex++
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActCursorLeft) {
		// Page up
		tl.selectedIndex = max(0, tl.selectedIndex-tl.maxVisibleLines)
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActCursorRight) {
		// Page down
		tl.selectedIndex = min(len(tl.filteredNodes)-1, tl.selectedIndex+tl.maxVisibleLines)
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectConfirm) {
		if tl.selectedIndex < len(tl.filteredNodes) && tl.OnSelect != nil {
			tl.OnSelect(tl.filteredNodes[tl.selectedIndex].node.Entry.ID)
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel) {
		if tl.searchQuery != "" {
			tl.searchQuery = ""
			tl.applyFilter()
		} else if tl.OnCancel != nil {
			tl.OnCancel()
		}
	} else if tui.MatchesKey(data, tui.KeyCtrl("d")) {
		tl.filter = filterDefault
		tl.applyFilter()
	} else if tui.MatchesKey(data, tui.KeyCtrl("t")) {
		if tl.filter == filterNoTools {
			tl.filter = filterDefault
		} else {
			tl.filter = filterNoTools
		}
		tl.applyFilter()
	} else if tui.MatchesKey(data, tui.KeyCtrl("u")) {
		if tl.filter == filterUserOnly {
			tl.filter = filterDefault
		} else {
			tl.filter = filterUserOnly
		}
		tl.applyFilter()
	} else if tui.MatchesKey(data, tui.KeyCtrl("l")) {
		if tl.filter == filterLabeledOnly {
			tl.filter = filterDefault
		} else {
			tl.filter = filterLabeledOnly
		}
		tl.applyFilter()
	} else if tui.MatchesKey(data, tui.KeyCtrl("a")) {
		if tl.filter == filterAll {
			tl.filter = filterDefault
		} else {
			tl.filter = filterAll
		}
		tl.applyFilter()
	} else if tui.MatchesKey(data, tui.KeyCtrl("o")) {
		// Cycle filter forward
		currentIdx := indexOf(filterModes, tl.filter)
		tl.filter = filterModes[(currentIdx+1)%len(filterModes)]
		tl.applyFilter()
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActDeleteCharBackward) {
		if len(tl.searchQuery) > 0 {
			tl.searchQuery = tl.searchQuery[:len(tl.searchQuery)-1]
			tl.applyFilter()
		}
	} else if tui.MatchesKey(data, "shift+l") || tui.MatchesKey(data, "L") {
		if tl.selectedIndex < len(tl.filteredNodes) && tl.OnLabelEdit != nil {
			fn := tl.filteredNodes[tl.selectedIndex]
			tl.OnLabelEdit(fn.node.Entry.ID, fn.node.Label)
		}
	} else {
		// Type to search: append printable characters
		hasControl := false
		for _, ch := range data {
			code := int(ch)
			if code < 32 || code == 0x7f || (code >= 0x80 && code <= 0x9f) {
				hasControl = true
				break
			}
		}
		if !hasControl && len(data) > 0 {
			tl.searchQuery += data
			tl.applyFilter()
		}
	}
}

func indexOf(modes []filterMode, m filterMode) int {
	for i, v := range modes {
		if v == m {
			return i
		}
	}
	return 0
}

// --- TreeSelectorComponent ---

// TreeSelectorComponent renders a session tree selector for navigation.
type TreeSelectorComponent struct {
	tui.Container
	treeList *TreeList
}

// NewTreeSelectorComponent creates the full tree selector UI.
func NewTreeSelectorComponent(
	tree []*core.SessionTreeNode,
	currentLeafID string,
	onSelect func(entryID string),
	onCancel func(),
) *TreeSelectorComponent {
	tl := newTreeList(tree, currentLeafID)
	tl.OnSelect = onSelect
	tl.OnCancel = onCancel

	comp := &TreeSelectorComponent{treeList: tl}

	t := theme.GetTheme()

	comp.AddChild(tuicomp.NewSpacer(1))
	comp.AddChild(NewDynamicBorder(nil))
	comp.AddChild(tuicomp.NewText(t.Bold("  Conversation Tree"), 1, 0, nil))
	comp.AddChild(tuicomp.NewText(
		t.Fg("muted", "  ↑/↓: move. ←/→: page. Shift+L: label. ")+
			t.Fg("muted", "^D/^T/^U/^L/^A: filters (^O cycle)"),
		0, 0, nil))
	comp.AddChild(NewDynamicBorder(nil))
	comp.AddChild(tuicomp.NewSpacer(1))
	comp.AddChild(tl)
	comp.AddChild(tuicomp.NewSpacer(1))
	comp.AddChild(NewDynamicBorder(nil))

	return comp
}

// SelectedNodeID returns the ID of the currently selected entry.
func (c *TreeSelectorComponent) SelectedNodeID() string {
	return c.treeList.SelectedNodeID()
}

// HandleInput delegates input to the tree list.
func (c *TreeSelectorComponent) HandleInput(data string) {
	c.treeList.HandleInput(data)
}
