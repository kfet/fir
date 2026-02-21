// Ported from: packages/tui/src/autocomplete.ts (CombinedAutocompleteProvider)
// Upstream hash: 1caadb2e
//
// Provides slash-command completion and file-path completion for the editor.
package interactive

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// SlashCommand describes a completable slash command.
type SlashCommand struct {
	Name        string
	Description string
}

// combinedAutocompleteProvider implements tuicomp.AutocompleteProvider and
// tuicomp.ForceFileSuggestionsProvider with slash-command and file-path completion.
type combinedAutocompleteProvider struct {
	commands []SlashCommand
	basePath string
}

// Verify interface compliance.
var _ tuicomp.AutocompleteProvider = (*combinedAutocompleteProvider)(nil)
var _ tuicomp.ForceFileSuggestionsProvider = (*combinedAutocompleteProvider)(nil)

// NewCombinedAutocompleteProvider creates an autocomplete provider for the editor.
func NewCombinedAutocompleteProvider(commands []SlashCommand, basePath string) tuicomp.AutocompleteProvider {
	return &combinedAutocompleteProvider{
		commands: commands,
		basePath: basePath,
	}
}

// pathDelimiters are characters that separate path tokens.
var pathDelimiters = map[byte]bool{
	' ': true, '\t': true, '"': true, '\'': true, '=': true,
}

func (p *combinedAutocompleteProvider) GetSuggestions(lines []string, cursorLine, cursorCol int) *tuicomp.AutocompleteSuggestions {
	if cursorLine >= len(lines) {
		return nil
	}
	currentLine := lines[cursorLine]
	if cursorCol > len(currentLine) {
		cursorCol = len(currentLine)
	}
	textBeforeCursor := currentLine[:cursorCol]

	// Check for @ file reference
	atPrefix := extractAtPrefix(textBeforeCursor)
	if atPrefix != "" {
		rawPrefix, _ := parsePathPrefix(atPrefix)
		suggestions := p.getFuzzyFileSuggestions(rawPrefix)
		if len(suggestions) == 0 {
			return nil
		}
		return &tuicomp.AutocompleteSuggestions{
			Prefix: atPrefix,
			Items:  suggestions,
		}
	}

	// Check for slash commands
	if strings.HasPrefix(textBeforeCursor, "/") {
		spaceIndex := strings.Index(textBeforeCursor, " ")

		if spaceIndex == -1 {
			// No space — complete command names with fuzzy matching
			prefix := textBeforeCursor[1:] // Remove "/"

			type cmdItem struct {
				name        string
				description string
			}
			items := make([]cmdItem, len(p.commands))
			for i, c := range p.commands {
				items[i] = cmdItem{name: c.Name, description: c.Description}
			}

			filtered := tui.FuzzyFilter(items, prefix, func(it cmdItem) string { return it.name })
			if len(filtered) == 0 {
				return nil
			}

			selectItems := make([]tuicomp.SelectItem, len(filtered))
			for i, it := range filtered {
				selectItems[i] = tuicomp.SelectItem{
					Value:       it.name,
					Label:       it.name,
					Description: it.description,
				}
			}

			return &tuicomp.AutocompleteSuggestions{
				Prefix: textBeforeCursor,
				Items:  selectItems,
			}
		}

		// Space found — no command-argument completion yet
		return nil
	}

	// Check for file paths
	pathMatch := extractPathPrefix(textBeforeCursor, false)
	if pathMatch != "" {
		suggestions := p.getFileSuggestions(pathMatch)
		if len(suggestions) == 0 {
			return nil
		}
		return &tuicomp.AutocompleteSuggestions{
			Prefix: pathMatch,
			Items:  suggestions,
		}
	}

	return nil
}

