// Package sections implements persistent per-extension context sections.
//
// An extension owns at most one section, stored by fir at
// <config>/sections/<name>.md. Fir reads the directory itself at session
// start (no extension round-trip), so sections are visible to the very
// first turn — including `fir -p`.
//
// Writes are atomic (tmp + rename) and size-capped; an over-cap write is
// rejected and the last valid version is kept. Content is never truncated.
package sections

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Size caps, in approximate tokens (bytes/4).
const (
	MaxSectionTokens = 500
	MaxTotalTokens   = 2000
	bytesPerToken    = 4
)

// BlockHeader opens the startup sections message. Providers recognise it to
// place a cache breakpoint right after the block.
const BlockHeader = "[SYS_EXT sections]"

// UpdatePrefix starts every in-session replacement message.
const UpdatePrefix = "[SYS_EXT section="

// ext names are file basenames; keep them to a safe slug.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// markerRe matches [SYS_EXT ...] / [/SYS_EXT] and [section ...] / [/section]
// markers, which section text must not be able to forge.
var markerRe = regexp.MustCompile(`(?i)\[/?(?:SYS_EXT|section)\b[^\]]*\]`)

// Section is one extension-owned section.
type Section struct {
	Name string
	Text string
}

// Hash returns a short content hash of the section text.
func (s Section) Hash() string {
	sum := sha256.Sum256([]byte(s.Text))
	return hex.EncodeToString(sum[:8])
}

// Dir returns the sections directory under the global config dir.
// Respects $XDG_CONFIG_HOME.
func Dir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "fir", "sections")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fir", "sections")
}

// Tokens estimates the token count of s.
func Tokens(s string) int { return (len(s) + bytesPerToken - 1) / bytesPerToken }

// Sanitize strips [SYS_EXT]/[section] markers and surrounding whitespace.
// Stripping repeats until stable so nested markers cannot reassemble.
func Sanitize(text string) string {
	for {
		out := markerRe.ReplaceAllString(text, "")
		if out == text {
			return strings.TrimSpace(out)
		}
		text = out
	}
}

func validName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid section owner %q", name)
	}
	return nil
}

// writeMu serialises writers in this process so the total-cap check and the
// write are atomic together; tmpSeq keeps concurrent temp names unique.
var (
	writeMu sync.Mutex
	tmpSeq  atomic.Uint64
)

// Store is a sections directory.
type Store struct{ dir string }

// NewStore returns a Store rooted at dir.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Default returns the Store at Dir().
func Default() *Store { return NewStore(Dir()) }

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(name string) string { return filepath.Join(s.dir, name+".md") }

// Set atomically writes the section for name. It rejects (keeping the
// previous version) when the section alone exceeds MaxSectionTokens or all
// sections together would exceed MaxTotalTokens.
func (s *Store) Set(name, text string) error {
	if err := validName(name); err != nil {
		return err
	}
	text = Sanitize(text)
	if text == "" {
		return s.Clear(name)
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	if n := Tokens(text); n > MaxSectionTokens {
		return fmt.Errorf("section %q is ~%d tokens, over the %d-token cap; write rejected, previous version kept",
			name, n, MaxSectionTokens)
	}
	all, err := s.ReadAll()
	if err != nil {
		return err
	}
	total := Tokens(text)
	for _, sec := range all {
		if sec.Name != name {
			total += Tokens(sec.Text)
		}
	}
	if total > MaxTotalTokens {
		return fmt.Errorf("all sections would total ~%d tokens, over the %d-token cap; write rejected, previous version kept",
			total, MaxTotalTokens)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	// Atomic: write a per-process temp file, then rename over the target.
	tmp := filepath.Join(s.dir, fmt.Sprintf(".%s.%d.%d.tmp", name, os.Getpid(), tmpSeq.Add(1)))
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path(name)); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Clear removes the section for name. Missing is not an error.
func (s *Store) Clear(name string) error {
	if err := validName(name); err != nil {
		return err
	}
	if err := os.Remove(s.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ReadAll returns every non-empty section on disk, sorted by name.
// A missing directory yields no sections.
func (s *Store) ReadAll() ([]Section, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Section
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".md") {
			continue
		}
		name := strings.TrimSuffix(n, ".md")
		if validName(name) != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, n))
		if err != nil {
			continue
		}
		// Hand-edited over-cap files are ignored, never truncated.
		if text := Sanitize(string(data)); text != "" && Tokens(text) <= MaxSectionTokens {
			out = append(out, Section{Name: name, Text: text})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Filter keeps sections whose owner is allowed, in order, skipping any
// that would push the total past MaxTotalTokens (possible only when
// files were written outside Set, e.g. by hand or by racing processes).
func Filter(all []Section, allowed func(name string) bool) []Section {
	var out []Section
	total := 0
	for _, s := range all {
		if allowed == nil || !allowed(s.Name) {
			continue
		}
		if n := Tokens(s.Text); total+n <= MaxTotalTokens {
			total += n
			out = append(out, s)
		}
	}
	return out
}

// RenderBlock renders the startup block: one [SYS_EXT] message holding every
// section, each headed by its owner. Deterministic — no timestamps or
// counters — so the prefix stays byte-identical across sessions. Returns ""
// when there are no sections.
func RenderBlock(secs []Section) string {
	if len(secs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(BlockHeader)
	b.WriteString("\nPersistent extension sections. Each is owned and maintained by the named extension.\n")
	for _, s := range secs {
		fmt.Fprintf(&b, "\n[section=%s]\n%s\n[/section]\n", s.Name, s.Text)
	}
	b.WriteString("[/SYS_EXT]")
	return b.String()
}

// RenderUpdate renders an in-session replacement of one section. prev is the
// version it replaces (0 = the section is new this session). An empty text
// means the section was cleared.
func RenderUpdate(name, text string, ver, prev int) string {
	label := fmt.Sprintf("%s%s v%d", UpdatePrefix, name, ver)
	if prev > 0 {
		label += fmt.Sprintf(" replaces v%d", prev)
	}
	label += "]"
	if text == "" {
		text = "(section cleared — disregard earlier versions)"
	}
	return label + "\n" + text + "\n[/SYS_EXT]"
}
