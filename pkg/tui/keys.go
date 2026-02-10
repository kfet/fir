// Ported from: packages/tui/src/keys.ts
// Upstream hash: 1caadb2e
package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// KeyID is a typed key identifier string (e.g. "ctrl+c", "escape", "shift+enter").
type KeyID = string

// Key helpers for constructing typed key identifiers.
var Key = struct {
	Escape    KeyID
	Esc       KeyID
	Enter     KeyID
	Return    KeyID
	Tab       KeyID
	Space     KeyID
	Backspace KeyID
	Delete    KeyID
	Insert    KeyID
	Clear     KeyID
	Home      KeyID
	End       KeyID
	PageUp    KeyID
	PageDown  KeyID
	Up        KeyID
	Down      KeyID
	Left      KeyID
	Right     KeyID
	F1        KeyID
	F2        KeyID
	F3        KeyID
	F4        KeyID
	F5        KeyID
	F6        KeyID
	F7        KeyID
	F8        KeyID
	F9        KeyID
	F10       KeyID
	F11       KeyID
	F12       KeyID
}{
	Escape: "escape", Esc: "esc", Enter: "enter", Return: "return",
	Tab: "tab", Space: "space", Backspace: "backspace", Delete: "delete",
	Insert: "insert", Clear: "clear", Home: "home", End: "end",
	PageUp: "pageUp", PageDown: "pageDown",
	Up: "up", Down: "down", Left: "left", Right: "right",
	F1: "f1", F2: "f2", F3: "f3", F4: "f4", F5: "f5", F6: "f6",
	F7: "f7", F8: "f8", F9: "f9", F10: "f10", F11: "f11", F12: "f12",
}

// KeyCtrl returns "ctrl+<key>".
func KeyCtrl(key string) KeyID { return "ctrl+" + key }

// KeyShift returns "shift+<key>".
func KeyShift(key string) KeyID { return "shift+" + key }

// KeyAlt returns "alt+<key>".
func KeyAlt(key string) KeyID { return "alt+" + key }

// KeyCtrlShift returns "ctrl+shift+<key>".
func KeyCtrlShift(key string) KeyID { return "ctrl+shift+" + key }

// KeyCtrlAlt returns "ctrl+alt+<key>".
func KeyCtrlAlt(key string) KeyID { return "ctrl+alt+" + key }

// KeyShiftAlt returns "shift+alt+<key>".
func KeyShiftAlt(key string) KeyID { return "shift+alt+" + key }

// KeyCtrlShiftAlt returns "ctrl+shift+alt+<key>".
func KeyCtrlShiftAlt(key string) KeyID { return "ctrl+shift+alt+" + key }

// =============================================================================
// Global Kitty Protocol State
// =============================================================================

var (
	kittyMu     sync.Mutex
	kittyActive bool
)

// SetKittyProtocolActive sets the global Kitty keyboard protocol state.
func SetKittyProtocolActive(active bool) {
	kittyMu.Lock()
	defer kittyMu.Unlock()
	kittyActive = active
}

// IsKittyProtocolActive returns whether Kitty keyboard protocol is active.
func IsKittyProtocolActive() bool {
	kittyMu.Lock()
	defer kittyMu.Unlock()
	return kittyActive
}

// =============================================================================
// Constants
// =============================================================================

var symbolKeys = map[byte]bool{
	'`': true, '-': true, '=': true, '[': true, ']': true, '\\': true,
	';': true, '\'': true, ',': true, '.': true, '/': true,
	'!': true, '@': true, '#': true, '$': true, '%': true, '^': true,
	'&': true, '*': true, '(': true, ')': true, '_': true, '+': true,
	'|': true, '~': true, '{': true, '}': true, ':': true,
	'<': true, '>': true, '?': true,
}

const (
	modShift = 1
	modAlt   = 2
	modCtrl  = 4
	lockMask = 64 + 128
)

const (
	cpEscape    = 27
	cpTab       = 9
	cpEnter     = 13
	cpSpace     = 32
	cpBackspace = 127
	cpKpEnter   = 57414
)

const (
	cpUp    = -1
	cpDown  = -2
	cpRight = -3
	cpLeft  = -4
)

