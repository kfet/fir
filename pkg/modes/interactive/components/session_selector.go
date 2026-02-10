// Ported from: packages/coding-agent/src/modes/interactive/components/session-selector.ts
// Upstream hash: d4e5f6a7
package components

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kfet/pi-go/pkg/core"
	"github.com/kfet/pi-go/pkg/modes/interactive/theme"
	"github.com/kfet/pi-go/pkg/tui"
	tuicomp "github.com/kfet/pi-go/pkg/tui/components"
)

// SessionScope controls which sessions are shown.
type SessionScope string

const (
	SessionScopeCurrent SessionScope = "current"
	SessionScopeAll     SessionScope = "all"
)

// SortMode aliases are re-exported from session_selector_search.go.
// Use SortThreaded, SortRecent, SortRelevance from that file.

// sessionTreeNode is a hierarchical session tree node.
type sessionTreeNode struct {
	session  core.SessionListInfo
	children []*sessionTreeNode
}

// flatSessionNode is a flattened tree node for display.
type flatSessionNode struct {
	session           core.SessionListInfo
	depth             int
	isLast            bool
	ancestorContinues []bool
}

// buildSessionTree builds a tree from sessions based on parent paths.
func buildSessionTree(sessions []core.SessionListInfo) []*sessionTreeNode {
	byPath := map[string]*sessionTreeNode{}
	for i := range sessions {
		byPath[sessions[i].Path] = &sessionTreeNode{session: sessions[i]}
	}

	var roots []*sessionTreeNode
	for i := range sessions {
		node := byPath[sessions[i].Path]
		parent := sessions[i].ParentSessionPath
		if parent != "" {
			if pn, ok := byPath[parent]; ok {
				pn.children = append(pn.children, node)
				continue
			}
		}
		roots = append(roots, node)
	}

	var sortNodes func([]*sessionTreeNode)
	sortNodes = func(nodes []*sessionTreeNode) {
		sort.Slice(nodes, func(i, j int) bool {
			return nodes[i].session.Modified.After(nodes[j].session.Modified)
		})
		for _, n := range nodes {
			sortNodes(n.children)
		}
	}
	sortNodes(roots)
	return roots
}

// flattenSessionTree flattens a tree into a display list.
func flattenSessionTree(roots []*sessionTreeNode) []flatSessionNode {
	var result []flatSessionNode
	var walk func(node *sessionTreeNode, depth int, ancestors []bool, isLast bool)
	walk = func(node *sessionTreeNode, depth int, ancestors []bool, isLast bool) {
		result = append(result, flatSessionNode{
			session:           node.session,
			depth:             depth,
			isLast:            isLast,
			ancestorContinues: append([]bool{}, ancestors...),
		})
		for i, child := range node.children {
			childIsLast := i == len(node.children)-1
			continues := depth > 0 && !isLast
			walk(child, depth+1, append(ancestors, continues), childIsLast)
		}
	}
	for i, root := range roots {
		walk(root, 0, nil, i == len(roots)-1)
	}
	return result
}