func (p *combinedAutocompleteProvider) ApplyCompletion(
	lines []string, cursorLine, cursorCol int,
	item tuicomp.SelectItem, prefix string,
) tuicomp.ApplyCompletionResult {
	if cursorLine >= len(lines) {
		return tuicomp.ApplyCompletionResult{Lines: lines, CursorLine: cursorLine, CursorCol: cursorCol}
	}
	currentLine := lines[cursorLine]
	if cursorCol > len(currentLine) {
		cursorCol = len(currentLine)
	}
	beforePrefix := currentLine[:cursorCol-len(prefix)]
	afterCursor := currentLine[cursorCol:]

	// Slash command completion
	isSlashCommand := strings.HasPrefix(prefix, "/") && strings.TrimSpace(beforePrefix) == "" && !strings.Contains(prefix[1:], "/")
	if isSlashCommand {
		newLine := beforePrefix + "/" + item.Value + " " + afterCursor
		newLines := make([]string, len(lines))
		copy(newLines, lines)
		newLines[cursorLine] = newLine
		return tuicomp.ApplyCompletionResult{
			Lines:      newLines,
			CursorLine: cursorLine,
			CursorCol:  len(beforePrefix) + len(item.Value) + 2, // +2 for "/" and space
		}
	}

	// @ file reference
	if strings.HasPrefix(prefix, "@") {
		isDirectory := strings.HasSuffix(item.Label, "/")
		suffix := ""
		if !isDirectory {
			suffix = " "
		}
		newLine := beforePrefix + item.Value + suffix + afterCursor
		newLines := make([]string, len(lines))
		copy(newLines, lines)
		newLines[cursorLine] = newLine
		return tuicomp.ApplyCompletionResult{
			Lines:      newLines,
			CursorLine: cursorLine,
			CursorCol:  len(beforePrefix) + len(item.Value) + len(suffix),
		}
	}

	// File path completion
	newLine := beforePrefix + item.Value + afterCursor
	newLines := make([]string, len(lines))
	copy(newLines, lines)
	newLines[cursorLine] = newLine

	isDirectory := strings.HasSuffix(item.Label, "/")
	cursorOffset := len(item.Value)
	if isDirectory && strings.HasSuffix(item.Value, "\"") {
		cursorOffset = len(item.Value) - 1
	}
	return tuicomp.ApplyCompletionResult{
		Lines:      newLines,
		CursorLine: cursorLine,
		CursorCol:  len(beforePrefix) + cursorOffset,
	}
}

// GetForceFileSuggestions provides file suggestions triggered by Tab key.
func (p *combinedAutocompleteProvider) GetForceFileSuggestions(lines []string, cursorLine, cursorCol int) *tuicomp.AutocompleteSuggestions {
	if cursorLine >= len(lines) {
		return nil
	}
	currentLine := lines[cursorLine]
	if cursorCol > len(currentLine) {
		cursorCol = len(currentLine)
	}
	textBeforeCursor := currentLine[:cursorCol]

	pathPrefix := extractPathPrefix(textBeforeCursor, true)
	suggestions := p.getFileSuggestions(pathPrefix)
	if len(suggestions) == 0 {
		return nil
	}
	return &tuicomp.AutocompleteSuggestions{
		Prefix: pathPrefix,
		Items:  suggestions,
	}
}

// ShouldTriggerFileCompletion returns true if Tab should trigger file completion.
func (p *combinedAutocompleteProvider) ShouldTriggerFileCompletion(lines []string, cursorLine, cursorCol int) bool {
	return true // Always allow Tab file completion
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func findLastDelimiter(text string) int {
	for i := len(text) - 1; i >= 0; i-- {
		if pathDelimiters[text[i]] {
			return i
		}
	}
	return -1
}

func isTokenStart(text string, index int) bool {
	return index == 0 || pathDelimiters[text[index-1]]
}

func extractAtPrefix(text string) string {
	lastDelim := findLastDelimiter(text)
	tokenStart := 0
	if lastDelim >= 0 {
		tokenStart = lastDelim + 1
	}
	if tokenStart < len(text) && text[tokenStart] == '@' {
		return text[tokenStart:]
	}
	return ""
}

func parsePathPrefix(prefix string) (rawPrefix string, isAtPrefix bool) {
	if strings.HasPrefix(prefix, "@\"") {
		return prefix[2:], true
	}
	if strings.HasPrefix(prefix, "@") {
		return prefix[1:], true
	}
	if strings.HasPrefix(prefix, "\"") {
		return prefix[1:], false
	}
	return prefix, false
}

func extractPathPrefix(text string, forceExtract bool) string {
	lastDelim := findLastDelimiter(text)
	var pathPrefix string
	if lastDelim == -1 {
		pathPrefix = text
	} else {
		pathPrefix = text[lastDelim+1:]
	}

	if forceExtract {
		return pathPrefix
	}

	if strings.Contains(pathPrefix, "/") || strings.HasPrefix(pathPrefix, ".") || strings.HasPrefix(pathPrefix, "~/") {
		return pathPrefix
	}

	if pathPrefix == "" && strings.HasSuffix(text, " ") {
		return pathPrefix
	}

	return ""
}

func expandHomePath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		expanded := filepath.Join(home, path[2:])
		if strings.HasSuffix(path, "/") && !strings.HasSuffix(expanded, "/") {
			expanded += "/"
		}
		return expanded
	}
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	return path
}

