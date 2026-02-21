// Ported from: packages/coding-agent/src/modes/interactive/components/config-selector.ts
// Upstream hash: a1b2c3d4
package components

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// ResourceType classifies config resources.
type ResourceType string

const (
	ResourceTypeExtensions ResourceType = "extensions"
	ResourceTypeSkills     ResourceType = "skills"
	ResourceTypePrompts    ResourceType = "prompts"
	ResourceTypeThemes     ResourceType = "themes"
)

var resourceTypeLabels = map[ResourceType]string{
	ResourceTypeExtensions: "Extensions",
	ResourceTypeSkills:     "Skills",
	ResourceTypePrompts:    "Prompts",
	ResourceTypeThemes:     "Themes",
}

// ResourceItem is a single configurable resource.
type ResourceItem struct {
	Path         string
	Enabled      bool
	Metadata     core.PathMetadata
	ResourceType ResourceType
	DisplayName  string
	GroupKey     string
	SubgroupKey  string
}

// ResourceSubgroup groups items by resource type within a group.
type ResourceSubgroup struct {
	Type  ResourceType
	Label string
	Items []*ResourceItem
}

// ResourceGroup groups resources by scope/origin.
type ResourceGroup struct {
	Key       string
	Label     string
	Scope     string // "user" or "project"
	Origin    string // "package" or "top-level"
	Source    string
	Subgroups []*ResourceSubgroup
}

// ResolvedResource represents a resolved resource path with enabled state.
type ResolvedResource struct {
	Path     string
	Enabled  bool
	Metadata core.PathMetadata
}

// ResolvedPaths holds all resolved resource paths by type.
type ResolvedPaths struct {
	Extensions []ResolvedResource
	Skills     []ResolvedResource
	Prompts    []ResolvedResource
	Themes     []ResolvedResource
}

func getGroupLabel(metadata core.PathMetadata) string {
	if metadata.Origin == "package" {
		return metadata.Source + " (" + metadata.Scope + ")"
	}
	if metadata.Source == "auto" {
		if metadata.Scope == "user" {
			return "User (~/.fir/agent/)"
		}
		return "Project (.fir/)"
	}
	if metadata.Scope == "user" {
		return "User settings"
	}
	return "Project settings"
}

func buildGroups(resolved ResolvedPaths) []*ResourceGroup {
	groupMap := map[string]*ResourceGroup{}

	addToGroup := func(resources []ResolvedResource, resType ResourceType) {
		for _, res := range resources {
			groupKey := res.Metadata.Origin + ":" + res.Metadata.Scope + ":" + res.Metadata.Source

			g, ok := groupMap[groupKey]
			if !ok {
				g = &ResourceGroup{
					Key:    groupKey,
					Label:  getGroupLabel(res.Metadata),
					Scope:  res.Metadata.Scope,
					Origin: res.Metadata.Origin,
					Source: res.Metadata.Source,
				}
				groupMap[groupKey] = g
			}

			subgroupKey := groupKey + ":" + string(resType)
			var sg *ResourceSubgroup
			for _, s := range g.Subgroups {
				if s.Type == resType {
					sg = s
					break
				}
			}
			if sg == nil {
				sg = &ResourceSubgroup{
					Type:  resType,
					Label: resourceTypeLabels[resType],
				}
				g.Subgroups = append(g.Subgroups, sg)
			}

			fileName := filepath.Base(res.Path)
			parentFolder := filepath.Base(filepath.Dir(res.Path))
			displayName := fileName
			if resType == ResourceTypeExtensions && parentFolder != "extensions" {
				displayName = parentFolder + "/" + fileName
			} else if resType == ResourceTypeSkills && fileName == "SKILL.md" {
				displayName = parentFolder
			}

			sg.Items = append(sg.Items, &ResourceItem{
				Path:         res.Path,
				Enabled:      res.Enabled,
				Metadata:     res.Metadata,
				ResourceType: resType,
				DisplayName:  displayName,
				GroupKey:     groupKey,
				SubgroupKey:  subgroupKey,
			})
		}
	}

	addToGroup(resolved.Extensions, ResourceTypeExtensions)
	addToGroup(resolved.Skills, ResourceTypeSkills)
	addToGroup(resolved.Prompts, ResourceTypePrompts)
	addToGroup(resolved.Themes, ResourceTypeThemes)

	// Sort groups: packages first, then by scope
	groups := make([]*ResourceGroup, 0, len(groupMap))
	for _, g := range groupMap {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.Origin != b.Origin {
			return a.Origin == "package"
		}
		if a.Scope != b.Scope {
			return a.Scope == "user"
		}
		return a.Source < b.Source
	})

	// Sort subgroups and items
	typeOrder := map[ResourceType]int{
		ResourceTypeExtensions: 0, ResourceTypeSkills: 1,
		ResourceTypePrompts: 2, ResourceTypeThemes: 3,
	}
	for _, g := range groups {
		sort.Slice(g.Subgroups, func(i, j int) bool {
			return typeOrder[g.Subgroups[i].Type] < typeOrder[g.Subgroups[j].Type]
		})
		for _, sg := range g.Subgroups {
			sort.Slice(sg.Items, func(i, j int) bool {
				return sg.Items[i].DisplayName < sg.Items[j].DisplayName
			})
		}
	}
	return groups
}

