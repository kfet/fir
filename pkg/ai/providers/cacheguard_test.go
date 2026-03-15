package providers

import (
	"testing"
)

func TestPrefixGuard_FirstCallNoInvalidation(t *testing.T) {
	pg := NewPrefixGuard()
	sys := []map[string]any{{"type": "text", "text": "system"}}
	msgs := []map[string]any{{"role": "user", "content": "hello"}}
	inv := pg.Check(sys, msgs)
	if inv != 0 {
		t.Errorf("first call should report 0 invalidations, got %d", inv)
	}
}

func TestPrefixGuard_StablePrefix(t *testing.T) {
	pg := NewPrefixGuard()
	sys := []map[string]any{{"type": "text", "text": "system"}}
	msgs := []map[string]any{{"role": "user", "content": "hello"}}
	pg.Check(sys, msgs)

	// Add a new message but keep prefix stable
	msgs2 := []map[string]any{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "hi"},
	}
	inv := pg.Check(sys, msgs2)
	if inv != 0 {
		t.Errorf("stable prefix should report 0 invalidations, got %d", inv)
	}
}

func TestPrefixGuard_SystemChanged(t *testing.T) {
	pg := NewPrefixGuard()
	sys1 := []map[string]any{{"type": "text", "text": "system v1"}}
	msgs := []map[string]any{{"role": "user", "content": "hello"}}
	pg.Check(sys1, msgs)

	sys2 := []map[string]any{{"type": "text", "text": "system v2"}}
	inv := pg.Check(sys2, msgs)
	if inv != 1 {
		t.Errorf("changed system should report 1 invalidation, got %d", inv)
	}
}

func TestPrefixGuard_MessageChanged(t *testing.T) {
	pg := NewPrefixGuard()
	sys := []map[string]any{{"type": "text", "text": "system"}}
	msgs1 := []map[string]any{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "hi"},
	}
	pg.Check(sys, msgs1)

	// Mutate first message
	msgs2 := []map[string]any{
		{"role": "user", "content": "CHANGED"},
		{"role": "assistant", "content": "hi"},
	}
	inv := pg.Check(sys, msgs2)
	if inv != 1 {
		t.Errorf("changed prefix message should report 1 invalidation, got %d", inv)
	}
}

func TestPrefixGuard_Reset(t *testing.T) {
	pg := NewPrefixGuard()
	sys := []map[string]any{{"type": "text", "text": "system"}}
	msgs := []map[string]any{{"role": "user", "content": "hello"}}
	pg.Check(sys, msgs)

	pg.Reset()

	sys2 := []map[string]any{{"type": "text", "text": "different"}}
	inv := pg.Check(sys2, msgs)
	if inv != 0 {
		t.Errorf("after reset, first call should report 0 invalidations, got %d", inv)
	}
}
