package components

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/resources"
)

func TestBuildGroups_Empty(t *testing.T) {
	groups := buildGroups(ResolvedPaths{})
	if len(groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(groups))
	}
}

func TestBuildGroups_SingleItem(t *testing.T) {
	resolved := ResolvedPaths{
		Skills: []ResolvedResource{
			{
				Path:    "/home/user/.fir/agent/skills/test/SKILL.md",
				Enabled: true,
				Metadata: resources.PathMetadata{
					Source: "auto",
					Scope:  "user",
					Origin: "top-level",
				},
			},
		},
	}
	groups := buildGroups(resolved)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	g := groups[0]
	if g.Scope != "user" {
		t.Errorf("expected scope user, got %s", g.Scope)
	}
	if len(g.Subgroups) != 1 {
		t.Fatalf("expected 1 subgroup, got %d", len(g.Subgroups))
	}
	if g.Subgroups[0].Type != ResourceTypeSkills {
		t.Errorf("expected skills subgroup, got %s", g.Subgroups[0].Type)
	}
	if len(g.Subgroups[0].Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(g.Subgroups[0].Items))
	}
	// SKILL.md should use parent folder name
	if g.Subgroups[0].Items[0].DisplayName != "test" {
		t.Errorf("expected display name 'test', got %s", g.Subgroups[0].Items[0].DisplayName)
	}
}

func TestBuildGroups_Sorting(t *testing.T) {
	resolved := ResolvedPaths{
		Extensions: []ResolvedResource{
			{
				Path:    "/project/.fir/extensions/foo.ts",
				Enabled: true,
				Metadata: resources.PathMetadata{Source: "auto", Scope: "project", Origin: "top-level"},
			},
		},
		Skills: []ResolvedResource{
			{
				Path:    "/home/.fir/agent/skills/bar/SKILL.md",
				Enabled: true,
				Metadata: resources.PathMetadata{Source: "auto", Scope: "user", Origin: "top-level"},
			},
		},
	}
	groups := buildGroups(resolved)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	// User should come before project
	if groups[0].Scope != "user" {
		t.Errorf("expected user scope first, got %s", groups[0].Scope)
	}
	if groups[1].Scope != "project" {
		t.Errorf("expected project scope second, got %s", groups[1].Scope)
	}
}

func TestConfigSelectorComponent_Render(t *testing.T) {
	resolved := ResolvedPaths{
		Skills: []ResolvedResource{
			{
				Path:    "/home/.fir/agent/skills/test/SKILL.md",
				Enabled: true,
				Metadata: resources.PathMetadata{Source: "auto", Scope: "user", Origin: "top-level"},
			},
		},
	}
	comp := NewConfigSelectorComponent(resolved, func() {}, func() {}, func() {})
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected non-empty render output")
	}
	// Should contain "Resource Configuration"
	found := false
	for _, line := range lines {
		if strings.Contains(line, "Resource Configuration") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Resource Configuration' in output")
	}
}

func TestResourceList_FilterItems(t *testing.T) {
	groups := []*ResourceGroup{
		{
			Key: "test", Label: "Test", Scope: "user", Origin: "top-level", Source: "auto",
			Subgroups: []*ResourceSubgroup{
				{
					Type: ResourceTypeSkills, Label: "Skills",
					Items: []*ResourceItem{
						{Path: "/a/foo.md", Enabled: true, DisplayName: "foo", ResourceType: ResourceTypeSkills},
						{Path: "/a/bar.md", Enabled: false, DisplayName: "bar", ResourceType: ResourceTypeSkills},
					},
				},
			},
		},
	}
	rl := newResourceList(groups)

	// Initially all items visible
	if len(rl.filtered) != 4 { // group + subgroup + 2 items
		t.Errorf("expected 4 entries, got %d", len(rl.filtered))
	}

	// Filter for "foo"
	rl.filterItems("foo")
	itemCount := 0
	for _, e := range rl.filtered {
		if e.entryType == "item" {
			itemCount++
		}
	}
	if itemCount != 1 {
		t.Errorf("expected 1 matching item, got %d", itemCount)
	}

	// Clear filter
	rl.filterItems("")
	if len(rl.filtered) != 4 {
		t.Errorf("expected 4 entries after clearing filter, got %d", len(rl.filtered))
	}
}

func TestResourceList_Navigation(t *testing.T) {
	groups := []*ResourceGroup{
		{
			Key: "test", Label: "Test", Scope: "user", Origin: "top-level", Source: "auto",
			Subgroups: []*ResourceSubgroup{
				{
					Type: ResourceTypeSkills, Label: "Skills",
					Items: []*ResourceItem{
						{Path: "/a/foo.md", Enabled: true, DisplayName: "foo"},
						{Path: "/a/bar.md", Enabled: false, DisplayName: "bar"},
					},
				},
			},
		},
	}
	rl := newResourceList(groups)

	// Should start on first item (index 2: group=0, subgroup=1, item=2)
	if rl.filtered[rl.selectedIndex].entryType != "item" {
		t.Errorf("expected selected to be an item, got %s at index %d", rl.filtered[rl.selectedIndex].entryType, rl.selectedIndex)
	}

	// Move down should go to next item
	orig := rl.selectedIndex
	next := rl.findNextItem(orig, 1)
	if next <= orig {
		t.Errorf("expected next item after %d, got %d", orig, next)
	}
}
