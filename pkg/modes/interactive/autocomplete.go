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

// CommandArgCompletionType describes how to complete a command's arguments.
type CommandArgCompletionType int

const (
	// ArgCompleteNone means no argument completion.
	ArgCompleteNone CommandArgCompletionType = iota
	// ArgCompleteFile means complete with file paths.
	ArgCompleteFile
	// ArgCompleteStatic means complete from a fixed list of values.
	ArgCompleteStatic
)

// CommandArgSpec describes how to complete arguments for a specific command.
type CommandArgSpec struct {
	Type CommandArgCompletionType
	// Values holds the static completion values (for ArgCompleteStatic).
	Values []string
	// SubCommands maps a first argument to a nested spec for the second argument.
	SubCommands map[string]*CommandArgSpec
}

// combinedAutocompleteProvider implements tuicomp.AutocompleteProvider and
// tuicomp.ForceFileSuggestionsProvider with slash-command and file-path completion.
type combinedAutocompleteProvider struct {
	commands []SlashCommand
	basePath string
	// argSpecs maps command name (without "/") to its argument completion spec.
	argSpecs map[string]*CommandArgSpec
}

// Verify interface compliance.
var _ tuicomp.AutocompleteProvider = (*combinedAutocompleteProvider)(nil)
var _ tuicomp.ForceFileSuggestionsProvider = (*combinedAutocompleteProvider)(nil)

// NewCombinedAutocompleteProvider creates an autocomplete provider for the editor.
func NewCombinedAutocompleteProvider(commands []SlashCommand, basePath string, argSpecs map[string]*CommandArgSpec) tuicomp.AutocompleteProvider {
	if argSpecs == nil {
		argSpecs = make(map[string]*CommandArgSpec)
	}
	return &combinedAutocompleteProvider{
		commands: commands,
		basePath: basePath,
		argSpecs: argSpecs,
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

		// Space found — complete command arguments.
		cmdName := textBeforeCursor[1:spaceIndex] // command name without "/"
		afterCmd := textBeforeCursor[spaceIndex+1:]
		return p.getCommandArgSuggestions(cmdName, afterCmd, textBeforeCursor)
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
		// Check if this is a command argument completion (prefix contains space)
		if strings.Contains(prefix, " ") {
			// Command argument: replace the whole prefix, keeping the command
			// and any preceding subcommand args, then append the selected value.
			lastSpace := strings.LastIndex(prefix, " ")
			cmdPart := prefix[:lastSpace+1]
			newLine := beforePrefix + cmdPart + item.Value + " " + afterCursor
			newLines := make([]string, len(lines))
			copy(newLines, lines)
			newLines[cursorLine] = newLine
			return tuicomp.ApplyCompletionResult{
				Lines:      newLines,
				CursorLine: cursorLine,
				CursorCol:  len(beforePrefix) + len(cmdPart) + len(item.Value) + 1,
			}
		}
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
// Command argument completion
// ---------------------------------------------------------------------------

// getCommandArgSuggestions returns completion suggestions for command arguments.
// cmdName is the command without "/", afterCmd is text after "/cmd ", and
// fullPrefix is the entire text before cursor (used as the suggestion prefix).
func (p *combinedAutocompleteProvider) getCommandArgSuggestions(cmdName, afterCmd, fullPrefix string) *tuicomp.AutocompleteSuggestions {
	spec, ok := p.argSpecs[cmdName]
	if !ok {
		return nil
	}

	// If the spec has subcommands, check if we're completing a subcommand
	// or a nested argument.
	if spec.SubCommands != nil {
		parts := strings.SplitN(afterCmd, " ", 2)
		firstArg := parts[0]

		if len(parts) == 1 {
			// Still typing the first arg — complete from subcommand names.
			var candidates []string
			for k := range spec.SubCommands {
				candidates = append(candidates, k)
			}
			sort.Strings(candidates)
			return p.filterStaticCandidates(candidates, firstArg, fullPrefix)
		}

		// First arg is complete, check for nested spec.
		if nested, ok := spec.SubCommands[firstArg]; ok {
			nestedAfter := parts[1]
			return p.completeFromSpec(nested, nestedAfter, fullPrefix)
		}
		return nil
	}

	return p.completeFromSpec(spec, afterCmd, fullPrefix)
}

// completeFromSpec returns suggestions based on the given spec and current partial input.
func (p *combinedAutocompleteProvider) completeFromSpec(spec *CommandArgSpec, partial, fullPrefix string) *tuicomp.AutocompleteSuggestions {
	switch spec.Type {
	case ArgCompleteStatic:
		return p.filterStaticCandidates(spec.Values, partial, fullPrefix)
	case ArgCompleteFile:
		suggestions := p.getFileSuggestions(partial)
		if len(suggestions) == 0 {
			return nil
		}
		// The prefix for replacement is the partial path portion only.
		return &tuicomp.AutocompleteSuggestions{
			Prefix: partial,
			Items:  suggestions,
		}
	default:
		return nil
	}
}

// filterStaticCandidates filters a list of static values by a partial prefix using fuzzy matching.
func (p *combinedAutocompleteProvider) filterStaticCandidates(candidates []string, partial, fullPrefix string) *tuicomp.AutocompleteSuggestions {
	type item struct {
		value string
	}
	items := make([]item, len(candidates))
	for i, c := range candidates {
		items[i] = item{value: c}
	}

	filtered := tui.FuzzyFilter(items, partial, func(it item) string { return it.value })
	if len(filtered) == 0 {
		return nil
	}

	selectItems := make([]tuicomp.SelectItem, len(filtered))
	for i, it := range filtered {
		selectItems[i] = tuicomp.SelectItem{
			Value: it.value,
			Label: it.value,
		}
	}

	return &tuicomp.AutocompleteSuggestions{
		Prefix: fullPrefix,
		Items:  selectItems,
	}
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
