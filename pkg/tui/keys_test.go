package tui

import (
	"testing"
)

func TestMatchesKey_Escape(t *testing.T) {
	if !MatchesKey("\x1b", "escape") {
		t.Error("expected \\x1b to match escape")
	}
	if !MatchesKey("\x1b", "esc") {
		t.Error("expected \\x1b to match esc")
	}
}

func TestMatchesKey_Enter(t *testing.T) {
	if !MatchesKey("\r", "enter") {
		t.Error("expected \\r to match enter")
	}
	if !MatchesKey("\r", "return") {
		t.Error("expected \\r to match return")
	}
}

func TestMatchesKey_Tab(t *testing.T) {
	if !MatchesKey("\t", "tab") {
		t.Error("expected \\t to match tab")
	}
}

func TestMatchesKey_ShiftTab(t *testing.T) {
	if !MatchesKey("\x1b[Z", "shift+tab") {
		t.Error("expected shift+tab sequence")
	}
}

func TestMatchesKey_Space(t *testing.T) {
	if !MatchesKey(" ", "space") {
		t.Error("expected space")
	}
}

func TestMatchesKey_Backspace(t *testing.T) {
	if !MatchesKey("\x7f", "backspace") {
		t.Error("expected \\x7f to match backspace")
	}
	if !MatchesKey("\x08", "backspace") {
		t.Error("expected \\x08 to match backspace")
	}
}

func TestMatchesKey_CtrlC(t *testing.T) {
	if !MatchesKey("\x03", "ctrl+c") {
		t.Error("expected ctrl+c")
	}
}

func TestMatchesKey_CtrlZ(t *testing.T) {
	if !MatchesKey("\x1a", "ctrl+z") {
		t.Error("expected ctrl+z")
	}
}

func TestMatchesKey_CtrlA(t *testing.T) {
	if !MatchesKey("\x01", "ctrl+a") {
		t.Error("expected ctrl+a")
	}
}

func TestMatchesKey_Letter(t *testing.T) {
	if !MatchesKey("a", "a") {
		t.Error("expected 'a'")
	}
	if !MatchesKey("z", "z") {
		t.Error("expected 'z'")
	}
}

func TestMatchesKey_ArrowUp(t *testing.T) {
	if !MatchesKey("\x1b[A", "up") {
		t.Error("expected up arrow")
	}
	if !MatchesKey("\x1bOA", "up") {
		t.Error("expected up arrow (SS3)")
	}
}

func TestMatchesKey_ArrowDown(t *testing.T) {
	if !MatchesKey("\x1b[B", "down") {
		t.Error("expected down arrow")
	}
}

func TestMatchesKey_ArrowLeft(t *testing.T) {
	if !MatchesKey("\x1b[D", "left") {
		t.Error("expected left arrow")
	}
}

func TestMatchesKey_ArrowRight(t *testing.T) {
	if !MatchesKey("\x1b[C", "right") {
		t.Error("expected right arrow")
	}
}

func TestMatchesKey_Home(t *testing.T) {
	if !MatchesKey("\x1b[H", "home") {
		t.Error("expected home")
	}
	if !MatchesKey("\x1bOH", "home") {
		t.Error("expected home (SS3)")
	}
}

func TestMatchesKey_End(t *testing.T) {
	if !MatchesKey("\x1b[F", "end") {
		t.Error("expected end")
	}
}

func TestMatchesKey_Delete(t *testing.T) {
	if !MatchesKey("\x1b[3~", "delete") {
		t.Error("expected delete")
	}
}

func TestMatchesKey_PageUp(t *testing.T) {
	if !MatchesKey("\x1b[5~", "pageUp") {
		t.Error("expected pageUp")
	}
}

func TestMatchesKey_PageDown(t *testing.T) {
	if !MatchesKey("\x1b[6~", "pageDown") {
		t.Error("expected pageDown")
	}
}

func TestMatchesKey_Insert(t *testing.T) {
	if !MatchesKey("\x1b[2~", "insert") {
		t.Error("expected insert")
	}
}

func TestMatchesKey_F1(t *testing.T) {
	if !MatchesKey("\x1bOP", "f1") {
		t.Error("expected f1 (SS3)")
	}
	if !MatchesKey("\x1b[11~", "f1") {
		t.Error("expected f1 (CSI)")
	}
}

func TestMatchesKey_F12(t *testing.T) {
	if !MatchesKey("\x1b[24~", "f12") {
		t.Error("expected f12")
	}
}

