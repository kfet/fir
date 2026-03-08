package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/resources"
)

// runSkills implements the "fir skills" subcommand family.
func runSkills() error {
	args := os.Args[2:] // skip "fir skills"

	if len(args) == 0 || args[0] == "list" {
		return runSkillsList()
	}

	if args[0] == "install" {
		if len(args) < 2 {
			return fmt.Errorf("usage: fir skills install <name> [--user] [--force]")
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
		return runSkillsInstall(name, user, force)
	}

	return fmt.Errorf("unknown skills subcommand: %s\nUsage: fir skills [list | install <name> [--user] [--force]]", args[0])
}

// runSkillsList lists all loaded skills in a table.
func runSkillsList() error {
	cwd, _ := os.Getwd()
	agentDir := resolveAgentDir()

	result := resources.LoadSkills(resources.LoadSkillsOptions{
		Cwd:             cwd,
		AgentDir:        agentDir,
		IncludeDefaults: true,
	})

	skills := result.Skills
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})

	if len(skills) == 0 {
		fmt.Println("No skills found.")
		return nil
	}

	// Compute column widths
	nameW := 4 // "NAME"
	sourceW := 6 // "SOURCE"
	for _, s := range skills {
		if len(s.Name) > nameW {
			nameW = len(s.Name)
		}
		if len(s.Source) > sourceW {
			sourceW = len(s.Source)
		}
	}

	fmt.Printf("%-*s  %-*s  %s\n", nameW, "NAME", sourceW, "SOURCE", "DESCRIPTION")
	for _, s := range skills {
		desc := s.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		fmt.Printf("%-*s  %-*s  %s\n", nameW, s.Name, sourceW, s.Source, desc)
	}
	return nil
}

// runSkillsInstall extracts a builtin skill to the project or user directory.
func runSkillsInstall(name string, user, force bool) error {
	// Verify the skill exists in builtins
	builtins := resources.LoadBuiltinSkills()
	var found bool
	for _, s := range builtins.Skills {
		if s.Name == name {
			found = true
			break
		}
	}
	if !found {
		available := make([]string, 0, len(builtins.Skills))
		for _, s := range builtins.Skills {
			available = append(available, s.Name)
		}
		sort.Strings(available)
		return fmt.Errorf("unknown builtin skill %q\nAvailable: %s", name, strings.Join(available, ", "))
	}

	// Determine target directory
	var targetDir string
	if user {
		agentDir := resolveAgentDir()
		targetDir = filepath.Join(agentDir, "skills", name)
	} else {
		cwd, _ := os.Getwd()
		targetDir = filepath.Join(cwd, ".fir", "skills", name)
	}

	// Check if target exists
	if _, err := os.Stat(targetDir); err == nil && !force {
		return fmt.Errorf("skill %q already exists at %s (use --force to overwrite)", name, targetDir)
	}

	// Extract from embedded FS
	prefix := "builtin_skills/" + name
	err := fs.WalkDir(resources.BuiltinSkillsFS, prefix, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(path, prefix)
		if rel == "" {
			return nil
		}
		rel = strings.TrimPrefix(rel, "/")
		target := filepath.Join(targetDir, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := resources.BuiltinSkillsFS.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return fmt.Errorf("extract skill %q: %w", name, err)
	}

	scope := "project"
	if user {
		scope = "user"
	}
	fmt.Printf("Installed skill %q to %s (%s)\n", name, targetDir, scope)
	return nil
}