const (
	cpDelete   = -10
	cpInsert   = -11
	cpPageUp   = -12
	cpPageDown = -13
	cpHome     = -14
	cpEnd      = -15
)

// KeyEventType represents key press/repeat/release.
type KeyEventType string

const (
	KeyPress   KeyEventType = "press"
	KeyRepeat  KeyEventType = "repeat"
	KeyRelease KeyEventType = "release"
)

// Legacy sequence maps
var legacyKeySequences = map[string][]string{
	"up":       {"\x1b[A", "\x1bOA"},
	"down":     {"\x1b[B", "\x1bOB"},
	"right":    {"\x1b[C", "\x1bOC"},
	"left":     {"\x1b[D", "\x1bOD"},
	"home":     {"\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~"},
	"end":      {"\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~"},
	"insert":   {"\x1b[2~"},
	"delete":   {"\x1b[3~"},
	"pageUp":   {"\x1b[5~", "\x1b[[5~"},
	"pageDown": {"\x1b[6~", "\x1b[[6~"},
	"clear":    {"\x1b[E", "\x1bOE"},
	"f1":       {"\x1bOP", "\x1b[11~", "\x1b[[A"},
	"f2":       {"\x1bOQ", "\x1b[12~", "\x1b[[B"},
	"f3":       {"\x1bOR", "\x1b[13~", "\x1b[[C"},
	"f4":       {"\x1bOS", "\x1b[14~", "\x1b[[D"},
	"f5":       {"\x1b[15~", "\x1b[[E"},
	"f6":       {"\x1b[17~"},
	"f7":       {"\x1b[18~"},
	"f8":       {"\x1b[19~"},
	"f9":       {"\x1b[20~"},
	"f10":      {"\x1b[21~"},
	"f11":      {"\x1b[23~"},
	"f12":      {"\x1b[24~"},
}

var legacyShiftSequences = map[string][]string{
	"up":       {"\x1b[a"},
	"down":     {"\x1b[b"},
	"right":    {"\x1b[c"},
	"left":     {"\x1b[d"},
	"clear":    {"\x1b[e"},
	"insert":   {"\x1b[2$"},
	"delete":   {"\x1b[3$"},
	"pageUp":   {"\x1b[5$"},
	"pageDown": {"\x1b[6$"},
	"home":     {"\x1b[7$"},
	"end":      {"\x1b[8$"},
}

var legacyCtrlSequences = map[string][]string{
	"up":       {"\x1bOa"},
	"down":     {"\x1bOb"},
	"right":    {"\x1bOc"},
	"left":     {"\x1bOd"},
	"clear":    {"\x1bOe"},
	"insert":   {"\x1b[2^"},
	"delete":   {"\x1b[3^"},
	"pageUp":   {"\x1b[5^"},
	"pageDown": {"\x1b[6^"},
	"home":     {"\x1b[7^"},
	"end":      {"\x1b[8^"},
}

var legacySequenceKeyIDs = map[string]string{
	"\x1bOA":   "up",
	"\x1bOB":   "down",
	"\x1bOC":   "right",
	"\x1bOD":   "left",
	"\x1bOH":   "home",
	"\x1bOF":   "end",
	"\x1b[E":   "clear",
	"\x1bOE":   "clear",
	"\x1bOe":   "ctrl+clear",
	"\x1b[e":   "shift+clear",
	"\x1b[2~":  "insert",
	"\x1b[2$":  "shift+insert",
	"\x1b[2^":  "ctrl+insert",
	"\x1b[3$":  "shift+delete",
	"\x1b[3^":  "ctrl+delete",
	"\x1b[[5~": "pageUp",
	"\x1b[[6~": "pageDown",
	"\x1b[a":   "shift+up",
	"\x1b[b":   "shift+down",
	"\x1b[c":   "shift+right",
	"\x1b[d":   "shift+left",
	"\x1bOa":   "ctrl+up",
	"\x1bOb":   "ctrl+down",
	"\x1bOc":   "ctrl+right",
	"\x1bOd":   "ctrl+left",
	"\x1b[5$":  "shift+pageUp",
	"\x1b[6$":  "shift+pageDown",
	"\x1b[7$":  "shift+home",
	"\x1b[8$":  "shift+end",
	"\x1b[5^":  "ctrl+pageUp",
	"\x1b[6^":  "ctrl+pageDown",
	"\x1b[7^":  "ctrl+home",
	"\x1b[8^":  "ctrl+end",
	"\x1bOP":   "f1",
	"\x1bOQ":   "f2",
	"\x1bOR":   "f3",
	"\x1bOS":   "f4",
	"\x1b[11~": "f1",
	"\x1b[12~": "f2",
	"\x1b[13~": "f3",
	"\x1b[14~": "f4",
	"\x1b[[A":  "f1",
	"\x1b[[B":  "f2",
	"\x1b[[C":  "f3",
	"\x1b[[D":  "f4",
	"\x1b[[E":  "f5",
	"\x1b[15~": "f5",
	"\x1b[17~": "f6",
	"\x1b[18~": "f7",
	"\x1b[19~": "f8",
	"\x1b[20~": "f9",
	"\x1b[21~": "f10",
	"\x1b[23~": "f11",
	"\x1b[24~": "f12",
	"\x1bb":    "alt+left",
	"\x1bf":    "alt+right",
	"\x1bp":    "alt+up",
	"\x1bn":    "alt+down",
}

