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

// The tail breakpoint moves every turn, and the side-query path retires an
// old anchor as it places a new one. cache_control is request metadata, not
// cached content — the guard must not report those moves as prefix drift, or
// its -vv trace is noise on every single turn.
func TestPrefixGuard_IgnoresMovingCacheControl(t *testing.T) {
	pg := NewPrefixGuard()

	system := []map[string]any{{"type": "text", "text": "sys"}}
	msg := func(text string, cached bool) map[string]any {
		block := map[string]any{"type": "text", "text": text}
		if cached {
			block["cache_control"] = map[string]any{"type": "ephemeral", "ttl": "1h"}
		}
		return map[string]any{"role": "user", "content": []map[string]any{block}}
	}

	// Turn 1: breakpoint on the last message.
	pg.Check(system, []map[string]any{msg("a", false), msg("b", true)})

	// Turn 2: same prefix, but the breakpoint has moved off "b" onto "c".
	// Only the two genuinely-new slots exist; nothing in the prefix changed.
	if n := pg.Check(system, []map[string]any{msg("a", false), msg("b", false), msg("c", true)}); n != 0 {
		t.Errorf("moving cache_control reported %d invalidations, want 0", n)
	}

	// A real content change is still caught.
	if n := pg.Check(system, []map[string]any{msg("a", false), msg("CHANGED", false), msg("c", true)}); n != 1 {
		t.Errorf("real content change reported %d invalidations, want 1", n)
	}
}

// The same de-noising applies to the system block, which always carries a
// breakpoint and would otherwise flip hash whenever retention changes.
func TestPrefixGuard_IgnoresSystemCacheControl(t *testing.T) {
	pg := NewPrefixGuard()
	short := []map[string]any{{
		"type": "text", "text": "sys",
		"cache_control": map[string]any{"type": "ephemeral"},
	}}
	long := []map[string]any{{
		"type": "text", "text": "sys",
		"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"},
	}}
	pg.Check(short, nil)
	if n := pg.Check(long, nil); n != 0 {
		t.Errorf("retention change on system block reported %d invalidations, want 0", n)
	}
	if n := pg.Check([]map[string]any{{"type": "text", "text": "DIFFERENT"}}, nil); n != 1 {
		t.Errorf("real system change reported %d invalidations, want 1", n)
	}
}