// shortenPath replaces home dir with ~.
func shortenPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || path == "" {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// formatSessionDate formats a time relative to now.
func formatSessionDate(t time.Time) string {
	diff := time.Since(t)
	mins := int(diff.Minutes())
	hours := int(diff.Hours())
	days := int(diff.Hours() / 24)

	switch {
	case mins < 1:
		return "now"
	case mins < 60:
		return fmt.Sprintf("%dm", mins)
	case hours < 24:
		return fmt.Sprintf("%dh", hours)
	case days < 7:
		return fmt.Sprintf("%dd", days)
	case days < 30:
		return fmt.Sprintf("%dw", days/7)
	case days < 365:
		return fmt.Sprintf("%dmo", days/30)
	default:
		return fmt.Sprintf("%dy", days/365)
	}
}

// sessionList is the internal session list with search.
type sessionList struct {
	allSessions      []core.SessionListInfo
	filteredSessions []flatSessionNode
	selectedIndex    int
	searchInput      *tuicomp.Input
	showPath         bool
	sortMode         SortMode
	maxVisible       int

	OnSelect func(path string)
	OnCancel func()
	OnDelete func(path string)
}

func newSessionList() *sessionList {
	sl := &sessionList{
		sortMode:   SortThreaded,
		maxVisible: 12,
	}
	sl.searchInput = tuicomp.NewInput()
	return sl
}

// SetSessions updates the session data.
func (sl *sessionList) SetSessions(sessions []core.SessionListInfo) {
	sl.allSessions = sessions
	sl.applyFilter("")
}

func (sl *sessionList) applyFilter(query string) {
	var sessions []core.SessionListInfo
	if query == "" {
		sessions = sl.allSessions
	} else {
		lower := strings.ToLower(query)
		for _, s := range sl.allSessions {
			if strings.Contains(strings.ToLower(s.Name), lower) ||
				strings.Contains(strings.ToLower(s.FirstMessage), lower) ||
				strings.Contains(strings.ToLower(s.Cwd), lower) {
				sessions = append(sessions, s)
			}
		}
	}

	switch sl.sortMode {
	case SortRecent:
		sort.Slice(sessions, func(i, j int) bool {
			return sessions[i].Modified.After(sessions[j].Modified)
		})
		sl.filteredSessions = nil
		for _, s := range sessions {
			sl.filteredSessions = append(sl.filteredSessions, flatSessionNode{session: s})
		}
	default: // threaded
		tree := buildSessionTree(sessions)
		sl.filteredSessions = flattenSessionTree(tree)
	}

	if sl.selectedIndex >= len(sl.filteredSessions) {
		sl.selectedIndex = len(sl.filteredSessions) - 1
	}
	if sl.selectedIndex < 0 {
		sl.selectedIndex = 0
	}
}

// SelectedPath returns the path of the selected session.
func (sl *sessionList) SelectedPath() string {
	if sl.selectedIndex >= 0 && sl.selectedIndex < len(sl.filteredSessions) {
		return sl.filteredSessions[sl.selectedIndex].session.Path
	}
	return ""
}

func (sl *sessionList) Invalidate() {}

func (sl *sessionList) Render(width int) []string {
	t := theme.GetTheme()
	lines := sl.searchInput.Render(width)
	lines = append(lines, "")

	if len(sl.filteredSessions) == 0 {
		lines = append(lines, t.Fg("muted", "  No sessions found"))
		return lines
	}

	// Visible range
	start := sl.selectedIndex - sl.maxVisible/2
	if start > len(sl.filteredSessions)-sl.maxVisible {
		start = len(sl.filteredSessions) - sl.maxVisible
	}
	if start < 0 {
		start = 0
	}
	end := start + sl.maxVisible
	if end > len(sl.filteredSessions) {
		end = len(sl.filteredSessions)
	}

	for i := start; i < end; i++ {
		node := sl.filteredSessions[i]
		s := node.session
		isSelected := i == sl.selectedIndex

		// Tree prefix
		var prefix string
		if node.depth > 0 {
			for _, continues := range node.ancestorContinues {
				if continues {
					prefix += "│ "
				} else {
					prefix += "  "
				}
			}
			if node.isLast {
				prefix += "└─"
			} else {
				prefix += "├─"
			}
		}

		cursor := "  "
		if isSelected {
			cursor = t.Fg("accent", "> ")
		}

		name := s.Name
		if name == "" {
			name = s.FirstMessage
			if len(name) > 40 {
				name = name[:40] + "…"
			}
		}
		if name == "" {
			name = "(empty)"
		}
		if isSelected {
			name = t.Bold(name)
		}

		dateStr := t.Fg("dim", formatSessionDate(s.Modified))
		countStr := t.Fg("dim", fmt.Sprintf("[%d msgs]", s.MessageCount))

		line := fmt.Sprintf("%s%s%s %s %s", cursor, prefix, name, dateStr, countStr)
		lines = append(lines, tui.TruncateToWidth(line, width, "…", false))

		// Show path on selected
		if isSelected && sl.showPath {
			pathLine := "    " + t.Fg("muted", shortenPath(s.Cwd))
			lines = append(lines, tui.TruncateToWidth(pathLine, width, "…", false))
		}
	}

	// Scroll indicator
	if start > 0 || end < len(sl.filteredSessions) {
		lines = append(lines, t.Fg("dim", fmt.Sprintf("  (%d/%d)", sl.selectedIndex+1, len(sl.filteredSessions))))
	}

	return lines
}

func (sl *sessionList) HandleInput(data string) {
	if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectUp) {
		if sl.selectedIndex > 0 {
			sl.selectedIndex--
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectDown) {
		if sl.selectedIndex < len(sl.filteredSessions)-1 {
			sl.selectedIndex++
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectConfirm) {
		if sl.OnSelect != nil {
			p := sl.SelectedPath()
			if p != "" {
				sl.OnSelect(p)
			}
		}
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel) {
		if sl.OnCancel != nil {
			sl.OnCancel()
		}
	} else if data == "\t" {
		// Toggle sort mode
		switch sl.sortMode {
		case SortThreaded:
			sl.sortMode = SortRecent
		case SortRecent:
			sl.sortMode = SortThreaded
		}
		sl.applyFilter(sl.searchInput.GetValue())
	} else {
		sl.searchInput.HandleInput(data)
		sl.applyFilter(sl.searchInput.GetValue())
	}
}

// SessionSelectorComponent renders a session selector with search and tree view.
type SessionSelectorComponent struct {
	tui.Container
	sessionList *sessionList
	scope       SessionScope
}

// NewSessionSelectorComponent creates a new session selector.
func NewSessionSelectorComponent(
	sessions []core.SessionListInfo,
	scope SessionScope,
	onSelect func(path string),
	onCancel func(),
) *SessionSelectorComponent {
	t := theme.GetTheme()
	c := &SessionSelectorComponent{
		scope: scope,
	}

	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewSpacer(1))

	title := "Resume Session (Current Folder)"
	if scope == SessionScopeAll {
		title = "Resume Session (All)"
	}
	c.AddChild(tuicomp.NewText(t.Bold(title), 0, 0, nil))
	c.AddChild(tuicomp.NewSpacer(1))

	c.sessionList = newSessionList()
	c.sessionList.SetSessions(sessions)
	c.sessionList.OnSelect = onSelect
	c.sessionList.OnCancel = onCancel
	c.AddChild(c.sessionList)

	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(NewDynamicBorder(nil))

	return c
}

// HandleInput processes keyboard input.
func (c *SessionSelectorComponent) HandleInput(data string) {
	c.sessionList.HandleInput(data)
}

// SetSessions updates the session data.
func (c *SessionSelectorComponent) SetSessions(sessions []core.SessionListInfo) {
	c.sessionList.SetSessions(sessions)
}

// GetSessionList returns the internal session list.
func (c *SessionSelectorComponent) GetSessionList() *sessionList {
	return c.sessionList
}

// SelectedPath returns the currently selected session path.
func (c *SessionSelectorComponent) SelectedPath() string {
	return c.sessionList.SelectedPath()
}
