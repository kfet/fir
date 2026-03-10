package extension

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/resources"
)

// FrontmatterMismatch describes a difference between what an extension
// declares in its comment frontmatter and what it actually registers
// during the init handshake.
type FrontmatterMismatch struct {
	ExtName string
	Path    string
	// Missing lists items present in the handshake but absent from frontmatter.
	MissingEvents   []string
	MissingCommands []string
	// Extra lists items in frontmatter but not in the handshake (stale).
	ExtraEvents   []string
	ExtraCommands []string
}

// Summary returns a human-readable description of the mismatch.
func (m FrontmatterMismatch) Summary() string {
	var parts []string
	if len(m.MissingEvents) > 0 {
		parts = append(parts, fmt.Sprintf("missing events: %s", strings.Join(m.MissingEvents, ", ")))
	}
	if len(m.MissingCommands) > 0 {
		parts = append(parts, fmt.Sprintf("missing commands: %s", strings.Join(m.MissingCommands, ", ")))
	}
	if len(m.ExtraEvents) > 0 {
		parts = append(parts, fmt.Sprintf("extra events: %s", strings.Join(m.ExtraEvents, ", ")))
	}
	if len(m.ExtraCommands) > 0 {
		parts = append(parts, fmt.Sprintf("extra commands: %s", strings.Join(m.ExtraCommands, ", ")))
	}
	return strings.Join(parts, "; ")
}

// Empty returns true if there is no mismatch.
func (m FrontmatterMismatch) Empty() bool {
	return len(m.MissingEvents) == 0 && len(m.MissingCommands) == 0 &&
		len(m.ExtraEvents) == 0 && len(m.ExtraCommands) == 0
}

// CheckFrontmatter compares the extension's frontmatter declarations against
// the actual capabilities returned by the init handshake.
func CheckFrontmatter(cfg ExtProcConfig, caps *InitResult) FrontmatterMismatch {
	mm := FrontmatterMismatch{ExtName: cfg.Name, Path: cfg.Path}

	// Build sets from frontmatter.
	fmEvents := make(map[string]bool, len(cfg.Events))
	for _, e := range cfg.Events {
		fmEvents[e] = true
	}
	fmCmds := make(map[string]bool, len(cfg.Commands))
	for _, c := range cfg.Commands {
		fmCmds[c.Name] = true
	}

	// Check events from handshake.
	for _, e := range caps.Events {
		if !fmEvents[e] {
			mm.MissingEvents = append(mm.MissingEvents, e)
		}
	}
	// Check for extra (stale) events in frontmatter.
	capsEvents := make(map[string]bool, len(caps.Events))
	for _, e := range caps.Events {
		capsEvents[e] = true
	}
	for _, e := range cfg.Events {
		if !capsEvents[e] {
			mm.ExtraEvents = append(mm.ExtraEvents, e)
		}
	}

	// Check commands from handshake.
	for _, c := range caps.Commands {
		if !fmCmds[c.Name] {
			mm.MissingCommands = append(mm.MissingCommands, c.Name)
		}
	}
	// Check for extra (stale) commands in frontmatter.
	capsCmds := make(map[string]bool, len(caps.Commands))
	for _, c := range caps.Commands {
		capsCmds[c.Name] = true
	}
	for _, c := range cfg.Commands {
		if !capsCmds[c.Name] {
			mm.ExtraCommands = append(mm.ExtraCommands, c.Name)
		}
	}

	sort.Strings(mm.MissingEvents)
	sort.Strings(mm.MissingCommands)
	sort.Strings(mm.ExtraEvents)
	sort.Strings(mm.ExtraCommands)
	return mm
}

// FixFrontmatter rewrites the extension file's comment frontmatter to include
// the correct events and commands from the handshake result. It preserves all
// other frontmatter fields and the rest of the file.
func FixFrontmatter(path string, caps *InitResult) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	// Find frontmatter boundaries.
	start := 0
	if len(lines) > 0 && strings.HasPrefix(lines[0], "#!") {
		start = 1
	}
	if start >= len(lines) || strings.TrimSpace(lines[start]) != "# ---" {
		return fmt.Errorf("no frontmatter found in %s", path)
	}

	openLine := start
	closeLine := -1
	for i := openLine + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "# ---" {
			closeLine = i
			break
		}
	}
	if closeLine < 0 {
		return fmt.Errorf("no closing frontmatter delimiter in %s", path)
	}

	// Parse existing frontmatter, preserving non-events/commands fields.
	var newFM []string
	for i := openLine + 1; i < closeLine; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "# ") {
			continue
		}
		kv := strings.TrimPrefix(trimmed, "# ")
		key, _, ok := strings.Cut(kv, ":")
		if !ok {
			newFM = append(newFM, line)
			continue
		}
		key = strings.TrimSpace(key)
		if key == "events" || key == "event" || key == "commands" {
			continue // will be rewritten
		}
		newFM = append(newFM, line)
	}

	// Add events line if there are subscribed events.
	if len(caps.Events) > 0 {
		sorted := make([]string, len(caps.Events))
		copy(sorted, caps.Events)
		sort.Strings(sorted)
		newFM = append(newFM, "# events: "+strings.Join(sorted, ", "))
	}

	// Add commands line if there are registered commands.
	if len(caps.Commands) > 0 {
		var parts []string
		for _, c := range caps.Commands {
			if c.Description != "" {
				parts = append(parts, c.Name+": "+c.Description)
			} else {
				parts = append(parts, c.Name)
			}
		}
		newFM = append(newFM, "# commands: "+strings.Join(parts, ", "))
	}

	// Rebuild the file.
	var result []string
	result = append(result, lines[:openLine+1]...) // up to and including "# ---"
	result = append(result, newFM...)
	result = append(result, lines[closeLine:]...) // "# ---" closing and rest of file

	return os.WriteFile(path, []byte(strings.Join(result, "\n")), 0o644)
}

// FormatFrontmatterWarning formats a warning message for a frontmatter mismatch.
func FormatFrontmatterWarning(mm FrontmatterMismatch) string {
	return fmt.Sprintf("Warning: extension %q frontmatter is incomplete (%s)\n  file: %s",
		mm.ExtName, mm.Summary(), mm.Path)
}

// FrontmatterFromInitResult builds the frontmatter fields that correspond
// to an InitResult, useful for comparing with what was declared.
func FrontmatterFromInitResult(caps *InitResult) (events []string, commands []resources.ExtensionFrontmatterCommand) {
	events = caps.Events
	for _, c := range caps.Commands {
		commands = append(commands, resources.ExtensionFrontmatterCommand{
			Name:        c.Name,
			Description: c.Description,
		})
	}
	return
}
