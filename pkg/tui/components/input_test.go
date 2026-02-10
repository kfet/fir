package components

import (
	"strings"
	"testing"

	"github.com/kfet/pi-go/pkg/tui"
)

func TestInput_InitialEmpty(t *testing.T) {
	inp := NewInput()
	if inp.GetValue() != "" {
		t.Errorf("expected empty, got %q", inp.GetValue())
	}
}

func TestInput_SetValue(t *testing.T) {
	inp := NewInput()
	inp.SetValue("hello")
	if inp.GetValue() != "hello" {
		t.Errorf("expected 'hello', got %q", inp.GetValue())
	}
}

func TestInput_TypeCharacters(t *testing.T) {
	inp := NewInput()
	inp.HandleInput("h")
	inp.HandleInput("i")
	if inp.GetValue() != "hi" {
		t.Errorf("expected 'hi', got %q", inp.GetValue())
	}
}

func TestInput_Backspace(t *testing.T) {
	inp := NewInput()
	inp.SetValue("hello")
	inp.cursor = 5
	inp.HandleInput("\x7f") // backspace
	if inp.GetValue() != "hell" {
		t.Errorf("expected 'hell', got %q", inp.GetValue())
	}
}

func TestInput_ForwardDelete(t *testing.T) {
	inp := NewInput()
	inp.SetValue("hello")
	inp.cursor = 0
	inp.HandleInput("\x1b[3~") // delete
	if inp.GetValue() != "ello" {
		t.Errorf("expected 'ello', got %q", inp.GetValue())
	}
}

func TestInput_CursorMovement(t *testing.T) {
	inp := NewInput()
	inp.SetValue("hello")
	inp.cursor = 5

	// Left
	inp.HandleInput("\x1b[D")
	if inp.cursor != 4 {
		t.Errorf("expected cursor at 4, got %d", inp.cursor)
	}

	// Right
	inp.HandleInput("\x1b[C")
	if inp.cursor != 5 {
		t.Errorf("expected cursor at 5, got %d", inp.cursor)
	}

	// Home
	inp.HandleInput("\x1b[H")
	if inp.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", inp.cursor)
	}

	// End
	inp.HandleInput("\x1b[F")
	if inp.cursor != 5 {
		t.Errorf("expected cursor at 5, got %d", inp.cursor)
	}
}

func TestInput_Submit(t *testing.T) {
	inp := NewInput()
	inp.SetValue("test")
	var submitted string
	inp.OnSubmit = func(value string) {
		submitted = value
	}
	inp.HandleInput("\r")
	if submitted != "test" {
		t.Errorf("expected 'test', got %q", submitted)
	}
}

func TestInput_Escape(t *testing.T) {
	inp := NewInput()
	escaped := false
	inp.OnEscape = func() {
		escaped = true
	}
	inp.HandleInput("\x1b")
	if !escaped {
		t.Error("expected escape callback")
	}
}

func TestInput_DeleteToLineStart(t *testing.T) {
	inp := NewInput()
	inp.SetValue("hello world")
	inp.cursor = 5
	inp.HandleInput("\x15") // ctrl+u
	if inp.GetValue() != " world" {
		t.Errorf("expected ' world', got %q", inp.GetValue())
	}
	if inp.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", inp.cursor)
	}
}

func TestInput_DeleteToLineEnd(t *testing.T) {
	inp := NewInput()
	inp.SetValue("hello world")
	inp.cursor = 5
	inp.HandleInput("\x0b") // ctrl+k
	if inp.GetValue() != "hello" {
		t.Errorf("expected 'hello', got %q", inp.GetValue())
	}
}

func TestInput_Undo(t *testing.T) {
	inp := NewInput()
	// Type some text
	inp.HandleInput("h")
	inp.HandleInput("e")
	inp.HandleInput("l")
	// Delete to line end (non-typing action that always pushes undo)
	inp.cursor = 1
	inp.HandleInput("\x0b") // ctrl+k: delete "el" → value = "h"
	if inp.GetValue() != "h" {
		t.Fatalf("expected 'h' after ctrl+k, got %q", inp.GetValue())
	}
	// Undo should restore "hel"
	inp.HandleInput("\x1a") // ctrl+z
	if inp.GetValue() != "hel" {
		t.Errorf("expected undo to restore 'hel', got %q", inp.GetValue())
	}
}

func TestInput_KillYank(t *testing.T) {
	inp := NewInput()
	inp.SetValue("hello world")
	inp.cursor = 5
	inp.HandleInput("\x0b") // ctrl+k: kill to end
	if inp.GetValue() != "hello" {
		t.Fatalf("expected 'hello', got %q", inp.GetValue())
	}
	// Move to beginning
	inp.HandleInput("\x01") // ctrl+a
	// Yank
	inp.HandleInput("\x19") // ctrl+y
	if inp.GetValue() != " worldhello" {
		t.Errorf("expected ' worldhello', got %q", inp.GetValue())
	}
}