// =============================================================================
// Kitty Protocol Parsing
// =============================================================================

type parsedKittySequence struct {
	codepoint    int
	shiftedKey   *int
	baseLayoutKey *int
	modifier     int
	eventType    KeyEventType
}

var (
	csiURe      = regexp.MustCompile(`^\x1b\[(\d+)(?::(\d*))?(?::(\d+))?(?:;(\d+))?(?::(\d+))?u$`)
	arrowRe     = regexp.MustCompile(`^\x1b\[1;(\d+)(?::(\d+))?([ABCD])$`)
	funcRe      = regexp.MustCompile(`^\x1b\[(\d+)(?:;(\d+))?(?::(\d+))?~$`)
	homeEndRe   = regexp.MustCompile(`^\x1b\[1;(\d+)(?::(\d+))?([HF])$`)
	modOtherRe  = regexp.MustCompile(`^\x1b\[27;(\d+);(\d+)~$`)
)

// IsKeyRelease checks if input data is a key release event.
func IsKeyRelease(data string) bool {
	if strings.Contains(data, "\x1b[200~") {
		return false
	}
	for _, suffix := range []string{":3u", ":3~", ":3A", ":3B", ":3C", ":3D", ":3H", ":3F"} {
		if strings.Contains(data, suffix) {
			return true
		}
	}
	return false
}

// IsKeyRepeat checks if input data is a key repeat event.
func IsKeyRepeat(data string) bool {
	if strings.Contains(data, "\x1b[200~") {
		return false
	}
	for _, suffix := range []string{":2u", ":2~", ":2A", ":2B", ":2C", ":2D", ":2H", ":2F"} {
		if strings.Contains(data, suffix) {
			return true
		}
	}
	return false
}

func parseEventType(s string) KeyEventType {
	if s == "" {
		return KeyPress
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return KeyPress
	}
	switch n {
	case 2:
		return KeyRepeat
	case 3:
		return KeyRelease
	}
	return KeyPress
}

