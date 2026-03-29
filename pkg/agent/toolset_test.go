package agent

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func makeTool(name string) AgentTool {
	return AgentTool{Tool: ai.Tool{Name: name, Description: name + " desc"}}
}

func TestToolSet_Add_NoDuplicates(t *testing.T) {
	ts := NewToolSet()
	ts.Add(makeTool("a"))
	ts.Add(makeTool("b"))
	ts.Add(makeTool("a")) // overwrite, not duplicate

	if ts.Len() != 2 {
		t.Fatalf("expected 2 tools, got %d", ts.Len())
	}
	names := ts.Names()
	if names[0] != "a" || names[1] != "b" {
		t.Fatalf("unexpected order: %v", names)
	}
}

func TestToolSet_Remove(t *testing.T) {
	ts := ToolSetFrom([]AgentTool{makeTool("a"), makeTool("b"), makeTool("c")})
	ts.Remove("b")
	if ts.Len() != 2 {
		t.Fatalf("expected 2, got %d", ts.Len())
	}
	if ts.Has("b") {
		t.Fatal("b should be removed")
	}
	names := ts.Names()
	if names[0] != "a" || names[1] != "c" {
		t.Fatalf("unexpected order: %v", names)
	}
}

func TestToolSet_Get(t *testing.T) {
	ts := ToolSetFrom([]AgentTool{makeTool("x")})
	tool, ok := ts.Get("x")
	if !ok || tool.Name != "x" {
		t.Fatal("expected to find x")
	}
	_, ok = ts.Get("y")
	if ok {
		t.Fatal("should not find y")
	}
}

func TestToolSet_Clone(t *testing.T) {
	ts := ToolSetFrom([]AgentTool{makeTool("a"), makeTool("b")})
	c := ts.Clone()
	c.Add(makeTool("c"))
	if ts.Len() != 2 {
		t.Fatal("clone mutated original")
	}
	if c.Len() != 3 {
		t.Fatal("clone should have 3")
	}
}

func TestToolSetFrom_LastWins(t *testing.T) {
	t1 := makeTool("a")
	t1.Description = "first"
	t2 := makeTool("a")
	t2.Description = "second"

	ts := ToolSetFrom([]AgentTool{t1, t2})
	if ts.Len() != 1 {
		t.Fatalf("expected 1, got %d", ts.Len())
	}
	got, _ := ts.Get("a")
	if got.Description != "second" {
		t.Fatalf("expected last-wins, got %q", got.Description)
	}
}

// --- UpdateTools tests: verify tools survive concurrent mutations ---

func makeAgent(toolNames ...string) *Agent {
	tools := make([]AgentTool, len(toolNames))
	for i, n := range toolNames {
		tools[i] = makeTool(n)
	}
	return NewAgent(AgentOptions{
		InitialState: &AgentState{Tools: ToolSetFrom(tools)},
	})
}

func assertHasTools(t *testing.T, a *Agent, names ...string) {
	t.Helper()
	ts := a.State().Tools
	for _, n := range names {
		if !ts.Has(n) {
			t.Errorf("tool %q missing; have %v", n, ts.Names())
		}
	}
}

func TestUpdateTools_AddPreservesExisting(t *testing.T) {
	a := makeAgent("read", "bash", "edit", "write", "plan")
	a.UpdateTools(func(ts *ToolSet) {
		ts.Add(makeTool("mcp__echo"))
	})
	assertHasTools(t, a, "read", "bash", "edit", "write", "plan", "mcp__echo")
}

func TestUpdateTools_RemovePreservesOthers(t *testing.T) {
	a := makeAgent("read", "bash", "edit", "write", "plan", "mcp__echo")
	a.UpdateTools(func(ts *ToolSet) {
		ts.Remove("mcp__echo")
	})
	assertHasTools(t, a, "read", "bash", "edit", "write", "plan")
	if a.State().Tools.Has("mcp__echo") {
		t.Error("mcp__echo should have been removed")
	}
}

func TestUpdateTools_GroupReplacePreservesOthers(t *testing.T) {
	// Simulate: base tools exist, then MCP adds tools, then MCP replaces them.
	a := makeAgent("read", "bash", "edit", "write", "plan")

	// First MCP load: add echo + greet
	var prevMCP []string
	mcpUpdate := func(names []string) {
		a.UpdateTools(func(ts *ToolSet) {
			for _, old := range prevMCP {
				ts.Remove(old)
			}
			for _, n := range names {
				ts.Add(makeTool(n))
			}
		})
		prevMCP = names
	}

	mcpUpdate([]string{"mcp__echo", "mcp__greet"})
	assertHasTools(t, a, "read", "bash", "edit", "write", "plan", "mcp__echo", "mcp__greet")

	// Second MCP load: replace with different tools
	mcpUpdate([]string{"mcp__search"})
	assertHasTools(t, a, "read", "bash", "edit", "write", "plan", "mcp__search")
	if a.State().Tools.Has("mcp__echo") {
		t.Error("mcp__echo should have been replaced")
	}
	if a.State().Tools.Has("mcp__greet") {
		t.Error("mcp__greet should have been replaced")
	}
}

func TestUpdateTools_WrapPreservesAll(t *testing.T) {
	// Simulate hook wrapping: transform every tool in place.
	a := makeAgent("read", "bash", "edit", "write", "plan")
	a.UpdateTools(func(ts *ToolSet) {
		for _, name := range ts.Names() {
			if tool, ok := ts.Get(name); ok {
				tool.Description = "wrapped:" + tool.Description
				ts.Add(tool)
			}
		}
	})
	assertHasTools(t, a, "read", "bash", "edit", "write", "plan")
	if got := a.State().Tools.Len(); got != 5 {
		t.Errorf("expected 5 tools after wrap, got %d", got)
	}
}

func TestUpdateTools_ConcurrentMutations(t *testing.T) {
	// Simulate multiple subsystems mutating tools concurrently.
	a := makeAgent("read", "bash", "edit", "write", "plan")
	base := []string{"read", "bash", "edit", "write", "plan"}

	done := make(chan struct{})
	// Goroutine 1: MCP updates
	go func() {
		for i := 0; i < 100; i++ {
			a.UpdateTools(func(ts *ToolSet) {
				ts.Add(makeTool("mcp__tool"))
			})
			a.UpdateTools(func(ts *ToolSet) {
				ts.Remove("mcp__tool")
			})
		}
		done <- struct{}{}
	}()
	// Goroutine 2: extension updates
	go func() {
		for i := 0; i < 100; i++ {
			a.UpdateTools(func(ts *ToolSet) {
				ts.Add(makeTool("ext__tool"))
			})
			a.UpdateTools(func(ts *ToolSet) {
				ts.Remove("ext__tool")
			})
		}
		done <- struct{}{}
	}()
	<-done
	<-done

	// Base tools must survive all mutations.
	assertHasTools(t, a, base...)
}