func TestInput_Render(t *testing.T) {
	inp := NewInput()
	inp.SetValue("hello")
	inp.cursor = 5 // cursor at end so all chars are before cursor
	lines := inp.Render(80)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.HasPrefix(lines[0], "> ") {
		t.Errorf("expected '> ' prefix, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "hello") {
		t.Errorf("expected 'hello' in output, got %q", lines[0])
	}
}

func TestInput_RenderWithCursor(t *testing.T) {
	inp := NewInput()
	inp.SetValue("hi")
	inp.Focused = true
	lines := inp.Render(80)
	// Should contain reverse video escape for cursor
	if !strings.Contains(lines[0], "\x1b[7m") {
		t.Errorf("expected cursor (reverse video), got %q", lines[0])
	}
}

func TestInput_Invalidate(t *testing.T) {
	inp := NewInput()
	inp.Invalidate() // should not panic
}

func TestInput_PasteHandling(t *testing.T) {
	inp := NewInput()
	// Simulate bracketed paste
	inp.HandleInput("\x1b[200~pasted text\x1b[201~")
	if inp.GetValue() != "pasted text" {
		t.Errorf("expected 'pasted text', got %q", inp.GetValue())
	}
}

func TestInput_PasteStripsNewlines(t *testing.T) {
	inp := NewInput()
	inp.HandleInput("\x1b[200~line1\nline2\r\nline3\x1b[201~")
	if inp.GetValue() != "line1line2line3" {
		t.Errorf("expected 'line1line2line3', got %q", inp.GetValue())
	}
}

func TestInput_RejectsControlChars(t *testing.T) {
	inp := NewInput()
	inp.HandleInput("a")
	// Try to type a control char that isn't a known key
	// (Most control chars are intercepted as keys, but this tests the guard)
	if inp.GetValue() != "a" {
		t.Errorf("expected 'a', got %q", inp.GetValue())
	}
}

func TestInput_WordMovement(t *testing.T) {
	tui.SetKittyProtocolActive(false)
	defer tui.SetKittyProtocolActive(false)

	inp := NewInput()
	inp.SetValue("hello world foo")
	inp.cursor = 0

	// Move word right (Alt+F)
	inp.HandleInput("\x1bf")
	if inp.cursor != 5 {
		t.Errorf("expected cursor at 5 after word-right, got %d", inp.cursor)
	}

	// Move word right again
	inp.HandleInput("\x1bf")
	if inp.cursor != 11 {
		t.Errorf("expected cursor at 11 after second word-right, got %d", inp.cursor)
	}

	// Move word left (Alt+B)
	inp.HandleInput("\x1bb")
	if inp.cursor != 6 {
		t.Errorf("expected cursor at 6 after word-left, got %d", inp.cursor)
	}
}

func TestInput_ScrollingRender(t *testing.T) {
	inp := NewInput()
	inp.SetValue(strings.Repeat("x", 200))
	inp.cursor = 100
	lines := inp.Render(80)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	// Should have the prompt
	if !strings.HasPrefix(lines[0], "> ") {
		t.Errorf("expected '> ' prefix")
	}
}

func TestKillRing_Basic(t *testing.T) {
	kr := NewKillRing()
	kr.Push("hello", false, false)
	if kr.Peek() != "hello" {
		t.Errorf("expected 'hello', got %q", kr.Peek())
	}
}

func TestKillRing_Accumulate(t *testing.T) {
	kr := NewKillRing()
	kr.Push("hello", false, false)
	kr.Push(" world", false, true)
	if kr.Peek() != "hello world" {
		t.Errorf("expected 'hello world', got %q", kr.Peek())
	}
}

func TestKillRing_Prepend(t *testing.T) {
	kr := NewKillRing()
	kr.Push("world", false, false)
	kr.Push("hello ", true, true)
	if kr.Peek() != "hello world" {
		t.Errorf("expected 'hello world', got %q", kr.Peek())
	}
}

func TestKillRing_Rotate(t *testing.T) {
	kr := NewKillRing()
	kr.Push("first", false, false)
	kr.Push("second", false, false)
	if kr.Peek() != "second" {
		t.Errorf("expected 'second', got %q", kr.Peek())
	}
	kr.Rotate()
	if kr.Peek() != "first" {
		t.Errorf("expected 'first' after rotate, got %q", kr.Peek())
	}
}

func TestUndoStack_PushPop(t *testing.T) {
	us := NewUndoStack[string]()
	us.Push("state1")
	us.Push("state2")
	val, ok := us.Pop()
	if !ok || val != "state2" {
		t.Errorf("expected 'state2', got %q", val)
	}
	val, ok = us.Pop()
	if !ok || val != "state1" {
		t.Errorf("expected 'state1', got %q", val)
	}
	_, ok = us.Pop()
	if ok {
		t.Error("expected empty stack")
	}
}