func parseKittySequence(data string) *parsedKittySequence {
	// CSI u format
	if m := csiURe.FindStringSubmatch(data); m != nil {
		cp, _ := strconv.Atoi(m[1])
		var shiftedKey, baseLayoutKey *int
		if m[2] != "" {
			v, _ := strconv.Atoi(m[2])
			shiftedKey = &v
		}
		if m[3] != "" {
			v, _ := strconv.Atoi(m[3])
			baseLayoutKey = &v
		}
		modVal := 1
		if m[4] != "" {
			modVal, _ = strconv.Atoi(m[4])
		}
		et := parseEventType(m[5])
		return &parsedKittySequence{codepoint: cp, shiftedKey: shiftedKey, baseLayoutKey: baseLayoutKey, modifier: modVal - 1, eventType: et}
	}

	// Arrow keys with modifier
	if m := arrowRe.FindStringSubmatch(data); m != nil {
		modVal, _ := strconv.Atoi(m[1])
		et := parseEventType(m[2])
		arrowCodes := map[byte]int{'A': cpUp, 'B': cpDown, 'C': cpRight, 'D': cpLeft}
		return &parsedKittySequence{codepoint: arrowCodes[m[3][0]], modifier: modVal - 1, eventType: et}
	}

	// Functional keys
	if m := funcRe.FindStringSubmatch(data); m != nil {
		keyNum, _ := strconv.Atoi(m[1])
		modVal := 1
		if m[2] != "" {
			modVal, _ = strconv.Atoi(m[2])
		}
		et := parseEventType(m[3])
		funcCodes := map[int]int{2: cpInsert, 3: cpDelete, 5: cpPageUp, 6: cpPageDown, 7: cpHome, 8: cpEnd}
		if cp, ok := funcCodes[keyNum]; ok {
			return &parsedKittySequence{codepoint: cp, modifier: modVal - 1, eventType: et}
		}
	}

	// Home/End with modifier
	if m := homeEndRe.FindStringSubmatch(data); m != nil {
		modVal, _ := strconv.Atoi(m[1])
		et := parseEventType(m[2])
		cp := cpHome
		if m[3] == "F" {
			cp = cpEnd
		}
		return &parsedKittySequence{codepoint: cp, modifier: modVal - 1, eventType: et}
	}

	return nil
}

func matchesKittySequence(data string, expectedCP, expectedMod int) bool {
	parsed := parseKittySequence(data)
	if parsed == nil {
		return false
	}
	actualMod := parsed.modifier & ^lockMask
	expectedModClean := expectedMod & ^lockMask
	if actualMod != expectedModClean {
		return false
	}
	if parsed.codepoint == expectedCP {
		return true
	}
	// Alternate: base layout key for non-Latin keyboards
	if parsed.baseLayoutKey != nil && *parsed.baseLayoutKey == expectedCP {
		cp := parsed.codepoint
		isLatinLetter := cp >= 97 && cp <= 122
		isKnownSymbol := cp >= 0 && cp < 128 && symbolKeys[byte(cp)]
		if !isLatinLetter && !isKnownSymbol {
			return true
		}
	}
	return false
}

func matchesModifyOtherKeys(data string, expectedKeycode, expectedMod int) bool {
	m := modOtherRe.FindStringSubmatch(data)
	if m == nil {
		return false
	}
	modVal, _ := strconv.Atoi(m[1])
	keycode, _ := strconv.Atoi(m[2])
	return keycode == expectedKeycode && (modVal-1) == expectedMod
}

func matchesLegacySequence(data string, sequences []string) bool {
	for _, s := range sequences {
		if data == s {
			return true
		}
	}
	return false
}

func matchesLegacyModifierSequence(data, key string, modifier int) bool {
	if modifier == modShift {
		if seqs, ok := legacyShiftSequences[key]; ok {
			return matchesLegacySequence(data, seqs)
		}
	}
	if modifier == modCtrl {
		if seqs, ok := legacyCtrlSequences[key]; ok {
			return matchesLegacySequence(data, seqs)
		}
	}
	return false
}

func rawCtrlChar(key string) string {
	ch := strings.ToLower(key)
	if len(ch) != 1 {
		return ""
	}
	code := ch[0]
	if (code >= 'a' && code <= 'z') || code == '[' || code == '\\' || code == ']' || code == '_' {
		return string(rune(code & 0x1f))
	}
	if code == '-' {
		return string(rune(31))
	}
	return ""
}

type parsedKeyID struct {
	key   string
	ctrl  bool
	shift bool
	alt   bool
}

func parseKeyID(keyID string) *parsedKeyID {
	parts := strings.Split(strings.ToLower(keyID), "+")
	key := parts[len(parts)-1]
	if key == "" {
		return nil
	}
	p := &parsedKeyID{key: key}
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "ctrl":
			p.ctrl = true
		case "shift":
			p.shift = true
		case "alt":
			p.alt = true
		}
	}
	return p
}