// flatEntry is one row in the flattened resource list.
type flatEntry struct {
	entryType string // "group", "subgroup", "item"
	group     *ResourceGroup
	subgroup  *ResourceSubgroup
	item      *ResourceItem
}

// resourceList implements the scrollable, searchable resource list.
type resourceList struct {
	groups        []*ResourceGroup
	flatItems     []flatEntry
	filtered      []flatEntry
	selectedIndex int
	searchInput   *tuicomp.Input
	maxVisible    int
	OnCancel      func()
	OnExit        func()
	OnToggle      func(item *ResourceItem, enabled bool)
}

func newResourceList(groups []*ResourceGroup) *resourceList {
	rl := &resourceList{
		groups:     groups,
		maxVisible: 15,
	}
	rl.searchInput = tuicomp.NewInput()
	rl.buildFlatList()
	rl.filtered = make([]flatEntry, len(rl.flatItems))
	copy(rl.filtered, rl.flatItems)
	rl.selectFirstItem()
	return rl
}

func (rl *resourceList) buildFlatList() {
	rl.flatItems = nil
	for _, g := range rl.groups {
		rl.flatItems = append(rl.flatItems, flatEntry{entryType: "group", group: g})
		for _, sg := range g.Subgroups {
			rl.flatItems = append(rl.flatItems, flatEntry{entryType: "subgroup", group: g, subgroup: sg})
			for _, item := range sg.Items {
				rl.flatItems = append(rl.flatItems, flatEntry{entryType: "item", item: item})
			}
		}
	}
	rl.selectFirstItem()
}

func (rl *resourceList) selectFirstItem() {
	for i, e := range rl.filtered {
		if e.entryType == "item" {
			rl.selectedIndex = i
			return
		}
	}
	rl.selectedIndex = 0
}

func (rl *resourceList) findNextItem(from, dir int) int {
	idx := from + dir
	for idx >= 0 && idx < len(rl.filtered) {
		if rl.filtered[idx].entryType == "item" {
			return idx
		}
		idx += dir
	}
	return from
}

func (rl *resourceList) filterItems(query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		rl.filtered = make([]flatEntry, len(rl.flatItems))
		copy(rl.filtered, rl.flatItems)
		rl.selectFirstItem()
		return
	}

	lower := strings.ToLower(query)
	matchingItems := map[*ResourceItem]bool{}
	for _, e := range rl.flatItems {
		if e.entryType == "item" {
			if strings.Contains(strings.ToLower(e.item.DisplayName), lower) ||
				strings.Contains(strings.ToLower(string(e.item.ResourceType)), lower) ||
				strings.Contains(strings.ToLower(e.item.Path), lower) {
				matchingItems[e.item] = true
			}
		}
	}

	matchingSGs := map[*ResourceSubgroup]bool{}
	matchingGs := map[*ResourceGroup]bool{}
	for _, g := range rl.groups {
		for _, sg := range g.Subgroups {
			for _, it := range sg.Items {
				if matchingItems[it] {
					matchingSGs[sg] = true
					matchingGs[g] = true
				}
			}
		}
	}

	rl.filtered = nil
	for _, e := range rl.flatItems {
		switch e.entryType {
		case "group":
			if matchingGs[e.group] {
				rl.filtered = append(rl.filtered, e)
			}
		case "subgroup":
			if matchingSGs[e.subgroup] {
				rl.filtered = append(rl.filtered, e)
			}
		case "item":
			if matchingItems[e.item] {
				rl.filtered = append(rl.filtered, e)
			}
		}
	}
	rl.selectFirstItem()
}