func TestMatchesKey_ShiftUp(t *testing.T) {
	if !MatchesKey("\x1b[a", "shift+up") {
		t.Error("expected shift+up")
	}
}

func TestMatchesKey_CtrlRight(t *testing.T) {
	if !MatchesKey("\x1b[1;5C", "ctrl+right") {
		t.Error("expected ctrl+right")
	}
}

func TestMatchesKey_AltBackspace(t *testing.T) {
	if !MatchesKey("\x1b\x7f", "alt+backspace") {
		t.Error("expected alt+backspace")
	}
}

func TestMatchesKey_AltLeft_Legacy(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	if !MatchesKey("\x1bb", "alt+left") {
		t.Error("expected alt+left (\\x1bb)")
	}
}

func TestMatchesKey_AltRight_Legacy(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	if !MatchesKey("\x1bf", "alt+right") {
		t.Error("expected alt+right (\\x1bf)")
	}
}

func TestMatchesKey_CtrlSpace(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	if !MatchesKey("\x00", "ctrl+space") {
		t.Error("expected ctrl+space")
	}
}

func TestMatchesKey_AltEnter_Legacy(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	if !MatchesKey("\x1b\r", "alt+enter") {
		t.Error("expected alt+enter")
	}
}

func TestMatchesKey_ShiftEnter_Kitty(t *testing.T) {
	SetKittyProtocolActive(true)
	defer SetKittyProtocolActive(false)
	if !MatchesKey("\x1b\r", "shift+enter") {
		t.Error("expected shift+enter in Kitty mode")
	}
}

func TestMatchesKey_AltLetter_Legacy(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	if !MatchesKey("\x1bx", "alt+x") {
		t.Error("expected alt+x")
	}
}

func TestMatchesKey_KittyCSI_CtrlC(t *testing.T) {
	// Kitty: \x1b[99;5u = codepoint 99 ('c'), modifier 5 (ctrl=4+1)
	if !MatchesKey("\x1b[99;5u", "ctrl+c") {
		t.Error("expected Kitty ctrl+c")
	}
}

func TestMatchesKey_KittyCSI_Enter(t *testing.T) {
	// Kitty: \x1b[13u = Enter without modifier
	if !MatchesKey("\x1b[13u", "enter") {
		t.Error("expected Kitty enter")
	}
}

func TestMatchesKey_KittyCSI_ShiftEnter(t *testing.T) {
	// Kitty: \x1b[13;2u = Enter with shift
	if !MatchesKey("\x1b[13;2u", "shift+enter") {
		t.Error("expected Kitty shift+enter")
	}
}

// TestMatchesKey_CSITilde_ShiftEnter verifies that the CSI-tilde variant
// \x1b[13;2~ (used by some terminals) is correctly parsed via funcCodes[13]=cpEnter.
func TestMatchesKey_CSITilde_ShiftEnter(t *testing.T) {
	if !MatchesKey("\x1b[13;2~", "shift+enter") {
		t.Error("expected \\x1b[13;2~ to match shift+enter")
	}
	// Must not match alt+enter
	if MatchesKey("\x1b[13;2~", "alt+enter") {
		t.Error("\\x1b[13;2~ must not match alt+enter")
	}
}

func TestMatchesKey_ModifyOtherKeys_ShiftEnter(t *testing.T) {
	// modifyOtherKeys level 2: \x1b[27;2;13~ = Shift+Enter
	// This is the sequence sent by xterm/iTerm2 with \x1b[>4;2m enabled.
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	if !MatchesKey("\x1b[27;2;13~", "shift+enter") {
		t.Error("expected modifyOtherKeys shift+enter")
	}
}

func TestMatchesKey_ModifyOtherKeys_AltEnter(t *testing.T) {
	// modifyOtherKeys level 2: \x1b[27;3;13~ = Alt+Enter
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	if !MatchesKey("\x1b[27;3;13~", "alt+enter") {
		t.Error("expected modifyOtherKeys alt+enter")
	}
}

// TestMatchesKey_ModifyOtherKeys_ShiftEnter_NotAltEnter ensures that with
// modifyOtherKeys the Shift+Enter sequence is NOT treated as alt+enter (which
// would trigger ActionFollowUp instead of newline insertion).
func TestMatchesKey_ModifyOtherKeys_ShiftEnter_NotAltEnter(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	if MatchesKey("\x1b[27;2;13~", "alt+enter") {
		t.Error("shift+enter modifyOtherKeys sequence must NOT match alt+enter")
	}
}