// MatchesKey checks if terminal input data matches a key identifier.
func MatchesKey(data string, keyID KeyID) bool {
	parsed := parseKeyID(keyID)
	if parsed == nil {
		return false
	}

	key := parsed.key
	ctrl := parsed.ctrl
	shift := parsed.shift
	alt := parsed.alt
	kitty := IsKittyProtocolActive()

	modifier := 0
	if shift {
		modifier |= modShift
	}
	if alt {
		modifier |= modAlt
	}
	if ctrl {
		modifier |= modCtrl
	}

	switch key {
	case "escape", "esc":
		if modifier != 0 {
			return false
		}
		return data == "\x1b" || matchesKittySequence(data, cpEscape, 0)

	case "space":
		if !kitty {
			if ctrl && !alt && !shift && data == "\x00" {
				return true
			}
			if alt && !ctrl && !shift && data == "\x1b " {
				return true
			}
		}
		if modifier == 0 {
			return data == " " || matchesKittySequence(data, cpSpace, 0)
		}
		return matchesKittySequence(data, cpSpace, modifier)

	case "tab":
		if shift && !ctrl && !alt {
			return data == "\x1b[Z" || matchesKittySequence(data, cpTab, modShift)
		}
		if modifier == 0 {
			return data == "\t" || matchesKittySequence(data, cpTab, 0)
		}
		return matchesKittySequence(data, cpTab, modifier)

	case "enter", "return":
		if shift && !ctrl && !alt {
			if matchesKittySequence(data, cpEnter, modShift) || matchesKittySequence(data, cpKpEnter, modShift) {
				return true
			}
			if matchesModifyOtherKeys(data, cpEnter, modShift) {
				return true
			}
			if kitty {
				return data == "\x1b\r" || data == "\n"
			}
			return false
		}
		if alt && !ctrl && !shift {
			if matchesKittySequence(data, cpEnter, modAlt) || matchesKittySequence(data, cpKpEnter, modAlt) {
				return true
			}
			if matchesModifyOtherKeys(data, cpEnter, modAlt) {
				return true
			}
			if !kitty {
				return data == "\x1b\r"
			}
			return false
		}
		if modifier == 0 {
			return data == "\r" ||
				(!kitty && data == "\n") ||
				data == "\x1bOM" ||
				matchesKittySequence(data, cpEnter, 0) ||
				matchesKittySequence(data, cpKpEnter, 0)
		}
		return matchesKittySequence(data, cpEnter, modifier) || matchesKittySequence(data, cpKpEnter, modifier)

	case "backspace":
		if alt && !ctrl && !shift {
			if data == "\x1b\x7f" || data == "\x1b\x08" {
				return true
			}
			return matchesKittySequence(data, cpBackspace, modAlt)
		}
		if modifier == 0 {
			return data == "\x7f" || data == "\x08" || matchesKittySequence(data, cpBackspace, 0)
		}
		return matchesKittySequence(data, cpBackspace, modifier)

	case "insert":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["insert"]) || matchesKittySequence(data, cpInsert, 0)
		}
		if matchesLegacyModifierSequence(data, "insert", modifier) {
			return true
		}
		return matchesKittySequence(data, cpInsert, modifier)

	case "delete":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["delete"]) || matchesKittySequence(data, cpDelete, 0)
		}
		if matchesLegacyModifierSequence(data, "delete", modifier) {
			return true
		}
		return matchesKittySequence(data, cpDelete, modifier)

	case "clear":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["clear"])
		}
		return matchesLegacyModifierSequence(data, "clear", modifier)

	case "home":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["home"]) || matchesKittySequence(data, cpHome, 0)
		}
		if matchesLegacyModifierSequence(data, "home", modifier) {
			return true
		}
		return matchesKittySequence(data, cpHome, modifier)

	case "end":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["end"]) || matchesKittySequence(data, cpEnd, 0)
		}
		if matchesLegacyModifierSequence(data, "end", modifier) {
			return true
		}
		return matchesKittySequence(data, cpEnd, modifier)

	case "pageup":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["pageUp"]) || matchesKittySequence(data, cpPageUp, 0)
		}
		if matchesLegacyModifierSequence(data, "pageUp", modifier) {
			return true
		}
		return matchesKittySequence(data, cpPageUp, modifier)

	case "pagedown":
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["pageDown"]) || matchesKittySequence(data, cpPageDown, 0)
		}
		if matchesLegacyModifierSequence(data, "pageDown", modifier) {
			return true
		}
		return matchesKittySequence(data, cpPageDown, modifier)

	case "up":
		if alt && !ctrl && !shift {
			return data == "\x1bp" || matchesKittySequence(data, cpUp, modAlt)
		}
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["up"]) || matchesKittySequence(data, cpUp, 0)
		}
		if matchesLegacyModifierSequence(data, "up", modifier) {
			return true
		}
		return matchesKittySequence(data, cpUp, modifier)

	case "down":
		if alt && !ctrl && !shift {
			return data == "\x1bn" || matchesKittySequence(data, cpDown, modAlt)
		}
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["down"]) || matchesKittySequence(data, cpDown, 0)
		}
		if matchesLegacyModifierSequence(data, "down", modifier) {
			return true
		}
		return matchesKittySequence(data, cpDown, modifier)

	case "left":
		if alt && !ctrl && !shift {
			return data == "\x1b[1;3D" ||
				(!kitty && data == "\x1bB") ||
				data == "\x1bb" ||
				matchesKittySequence(data, cpLeft, modAlt)
		}
		if ctrl && !alt && !shift {
			return data == "\x1b[1;5D" ||
				matchesLegacyModifierSequence(data, "left", modCtrl) ||
				matchesKittySequence(data, cpLeft, modCtrl)
		}
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["left"]) || matchesKittySequence(data, cpLeft, 0)
		}
		if matchesLegacyModifierSequence(data, "left", modifier) {
			return true
		}
		return matchesKittySequence(data, cpLeft, modifier)

	case "right":
		if alt && !ctrl && !shift {
			return data == "\x1b[1;3C" ||
				(!kitty && data == "\x1bF") ||
				data == "\x1bf" ||
				matchesKittySequence(data, cpRight, modAlt)
		}
		if ctrl && !alt && !shift {
			return data == "\x1b[1;5C" ||
				matchesLegacyModifierSequence(data, "right", modCtrl) ||
				matchesKittySequence(data, cpRight, modCtrl)
		}
		if modifier == 0 {
			return matchesLegacySequence(data, legacyKeySequences["right"]) || matchesKittySequence(data, cpRight, 0)
		}
		if matchesLegacyModifierSequence(data, "right", modifier) {
			return true
		}
		return matchesKittySequence(data, cpRight, modifier)

	case "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12":
		if modifier != 0 {
			return false
		}
		return matchesLegacySequence(data, legacyKeySequences[key])
	}

	// Single letter/symbol
	if len(key) == 1 {
		ch := key[0]
		if (ch >= 'a' && ch <= 'z') || symbolKeys[ch] {
			codepoint := int(ch)
			rc := rawCtrlChar(key)

			if ctrl && alt && !shift && !kitty && rc != "" {
				return data == "\x1b"+rc
			}
			if alt && !ctrl && !shift && !kitty && ch >= 'a' && ch <= 'z' {
				if data == "\x1b"+key {
					return true
				}
			}
			if ctrl && !shift && !alt {
				if rc != "" && data == rc {
					return true
				}
				return matchesKittySequence(data, codepoint, modCtrl)
			}
			if ctrl && shift && !alt {
				return matchesKittySequence(data, codepoint, modShift+modCtrl)
			}
			if shift && !ctrl && !alt {
				if data == strings.ToUpper(key) {
					return true
				}
				return matchesKittySequence(data, codepoint, modShift)
			}
			if modifier != 0 {
				return matchesKittySequence(data, codepoint, modifier)
			}
			return data == key || matchesKittySequence(data, codepoint, 0)
		}
	}

	return false
}