// cleanSlashes collapses runs of consecutive forward slashes into a single
// slash (e.g. "a//b///c" → "a/b/c"). This prevents double-slash artifacts
// when path segments are concatenated with "/".
func cleanSlashes(s string) string {
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	return s
}

func (p *combinedAutocompleteProvider) getFileSuggestions(prefix string) []tuicomp.SelectItem {
	rawPrefix, isAtPrefix := parsePathPrefix(prefix)
	expandedPrefix := rawPrefix
	if strings.HasPrefix(expandedPrefix, "~") {
		expandedPrefix = expandHomePath(expandedPrefix)
	}

	var searchDir, searchPrefix string
	isRoot := rawPrefix == "" || rawPrefix == "./" || rawPrefix == "../" ||
		rawPrefix == "~" || rawPrefix == "~/" || rawPrefix == "/"

	if isRoot {
		if strings.HasPrefix(rawPrefix, "~") || strings.HasPrefix(expandedPrefix, "/") {
			searchDir = expandedPrefix
		} else {
			searchDir = filepath.Join(p.basePath, expandedPrefix)
		}
		searchPrefix = ""
	} else if strings.HasSuffix(rawPrefix, "/") {
		if strings.HasPrefix(rawPrefix, "~") || strings.HasPrefix(expandedPrefix, "/") {
			searchDir = expandedPrefix
		} else {
			searchDir = filepath.Join(p.basePath, expandedPrefix)
		}
		searchPrefix = ""
	} else {
		dir := filepath.Dir(expandedPrefix)
		file := filepath.Base(expandedPrefix)
		if strings.HasPrefix(rawPrefix, "~") || strings.HasPrefix(expandedPrefix, "/") {
			searchDir = dir
		} else {
			searchDir = filepath.Join(p.basePath, dir)
		}
		searchPrefix = file
	}

	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return nil
	}

	var suggestions []tuicomp.SelectItem
	searchPrefixLower := strings.ToLower(searchPrefix)

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(strings.ToLower(name), searchPrefixLower) {
			continue
		}

		isDirectory := entry.IsDir()
		if !isDirectory && entry.Type()&os.ModeSymlink != 0 {
			fullPath := filepath.Join(searchDir, name)
			if info, err := os.Stat(fullPath); err == nil {
				isDirectory = info.IsDir()
			}
		}

		var relativePath string
		if strings.HasSuffix(rawPrefix, "/") {
			relativePath = rawPrefix + name
		} else if strings.Contains(rawPrefix, "/") {
			if strings.HasPrefix(rawPrefix, "~/") {
				dir := filepath.Dir(rawPrefix[2:])
				if dir == "." {
					relativePath = "~/" + name
				} else {
					relativePath = "~/" + dir + "/" + name
				}
			} else if strings.HasPrefix(rawPrefix, "/") {
				dir := filepath.Dir(rawPrefix)
				if dir == "/" {
					relativePath = "/" + name
				} else {
					relativePath = dir + "/" + name
				}
			} else {
				relativePath = filepath.Join(filepath.Dir(rawPrefix), name)
			}
		} else {
			if strings.HasPrefix(rawPrefix, "~") {
				relativePath = "~/" + name
			} else {
				relativePath = name
			}
		}
		relativePath = cleanSlashes(relativePath)

		pathValue := relativePath
		if isDirectory {
			pathValue += "/"
		}

		value := pathValue
		if isAtPrefix {
			value = "@" + value
		}

		label := name
		if isDirectory {
			label += "/"
		}

		suggestions = append(suggestions, tuicomp.SelectItem{
			Value: value,
			Label: label,
		})
	}

	// Sort: directories first, then alphabetically
	sort.Slice(suggestions, func(i, j int) bool {
		iDir := strings.HasSuffix(suggestions[i].Value, "/")
		jDir := strings.HasSuffix(suggestions[j].Value, "/")
		if iDir != jDir {
			return iDir
		}
		return suggestions[i].Label < suggestions[j].Label
	})

	return suggestions
}

// maxWalkFiles caps the number of files collected during recursive walk to
// keep autocomplete responsive.
const maxWalkFiles = 10000