// TestMatchesKey_ModifyOtherKeys_CtrlC verifies that tmux's xterm extended-key
// format (\x1b[27;5;99~) for Ctrl+C is recognised correctly.
func TestMatchesKey_ModifyOtherKeys_CtrlC(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	if !MatchesKey("\x1b[27;5;99~", "ctrl+c") {
		t.Error("expected \\x1b[27;5;99~ to match ctrl+c (tmux modifyOtherKeys format)")
	}
	// Must not fire as an unrelated key
	if MatchesKey("\x1b[27;5;99~", "ctrl+d") {
		t.Error("ctrl+c sequence must not match ctrl+d")
	}
}

// TestMatchesKey_ModifyOtherKeys_CtrlD verifies that tmux's xterm extended-key
// format (\x1b[27;5;100~) for Ctrl+D is recognised correctly.
func TestMatchesKey_ModifyOtherKeys_CtrlD(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	if !MatchesKey("\x1b[27;5;100~", "ctrl+d") {
		t.Error("expected \\x1b[27;5;100~ to match ctrl+d (tmux modifyOtherKeys format)")
	}
}

// TestMatchesKey_ModifyOtherKeys_CtrlShift verifies ctrl+shift combinations
// in modifyOtherKeys format.
func TestMatchesKey_ModifyOtherKeys_CtrlShift(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	// modifier = shift(1) + ctrl(4) + 1 = 6 → \x1b[27;6;99~
	if !MatchesKey("\x1b[27;6;99~", "ctrl+shift+c") {
		t.Error("expected \\x1b[27;6;99~ to match ctrl+shift+c")
	}
}

func TestMatchesKey_KittyArrow(t *testing.T) {
	// \x1b[1;5A = ctrl+up
	if !MatchesKey("\x1b[1;5A", "ctrl+up") {
		t.Error("expected Kitty ctrl+up")
	}
}

func TestMatchesKey_KittyDelete(t *testing.T) {
	// \x1b[3;2~ = shift+delete
	if !MatchesKey("\x1b[3;2~", "shift+delete") {
		t.Error("expected Kitty shift+delete")
	}
}

func TestMatchesKey_NoMatch(t *testing.T) {
	if MatchesKey("x", "ctrl+c") {
		t.Error("should not match")
	}
}

func TestMatchesKey_CtrlDash(t *testing.T) {
	// ctrl+- sends \x1f
	if !MatchesKey("\x1f", "ctrl+-") {
		t.Error("expected ctrl+-")
	}
}

func TestMatchesKey_CtrlBackslash(t *testing.T) {
	if !MatchesKey("\x1c", "ctrl+\\") {
		t.Error("expected ctrl+\\")
	}
}

// parseKey tests
func TestParseKey_Escape(t *testing.T) {
	if ParseKey("\x1b") != "escape" {
		t.Errorf("got %q", ParseKey("\x1b"))
	}
}

func TestParseKey_Enter(t *testing.T) {
	if ParseKey("\r") != "enter" {
		t.Errorf("got %q", ParseKey("\r"))
	}
}

func TestParseKey_Tab(t *testing.T) {
	if ParseKey("\t") != "tab" {
		t.Errorf("got %q", ParseKey("\t"))
	}
}

func TestParseKey_CtrlC(t *testing.T) {
	if ParseKey("\x03") != "ctrl+c" {
		t.Errorf("got %q", ParseKey("\x03"))
	}
}

func TestParseKey_Space(t *testing.T) {
	if ParseKey(" ") != "space" {
		t.Errorf("got %q", ParseKey(" "))
	}
}

func TestParseKey_Backspace(t *testing.T) {
	if ParseKey("\x7f") != "backspace" {
		t.Errorf("got %q", ParseKey("\x7f"))
	}
}

func TestParseKey_Letter(t *testing.T) {
	if ParseKey("a") != "a" {
		t.Errorf("got %q", ParseKey("a"))
	}
}

func TestParseKey_ArrowUp(t *testing.T) {
	if ParseKey("\x1b[A") != "up" {
		t.Errorf("got %q", ParseKey("\x1b[A"))
	}
}

func TestParseKey_Home(t *testing.T) {
	if ParseKey("\x1b[H") != "home" {
		t.Errorf("got %q", ParseKey("\x1b[H"))
	}
}

func TestParseKey_Delete(t *testing.T) {
	if ParseKey("\x1b[3~") != "delete" {
		t.Errorf("got %q", ParseKey("\x1b[3~"))
	}
}

func TestParseKey_F1(t *testing.T) {
	if ParseKey("\x1bOP") != "f1" {
		t.Errorf("got %q", ParseKey("\x1bOP"))
	}
}

