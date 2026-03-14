package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/pkg"
)

// newPackageManager creates a Manager using the current working directory and agent dir.
func newPackageManager() (*pkg.Manager, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	agentDir := resolveAgentDir()
	sm := config.NewSettingsManager(cwd, agentDir)
	return pkg.New(agentDir, cwd, sm), nil
}

// runInstall implements "fir install <source> [--local]".
func runInstall() error {
	args := os.Args[2:] // skip "fir install"

	if len(args) == 0 {
		return fmt.Errorf("usage: fir install <source> [--local]")
	}

	var local bool
	var source string
	for _, a := range args {
		switch a {
		case "--local":
			local = true
		default:
			if source != "" {
				return fmt.Errorf("usage: fir install <source> [--local]")
			}
			source = a
		}
	}
	if source == "" {
		return fmt.Errorf("usage: fir install <source> [--local]")
	}

	m, err := newPackageManager()
	if err != nil {
		return err
	}

	scope := "user"
	if local {
		scope = "project"
	}
	fmt.Printf("Installing %s (%s scope)...\n", source, scope)
	return m.Install(source, local)
}

// runUninstall implements "fir uninstall <source> [--local]".
func runUninstall() error {
	args := os.Args[2:] // skip "fir uninstall"

	if len(args) == 0 {
		return fmt.Errorf("usage: fir uninstall <source> [--local]")
	}

	var local bool
	var source string
	for _, a := range args {
		switch a {
		case "--local":
			local = true
		default:
			if source != "" {
				return fmt.Errorf("usage: fir uninstall <source> [--local]")
			}
			source = a
		}
	}
	if source == "" {
		return fmt.Errorf("usage: fir uninstall <source> [--local]")
	}

	m, err := newPackageManager()
	if err != nil {
		return err
	}

	return m.Uninstall(source, local)
}

// runPackageUpdate implements "fir update [source]" for package updates.
// This is distinct from the binary self-update; it is invoked only when
// called as "fir pkg-update" to avoid conflict with the existing "fir update".
// Actually, looking at app.go, "fir update" is already taken for self-update.
// We expose this as a helper called by runPackages.
func runPackageUpdate(source string) error {
	m, err := newPackageManager()
	if err != nil {
		return err
	}
	return m.Update(source)
}

// runPackages implements "fir packages [list|update [source]]".
func runPackages() error {
	args := os.Args[2:] // skip "fir packages"

	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "list", "":
		return runPackagesList()
	case "update":
		source := ""
		if len(args) > 1 {
			source = args[1]
		}
		return runPackageUpdate(source)
	default:
		return fmt.Errorf("unknown packages subcommand: %s\nUsage: fir packages [list | update [source]]", sub)
	}
}

// runPackagesList prints all installed packages in a table.
func runPackagesList() error {
	m, err := newPackageManager()
	if err != nil {
		return err
	}

	pkgs, err := m.List()
	if err != nil {
		return err
	}

	if len(pkgs) == 0 {
		fmt.Println("No packages installed.")
		return nil
	}

	// Compute column widths.
	srcW := len("SOURCE")
	for _, p := range pkgs {
		if n := len(p.Source.Raw); n > srcW {
			srcW = n
		}
	}

	fmt.Printf("%-*s  %-7s  %-6s  %-10s  %s\n", srcW, "SOURCE", "SCOPE", "SKILLS", "EXTENSIONS", "PATH")
	for _, p := range pkgs {
		skills, exts := 0, 0
		if p.Resources != nil {
			skills = len(p.Resources.Skills)
			exts = len(p.Resources.Extensions)
		}
		displayPath := p.InstallPath
		if home, err := os.UserHomeDir(); err == nil {
			if rel, err := filepath.Rel(home, p.InstallPath); err == nil && !filepath.IsAbs(rel) {
				displayPath = "~/" + rel
			}
		}
		fmt.Printf("%-*s  %-7s  %-6d  %-10d  %s\n",
			srcW, p.Source.Raw, p.Scope, skills, exts, displayPath)
	}
	return nil
}
