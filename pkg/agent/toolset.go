package agent

import (
	"sort"
	"strings"
)

// ToolSet is an ordered, name-unique collection of AgentTool values.
// Adding a tool with a name that already exists overwrites the previous entry
// (keeping insertion order of the *first* occurrence). This makes duplicate
// tool names structurally impossible.
//
// All read methods are nil-safe: calling them on a nil *ToolSet returns
// zero values rather than panicking.
type ToolSet struct {
	order  []string             // insertion-ordered names
	byName map[string]AgentTool // name → tool
}

// NewToolSet creates an empty ToolSet.
func NewToolSet() *ToolSet {
	return &ToolSet{byName: make(map[string]AgentTool)}
}

// ToolSetFrom builds a ToolSet from a slice of tools. If the slice contains
// duplicate names, the last entry wins.
func ToolSetFrom(tools []AgentTool) *ToolSet {
	ts := &ToolSet{byName: make(map[string]AgentTool, len(tools))}
	for _, t := range tools {
		ts.Add(t)
	}
	return ts
}

// Add inserts or replaces a tool. If a tool with the same name already
// exists, the definition is updated in place without changing order.
func (ts *ToolSet) Add(t AgentTool) {
	if _, exists := ts.byName[t.Name]; !exists {
		ts.order = append(ts.order, t.Name)
	}
	ts.byName[t.Name] = t
}

// Get returns the tool with the given name and true, or zero value and false.
func (ts *ToolSet) Get(name string) (AgentTool, bool) {
	if ts == nil {
		return AgentTool{}, false
	}
	t, ok := ts.byName[name]
	return t, ok
}

// Remove deletes a tool by name. No-op if the name doesn't exist or ts is nil.
func (ts *ToolSet) Remove(name string) {
	if ts == nil {
		return
	}
	if _, ok := ts.byName[name]; !ok {
		return
	}
	delete(ts.byName, name)
	for i, n := range ts.order {
		if n == name {
			ts.order = append(ts.order[:i], ts.order[i+1:]...)
			break
		}
	}
}

// Len returns the number of tools.
func (ts *ToolSet) Len() int {
	if ts == nil {
		return 0
	}
	return len(ts.byName)
}

// Slice returns a copy of the tools in insertion order.
func (ts *ToolSet) Slice() []AgentTool {
	if ts == nil {
		return nil
	}
	out := make([]AgentTool, len(ts.order))
	for i, name := range ts.order {
		out[i] = ts.byName[name]
	}
	return out
}

// Names returns tool names in insertion order.
func (ts *ToolSet) Names() []string {
	if ts == nil {
		return nil
	}
	out := make([]string, len(ts.order))
	copy(out, ts.order)
	return out
}

// Has reports whether a tool with the given name exists.
func (ts *ToolSet) Has(name string) bool {
	if ts == nil {
		return false
	}
	_, ok := ts.byName[name]
	return ok
}

// Clone returns a deep copy of the ToolSet. Returns nil if ts is nil.
func (ts *ToolSet) Clone() *ToolSet {
	if ts == nil {
		return nil
	}
	c := &ToolSet{
		order:  make([]string, len(ts.order)),
		byName: make(map[string]AgentTool, len(ts.byName)),
	}
	copy(c.order, ts.order)
	for k, v := range ts.byName {
		c.byName[k] = v
	}
	return c
}

// ToolClassification holds tools grouped into built-in and per-extension buckets.
// MCP tools (prefixed "mcp__") are excluded.
type ToolClassification struct {
	Builtin    []string            // sorted built-in tool names
	Extensions map[string][]string // extension name → sorted tool names
}

// ClassifyTools partitions the tool set into built-in and per-extension groups,
// excluding MCP tools. extensionTools maps extension name → tool names and is
// used to attribute tools to their owning extension; any non-MCP tool not in
// the map is considered built-in. Both the built-in list and per-extension
// lists are sorted alphabetically.
func (ts *ToolSet) ClassifyTools(extensionTools map[string][]string) ToolClassification {
	// Invert extensionTools to tool name → extension name.
	extToolMap := make(map[string]string)
	for ext, names := range extensionTools {
		for _, n := range names {
			extToolMap[n] = ext
		}
	}

	var builtin []string
	extGrouped := make(map[string][]string)
	for _, t := range ts.Slice() {
		if strings.HasPrefix(t.Name, "mcp__") {
			continue
		}
		if ext, ok := extToolMap[t.Name]; ok {
			extGrouped[ext] = append(extGrouped[ext], t.Name)
		} else {
			builtin = append(builtin, t.Name)
		}
	}

	sort.Strings(builtin)
	for ext := range extGrouped {
		sort.Strings(extGrouped[ext])
	}
	return ToolClassification{Builtin: builtin, Extensions: extGrouped}
}