func TestParseKey_ShiftTab(t *testing.T) {
	if ParseKey("\x1b[Z") != "shift+tab" {
		t.Errorf("got %q", ParseKey("\x1b[Z"))
	}
}

func TestParseKey_KittyCtrlC(t *testing.T) {
	result := ParseKey("\x1b[99;5u")
	if result != "ctrl+c" {
		t.Errorf("expected ctrl+c, got %q", result)
	}
}

func TestParseKey_KittyEnter(t *testing.T) {
	result := ParseKey("\x1b[13u")
	if result != "enter" {
		t.Errorf("expected enter, got %q", result)
	}
}

func TestParseKey_AltEnter_Legacy(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	result := ParseKey("\x1b\r")
	if result != "alt+enter" {
		t.Errorf("expected alt+enter, got %q", result)
	}
}

func TestParseKey_ShiftEnter_Kitty(t *testing.T) {
	SetKittyProtocolActive(true)
	defer SetKittyProtocolActive(false)
	result := ParseKey("\x1b\r")
	if result != "shift+enter" {
		t.Errorf("expected shift+enter, got %q", result)
	}
}

func TestParseKey_AltLetter(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	result := ParseKey("\x1bx")
	if result != "alt+x" {
		t.Errorf("expected alt+x, got %q", result)
	}
}

func TestParseKey_CtrlAltLetter(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	result := ParseKey("\x1b\x03") // ESC + ctrl+c
	if result != "ctrl+alt+c" {
		t.Errorf("expected ctrl+alt+c, got %q", result)
	}
}