// skipDirs contains directory names that are skipped during recursive walk.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".hg":          true,
	".svn":         true,
	"__pycache__":  true,
	".tox":         true,
	".venv":        true,
	"vendor":       true,
}

// collectFiles recursively collects files under root, returning paths
// relative to root. Hidden directories (starting with ".") other than "."
// itself are skipped, as are common noise directories.
func collectFiles(root string) []fileEntry {
	var items []fileEntry
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(items) >= maxWalkFiles {
			return filepath.SkipAll
		}

		name := d.Name()

		// Skip noisy / hidden directories (but not the root itself).
		if d.IsDir() && path != root {
			if skipDirs[name] || (strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
		}

		// Skip the root entry itself.
		if path == root {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		// Normalise to forward-slash separators for display / matching.
		rel = filepath.ToSlash(rel)

		items = append(items, fileEntry{name: rel, isDir: d.IsDir()})
		return nil
	})
	return items
}

type fileEntry struct {
	name  string // relative path (forward-slash separated)
	isDir bool
}

func (p *combinedAutocompleteProvider) getFuzzyFileSuggestions(rawPrefix string) []tuicomp.SelectItem {
	// For @ completions, use fuzzy matching across the entire file tree.
	expandedPrefix := rawPrefix
	if strings.HasPrefix(expandedPrefix, "~") {
		expandedPrefix = expandHomePath(expandedPrefix)
	}

	// If the prefix explicitly names a directory (ends with "/"), list that
	// directory's direct contents — no recursive walk needed.
	if strings.HasSuffix(expandedPrefix, "/") {
		return p.getFuzzyFileSuggestionsDir(expandedPrefix)
	}

	// Determine the root to walk and the query to fuzzy-match against.
	walkRoot := p.basePath
	query := expandedPrefix

	// If prefix contains a "/" we anchor the walk one level deeper and
	// fuzzy-match only the remainder, but still walk recursively from there.
	if strings.Contains(expandedPrefix, "/") {
		dir := filepath.Dir(expandedPrefix)
		if filepath.IsAbs(dir) || strings.HasPrefix(rawPrefix, "~") {
			walkRoot = dir
		} else {
			walkRoot = filepath.Join(p.basePath, dir)
		}
		query = filepath.Base(expandedPrefix)
	}

	items := collectFiles(walkRoot)

	if query != "" {
		items = tui.FuzzyFilter(items, query, func(f fileEntry) string { return f.name })
	}

	// Build suggestion list.
	var suggestions []tuicomp.SelectItem
	for _, item := range items {
		label := item.name
		if item.isDir {
			label += "/"
		}

		// Compute the relative path shown in the value (includes any
		// directory prefix the user already typed).
		var relPath string
		if strings.Contains(expandedPrefix, "/") {
			dir := filepath.Dir(expandedPrefix)
			relPath = cleanSlashes(dir + "/" + item.name)
		} else {
			relPath = item.name
		}
		if item.isDir {
			relPath += "/"
		}
		value := "@" + relPath

		suggestions = append(suggestions, tuicomp.SelectItem{
			Value: value,
			Label: label,
		})
	}

	return suggestions
}

// getFuzzyFileSuggestionsDir lists the direct contents of a single directory
// (used when the prefix explicitly ends with "/").
func (p *combinedAutocompleteProvider) getFuzzyFileSuggestionsDir(expandedPrefix string) []tuicomp.SelectItem {
	searchDir := expandedPrefix
	if !filepath.IsAbs(searchDir) {
		searchDir = filepath.Join(p.basePath, searchDir)
	}

	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return nil
	}

	var suggestions []tuicomp.SelectItem
	for _, entry := range entries {
		name := entry.Name()
		isDir := entry.IsDir()

		label := name
		if isDir {
			label += "/"
		}

		relPath := cleanSlashes(expandedPrefix + name)
		if isDir {
			relPath += "/"
		}
		value := "@" + relPath

		suggestions = append(suggestions, tuicomp.SelectItem{
			Value: value,
			Label: label,
		})
	}

	// Sort: directories first, then alphabetically.
	sort.Slice(suggestions, func(i, j int) bool {
		iDir := strings.HasSuffix(suggestions[i].Value, "/")
		jDir := strings.HasSuffix(suggestions[j].Value, "/")
		if iDir != jDir {
			return iDir
		}
		return suggestions[i].Label < suggestions[j].Label
	})

	return suggestions
}
