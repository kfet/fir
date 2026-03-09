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