func TestParseKey_Unknown(t *testing.T) {
	result := ParseKey("\x1b[999Z")
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestIsKeyRelease(t *testing.T) {
	if !IsKeyRelease("\x1b[99;5:3u") {
		t.Error("expected key release")
	}
	if IsKeyRelease("\x1b[99;5u") {
		t.Error("should not be release")
	}
	if IsKeyRelease("\x1b[200~some:3upasted") {
		t.Error("paste should not be release")
	}
}

func TestIsKeyRepeat(t *testing.T) {
	if !IsKeyRepeat("\x1b[99;5:2u") {
		t.Error("expected key repeat")
	}
	if IsKeyRepeat("\x1b[99;5u") {
		t.Error("should not be repeat")
	}
}

func TestSetKittyProtocolActive(t *testing.T) {
	SetKittyProtocolActive(true)
	if !IsKittyProtocolActive() {
		t.Error("expected active")
	}
	SetKittyProtocolActive(false)
	if IsKittyProtocolActive() {
		t.Error("expected inactive")
	}
}

func TestMatchesKey_CtrlBracket(t *testing.T) {
	// ctrl+[ sends ESC (0x1b) — rawCtrlChar('[') = 0x1b
	if !MatchesKey("\x1b", "ctrl+[") {
		t.Error("expected \\x1b to match ctrl+[")
	}
}

func TestMatchesKey_ShiftLetter(t *testing.T) {
	if !MatchesKey("A", "shift+a") {
		t.Error("expected shift+a = 'A'")
	}
}

func TestMatchesKey_CtrlAltLetter_Legacy(t *testing.T) {
	SetKittyProtocolActive(false)
	defer SetKittyProtocolActive(false)
	// ctrl+alt+c = ESC + ctrl+c = \x1b\x03
	if !MatchesKey("\x1b\x03", "ctrl+alt+c") {
		t.Error("expected ctrl+alt+c")
	}
}

func TestMatchesKey_KittyHomeEnd(t *testing.T) {
	// \x1b[1;5H = ctrl+home
	if !MatchesKey("\x1b[1;5H", "ctrl+home") {
		t.Error("expected Kitty ctrl+home")
	}
	// \x1b[1;5F = ctrl+end
	if !MatchesKey("\x1b[1;5F", "ctrl+end") {
		t.Error("expected Kitty ctrl+end")
	}
}

func TestParseKey_KittyArrowCtrl(t *testing.T) {
	result := ParseKey("\x1b[1;5A")
	if result != "ctrl+up" {
		t.Errorf("expected ctrl+up, got %q", result)
	}
}

func TestParseKey_KittyHomeEnd(t *testing.T) {
	result := ParseKey("\x1b[1;2H")
	if result != "shift+home" {
		t.Errorf("expected shift+home, got %q", result)
	}
	result = ParseKey("\x1b[1;2F")
	if result != "shift+end" {
		t.Errorf("expected shift+end, got %q", result)
	}
}

func TestSplitKeySequences_Empty(t *testing.T) {
	if seqs := SplitKeySequences(""); len(seqs) != 0 {
		t.Errorf("expected nil, got %v", seqs)
	}
}

func TestSplitKeySequences_SingleByte(t *testing.T) {
	seqs := SplitKeySequences("\x7f")
	if len(seqs) != 1 || seqs[0] != "\x7f" {
		t.Errorf("expected [\\x7f], got %v", seqs)
	}
}

func TestSplitKeySequences_MultipleBackspaces(t *testing.T) {
	// Three backspaces buffered during a slow render — must NOT be dropped.
	seqs := SplitKeySequences("\x7f\x7f\x7f")
	if len(seqs) != 3 {
		t.Fatalf("expected 3 sequences, got %d: %v", len(seqs), seqs)
	}
	for i, s := range seqs {
		if s != "\x7f" {
			t.Errorf("seq[%d]: expected \\x7f, got %q", i, s)
		}
	}
}

func TestSplitKeySequences_MixedControlAndPrintable(t *testing.T) {
	// ctrl+a followed by 'b'
	seqs := SplitKeySequences("\x01b")
	if len(seqs) != 2 || seqs[0] != "\x01" || seqs[1] != "b" {
		t.Errorf("expected [\\x01, b], got %v", seqs)
	}
}

func TestSplitKeySequences_CSIArrow(t *testing.T) {
	seqs := SplitKeySequences("\x1b[A")
	if len(seqs) != 1 || seqs[0] != "\x1b[A" {
		t.Errorf("expected [\\x1b[A], got %v", seqs)
	}
}

func TestSplitKeySequences_TwoArrows(t *testing.T) {
	seqs := SplitKeySequences("\x1b[A\x1b[B")
	if len(seqs) != 2 || seqs[0] != "\x1b[A" || seqs[1] != "\x1b[B" {
		t.Errorf("expected [up, down], got %v", seqs)
	}
}

func TestSplitKeySequences_AltBackspace(t *testing.T) {
	// alt+backspace = ESC + 0x7f
	seqs := SplitKeySequences("\x1b\x7f")
	if len(seqs) != 1 || seqs[0] != "\x1b\x7f" {
		t.Errorf("expected [\\x1b\\x7f], got %v", seqs)
	}
}

func TestSplitKeySequences_BackspaceAfterArrow(t *testing.T) {
	// up-arrow then backspace
	seqs := SplitKeySequences("\x1b[A\x7f")
	if len(seqs) != 2 || seqs[0] != "\x1b[A" || seqs[1] != "\x7f" {
		t.Errorf("expected [up, backspace], got %v", seqs)
	}
}

func TestSplitKeySequences_BracketedPaste(t *testing.T) {
	paste := "\x1b[200~hello\x1b[201~"
	seqs := SplitKeySequences(paste)
	if len(seqs) != 1 || seqs[0] != paste {
		t.Errorf("expected paste as single seq, got %v", seqs)
	}
}

func TestSplitKeySequences_PasteThenBackspace(t *testing.T) {
	data := "\x1b[200~hi\x1b[201~\x7f"
	seqs := SplitKeySequences(data)
	if len(seqs) != 2 {
		t.Fatalf("expected 2 seqs, got %d: %v", len(seqs), seqs)
	}
	if seqs[0] != "\x1b[200~hi\x1b[201~" {
		t.Errorf("seq[0]: expected paste, got %q", seqs[0])
	}
	if seqs[1] != "\x7f" {
		t.Errorf("seq[1]: expected backspace, got %q", seqs[1])
	}
}

func TestSplitKeySequences_MultibyteUTF8(t *testing.T) {
	// Two emoji characters buffered together
	seqs := SplitKeySequences("😀😀")
	if len(seqs) != 2 {
		t.Fatalf("expected 2 seqs, got %d: %v", len(seqs), seqs)
	}
	for i, s := range seqs {
		if s != "😀" {
			t.Errorf("seq[%d]: expected 😀, got %q", i, s)
		}
	}
}

func TestSplitKeySequences_KittySequence(t *testing.T) {
	// Kitty protocol: \x1b[127;1u (backspace) — two such seqs buffered
	seqs := SplitKeySequences("\x1b[127;1u\x1b[127;1u")
	if len(seqs) != 2 {
		t.Fatalf("expected 2 seqs, got %d: %v", len(seqs), seqs)
	}
	for i, s := range seqs {
		if s != "\x1b[127;1u" {
			t.Errorf("seq[%d]: expected kitty backspace, got %q", i, s)
		}
	}
}
