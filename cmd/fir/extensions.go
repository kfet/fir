package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/resources"
)

// runExtensions implements the "fir extensions" subcommand family.
func runExtensions() error {
	args := os.Args[2:] // skip "fir extensions"

	if len(args) == 0 || args[0] == "list" {
		return runExtensionsList()
	}

	if args[0] == "install" {
		if len(args) < 2 {
			return fmt.Errorf("usage: fir extensions install <name> [--user] [--force]")
		}
		name := args[1]
		var user, force bool
		for _, a := range args[2:] {
			switch a {
			case "--user":
				user = true
			case "--force":
				force = true
			default:
				return fmt.Errorf("unknown flag: %s", a)
			}
		}
		return runExtensionsInstall(name, user, force)
	}

	return fmt.Errorf("unknown extensions subcommand: %s\nUsage: fir extensions [list | install <name> [--user] [--force]]", args[0])
}

// runExtensionsList lists all builtin extensions.
func runExtensionsList() error {
	builtins := listBuiltinExtensionMeta()

	if len(builtins) == 0 {
		fmt.Println("No builtin extensions found.")
		return nil
	}

	sort.Slice(builtins, func(i, j int) bool {
		return builtins[i].Name < builtins[j].Name
	})

	nameW := 4
	for _, b := range builtins {
		if len(b.Name) > nameW {
			nameW = len(b.Name)
		}
	}

	fmt.Printf("%-*s  %s\n", nameW, "NAME", "DESCRIPTION")
	for _, b := range builtins {
		desc := b.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		fmt.Printf("%-*s  %s\n", nameW, b.Name, desc)
	}
	return nil
}

type builtinExtMeta struct {
	Name        string
	Description string
	FileName    string // original filename in the embedded FS
}

// listBuiltinExtensionMeta reads frontmatter from all embedded extensions
// marked with builtin: true.
func listBuiltinExtensionMeta() []builtinExtMeta {
	entries, err := resources.BuiltinExtensionsFS.ReadDir("builtin_extensions")
	if err != nil {
		return nil
	}

	var result []builtinExtMeta
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := resources.BuiltinExtensionsFS.ReadFile("builtin_extensions/" + e.Name())
		if err != nil {
			continue
		}
		fm := resources.ParseCommentFrontmatter(string(data))
		if !fm.Builtin {
			continue
		}
		name := fm.Name
		if name == "" {
			name = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		}
		result = append(result, builtinExtMeta{
			Name:        name,
			Description: fm.Description,
			FileName:    e.Name(),
		})
	}
	return result
}

// runExtensionsInstall extracts a builtin extension to the project or user directory.
func runExtensionsInstall(name string, user, force bool) error {
	builtins := listBuiltinExtensionMeta()

	var found *builtinExtMeta
	for i := range builtins {
		if builtins[i].Name == name {
			found = &builtins[i]
			break
		}
	}
	if found == nil {
		available := make([]string, 0, len(builtins))
		for _, b := range builtins {
			available = append(available, b.Name)
		}
		sort.Strings(available)
		return fmt.Errorf("unknown builtin extension %q\nAvailable: %s", name, strings.Join(available, ", "))
	}

	// Determine target directory
	var targetDir string
	if user {
		agentDir := resolveAgentDir()
		targetDir = filepath.Join(agentDir, "extensions")
	} else {
		cwd, _ := os.Getwd()
		targetDir = filepath.Join(cwd, ".fir", "extensions")
	}

	targetFile := filepath.Join(targetDir, found.FileName)

	// Check if target exists
	if _, err := os.Stat(targetFile); err == nil && !force {
		return fmt.Errorf("extension %q already exists at %s (use --force to overwrite)", name, targetFile)
	}

	// Read from embedded FS
	data, err := resources.BuiltinExtensionsFS.ReadFile("builtin_extensions/" + found.FileName)
	if err != nil {
		return fmt.Errorf("read builtin extension %q: %w", name, err)
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", targetDir, err)
	}

	// Strip the builtin: true frontmatter line so the installed copy
	// is treated as a normal extension (not a duplicate builtin).
	content := stripBuiltinFrontmatter(string(data))

	if err := os.WriteFile(targetFile, []byte(content), 0o755); err != nil {
		return fmt.Errorf("write extension %q: %w", name, err)
	}

	scope := "project"
	if user {
		scope = "user"
	}
	fmt.Printf("Installed extension %q to %s (%s)\n", name, targetFile, scope)
	return nil
}

// stripBuiltinFrontmatter removes the "# builtin: true" line from
// frontmatter so an installed copy isn't treated as a builtin.
func stripBuiltinFrontmatter(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "# builtin: true" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