// ParseKey parses raw terminal input and returns the key identifier.
func ParseKey(data string) string {
	kitty := IsKittyProtocolActive()

	// Try Kitty protocol first
	if parsed := parseKittySequence(data); parsed != nil {
		cp := parsed.codepoint
		effectiveMod := parsed.modifier & ^lockMask
		var mods []string
		if effectiveMod&modShift != 0 {
			mods = append(mods, "shift")
		}
		if effectiveMod&modCtrl != 0 {
			mods = append(mods, "ctrl")
		}
		if effectiveMod&modAlt != 0 {
			mods = append(mods, "alt")
		}

		isLatinLetter := cp >= 97 && cp <= 122
		isKnownSymbol := cp >= 0 && cp < 128 && symbolKeys[byte(cp)]
		effectiveCP := cp
		if !isLatinLetter && !isKnownSymbol && parsed.baseLayoutKey != nil {
			effectiveCP = *parsed.baseLayoutKey
		}

		var keyName string
		switch effectiveCP {
		case cpEscape:
			keyName = "escape"
		case cpTab:
			keyName = "tab"
		case cpEnter, cpKpEnter:
			keyName = "enter"
		case cpSpace:
			keyName = "space"
		case cpBackspace:
			keyName = "backspace"
		case cpDelete:
			keyName = "delete"
		case cpInsert:
			keyName = "insert"
		case cpHome:
			keyName = "home"
		case cpEnd:
			keyName = "end"
		case cpPageUp:
			keyName = "pageUp"
		case cpPageDown:
			keyName = "pageDown"
		case cpUp:
			keyName = "up"
		case cpDown:
			keyName = "down"
		case cpLeft:
			keyName = "left"
		case cpRight:
			keyName = "right"
		default:
			if effectiveCP >= 97 && effectiveCP <= 122 {
				keyName = string(rune(effectiveCP))
			} else if effectiveCP >= 0 && effectiveCP < 128 && symbolKeys[byte(effectiveCP)] {
				keyName = string(rune(effectiveCP))
			}
		}

		if keyName != "" {
			if len(mods) > 0 {
				return strings.Join(mods, "+") + "+" + keyName
			}
			return keyName
		}
	}

	// Mode-aware legacy sequences
	if kitty {
		if data == "\x1b\r" || data == "\n" {
			return "shift+enter"
		}
	}

	if id, ok := legacySequenceKeyIDs[data]; ok {
		return id
	}

	// Fixed legacy sequences
	switch data {
	case "\x1b":
		return "escape"
	case "\x1c":
		return "ctrl+\\"
	case "\x1d":
		return "ctrl+]"
	case "\x1f":
		return "ctrl+-"
	case "\x1b\x1b":
		return "ctrl+alt+["
	case "\x1b\x1c":
		return "ctrl+alt+\\"
	case "\x1b\x1d":
		return "ctrl+alt+]"
	case "\x1b\x1f":
		return "ctrl+alt+-"
	case "\t":
		return "tab"
	case "\x00":
		return "ctrl+space"
	case " ":
		return "space"
	case "\x7f", "\x08":
		return "backspace"
	case "\x1b[Z":
		return "shift+tab"
	case "\x1b\x7f", "\x1b\x08":
		return "alt+backspace"
	case "\x1bOM":
		return "enter"
	}

	// Enter/newline - mode dependent
	if data == "\r" || (!kitty && data == "\n") {
		return "enter"
	}
	if !kitty && data == "\x1b\r" {
		return "alt+enter"
	}
	if !kitty && data == "\x1b " {
		return "alt+space"
	}
	if !kitty && data == "\x1bB" {
		return "alt+left"
	}
	if !kitty && data == "\x1bF" {
		return "alt+right"
	}

	// Legacy ESC + char
	if !kitty && len(data) == 2 && data[0] == '\x1b' {
		code := data[1]
		if code >= 1 && code <= 26 {
			return fmt.Sprintf("ctrl+alt+%c", code+96)
		}
		if code >= 'a' && code <= 'z' {
			return fmt.Sprintf("alt+%c", code)
		}
	}

	// Arrow/nav legacy
	switch data {
	case "\x1b[A":
		return "up"
	case "\x1b[B":
		return "down"
	case "\x1b[C":
		return "right"
	case "\x1b[D":
		return "left"
	case "\x1b[H", "\x1bOH":
		return "home"
	case "\x1b[F", "\x1bOF":
		return "end"
	case "\x1b[3~":
		return "delete"
	case "\x1b[5~":
		return "pageUp"
	case "\x1b[6~":
		return "pageDown"
	}

	// Raw Ctrl+letter
	if len(data) == 1 {
		code := data[0]
		if code >= 1 && code <= 26 {
			return fmt.Sprintf("ctrl+%c", code+96)
		}
		if code >= 32 && code <= 126 {
			return data
		}
	}

	return ""
}