func (rl *resourceList) Invalidate() {}

func (rl *resourceList) Render(width int) []string {
	t := theme.GetTheme()
	lines := rl.searchInput.Render(width)
	lines = append(lines, "")

	if len(rl.filtered) == 0 {
		lines = append(lines, t.Fg("muted", "  No resources found"))
		return lines
	}

	start := rl.selectedIndex - rl.maxVisible/2
	if start > len(rl.filtered)-rl.maxVisible {
		start = len(rl.filtered) - rl.maxVisible
	}
	if start < 0 {
		start = 0
	}
	end := start + rl.maxVisible
	if end > len(rl.filtered) {
		end = len(rl.filtered)
	}

	for i := start; i < end; i++ {
		e := rl.filtered[i]
		isSelected := i == rl.selectedIndex

		switch e.entryType {
		case "group":
			lines = append(lines, tui.TruncateToWidth("  "+t.Fg("accent", t.Bold(e.group.Label)), width, "", false))
		case "subgroup":
			lines = append(lines, tui.TruncateToWidth("    "+t.Fg("muted", e.subgroup.Label), width, "", false))
		case "item":
			cursor := "  "
			if isSelected {
				cursor = "> "
			}
			checkbox := t.Fg("dim", "[ ]")
			if e.item.Enabled {
				checkbox = t.Fg("success", "[x]")
			}
			name := e.item.DisplayName
			if isSelected {
				name = t.Bold(name)
			}
			lines = append(lines, tui.TruncateToWidth(cursor+"    "+checkbox+" "+name, width, "...", false))
		}
	}

	if start > 0 || end < len(rl.filtered) {
		lines = append(lines, t.Fg("dim", "  ("+strings.Repeat(" ", 0)+")"))
	}
	return lines
}

func (rl *resourceList) HandleInput(data string) {
	if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectUp) {
		rl.selectedIndex = rl.findNextItem(rl.selectedIndex, -1)
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectDown) {
		rl.selectedIndex = rl.findNextItem(rl.selectedIndex, 1)
	} else if tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel) {
		if rl.OnCancel != nil {
			rl.OnCancel()
		}
	} else if data == " " || tuicomp.MatchesEditorAction(data, tuicomp.ActSelectConfirm) {
		if rl.selectedIndex >= 0 && rl.selectedIndex < len(rl.filtered) {
			e := rl.filtered[rl.selectedIndex]
			if e.entryType == "item" {
				e.item.Enabled = !e.item.Enabled
				if rl.OnToggle != nil {
					rl.OnToggle(e.item, e.item.Enabled)
				}
			}
		}
	} else {
		rl.searchInput.HandleInput(data)
	}
}

// ConfigSelectorComponent renders a resource configuration selector.
type ConfigSelectorComponent struct {
	tui.Container
	resourceList *resourceList
}

// NewConfigSelectorComponent creates a new config selector.
func NewConfigSelectorComponent(
	resolved ResolvedPaths,
	onClose func(),
	onExit func(),
	requestRender func(),
) *ConfigSelectorComponent {
	groups := buildGroups(resolved)
	c := &ConfigSelectorComponent{}

	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewSpacer(1))

	t := theme.GetTheme()
	c.AddChild(tuicomp.NewText(t.Bold("Resource Configuration"), 0, 0, nil))
	c.AddChild(tuicomp.NewSpacer(1))

	c.resourceList = newResourceList(groups)
	c.resourceList.OnCancel = onClose
	c.resourceList.OnExit = onExit
	c.resourceList.OnToggle = func(item *ResourceItem, enabled bool) {
		if requestRender != nil {
			requestRender()
		}
	}
	c.AddChild(c.resourceList)

	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(NewDynamicBorder(nil))

	return c
}

// HandleInput processes keyboard input.
func (c *ConfigSelectorComponent) HandleInput(data string) {
	c.resourceList.HandleInput(data)
}

// GetResourceList returns the internal resource list for testing.
func (c *ConfigSelectorComponent) GetResourceList() *resourceList {
	return c.resourceList
}
