package builtin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/slashcmd"
)

// ============================================================================
// /name, /sections, /export
// ============================================================================

func cmdName(c *slashcmd.Ctx, name string) {
	s := sess(c)
	if s == nil {
		c.Out.Warn(noSession)
		return
	}
	if name == "" {
		if cur := s.SessionName(); cur != "" {
			c.Out.Message("Session name: " + cur)
		} else {
			c.Out.Warn("Usage: /name <name>")
		}
		return
	}
	s.SetSessionName(name)
	c.Out.Message("Session name set: " + name)
}

func cmdSections(c *slashcmd.Ctx, _ string) {
	s := sess(c)
	if s == nil {
		c.Out.Warn(noSession)
		return
	}
	c.Out.Code(s.SectionsText())
}

func cmdExport(c *slashcmd.Ctx, path string) {
	s := sess(c)
	if s == nil {
		c.Out.Warn(noSession)
		return
	}
	c.Async(func() {
		filePath, err := s.ExportToHTML(path)
		if err != nil {
			c.Out.Warn(fmt.Sprintf("Failed to export session: %v", err))
			return
		}
		c.Out.Status(fmt.Sprintf("Session exported to: %s", filePath))
	})
}

// ============================================================================
// /share
// ============================================================================

var gistIDRegex = regexp.MustCompile(`^[a-fA-F0-9]{20,}$`)

// runCommand is swapped by tests.
var runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func cmdShare(c *slashcmd.Ctx, _ string) {
	s := sess(c)
	if s == nil {
		c.Out.Warn(noSession)
		return
	}
	c.Async(func() { share(c, s) })
}

func share(c *slashcmd.Ctx, s Session) {
	if _, err := runCommand(c.Context, "gh", "auth", "status"); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			c.Out.Warn("GitHub CLI is not logged in. Run 'gh auth login' first.")
		} else {
			c.Out.Warn("GitHub CLI (gh) is not installed. Install it from https://cli.github.com/")
		}
		return
	}
	tmpPath, err := s.ExportToHTML("")
	if err != nil {
		c.Out.Warn(fmt.Sprintf("Failed to export session: %v", err))
		return
	}
	defer os.Remove(tmpPath)

	ctx, cancel := context.WithCancel(c.Context)
	defer cancel()
	stop := func() {}
	if p, ok := c.Out.(Progress); ok {
		stop = p.StartProgress("Creating gist...", cancel)
	}
	out, err := runCommand(ctx, "gh", "gist", "create", "--public=false", tmpPath)
	stop()
	if ctx.Err() == context.Canceled {
		c.Out.Status("Share cancelled.")
		return
	}
	if err != nil {
		c.Out.Warn("Failed to create gist. Check that 'gh' is installed and authenticated.")
		return
	}
	gistURL := strings.TrimSpace(string(out))
	if gistURL == "" {
		c.Out.Warn("Gist created but no URL returned.")
		return
	}
	gistID := gistURL[strings.LastIndex(gistURL, "/")+1:]
	if !gistIDRegex.MatchString(gistID) {
		c.Out.Warn(fmt.Sprintf("Gist created but could not parse ID from URL: %s", gistURL))
		return
	}
	c.Out.Message(fmt.Sprintf("Session shared (secret gist):\nGist: %s\nPreview: https://gistpreview.github.io/?%s", gistURL, gistID))
}

// ============================================================================
// /skills
// ============================================================================

const skillsUsage = "Usage: /skills [list | install <name> [--user] [--force] | <name>]"

func cmdSkills(c *slashcmd.Ctx, args string) {
	if args == "" {
		cmdSkillsList(c, "")
		return
	}
	name, _, _ := strings.Cut(args, " ")
	s := sess(c)
	if s == nil {
		c.Out.Warn(noSession)
		return
	}
	for _, sk := range s.Skills() {
		if sk.Name == name {
			c.Out.Code(fmt.Sprintf("Name:        %s\nSource:      %s\nLocation:    %s\nDescription: %s",
				sk.Name, resources.DisplayOrigin(sk), sk.FilePath, sk.Description))
			return
		}
	}
	c.Out.Warn(fmt.Sprintf("Unknown skills subcommand or skill: %s. %s", name, skillsUsage))
}

func cmdSkillsList(c *slashcmd.Ctx, _ string) {
	s := sess(c)
	if s == nil {
		c.Out.Warn(noSession)
		return
	}
	skills := s.Skills()
	if len(skills) == 0 {
		c.Out.Status("No skills loaded.")
		return
	}
	sorted := make([]resources.Skill, len(skills))
	copy(sorted, skills)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	nameW, sourceW := 4, 6
	origins := make([]string, len(sorted))
	for i, sk := range sorted {
		origins[i] = resources.DisplayOrigin(sk)
		nameW = max(nameW, len(sk.Name))
		sourceW = max(sourceW, len(origins[i]))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-*s  %-*s  %s\n", nameW, "NAME", sourceW, "SOURCE", "DESCRIPTION")
	for i, sk := range sorted {
		desc := sk.Description
		if len(desc) > 50 {
			desc = desc[:47] + "..."
		}
		fmt.Fprintf(&sb, "%-*s  %-*s  %s\n", nameW, sk.Name, sourceW, origins[i], desc)
	}
	c.Out.Code(strings.TrimRight(sb.String(), "\n"))
}

// userSkillsDir is swapped by tests.
var userSkillsDir = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".fir", "agent", "skills")
}

func cmdSkillsInstall(c *slashcmd.Ctx, args string) {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		c.Out.Warn("Usage: /skills install <name> [--user] [--force]")
		return
	}
	name := parts[0]
	var toUser, force bool
	for _, p := range parts[1:] {
		switch p {
		case "--user":
			toUser = true
		case "--force":
			force = true
		}
	}

	builtins := resources.LoadBuiltinSkills()
	available := make([]string, 0, len(builtins.Skills))
	found := false
	for _, sk := range builtins.Skills {
		available = append(available, sk.Name)
		found = found || sk.Name == name
	}
	if !found {
		sort.Strings(available)
		c.Out.Warn(fmt.Sprintf("Unknown builtin skill %q. Available: %s", name, strings.Join(available, ", ")))
		return
	}

	var targetDir, scope string
	if toUser {
		targetDir, scope = filepath.Join(userSkillsDir(), name), "user"
	} else {
		cwd := ""
		if h := host(c); h != nil {
			cwd = h.Cwd()
		}
		targetDir, scope = filepath.Join(cwd, ".fir", "skills", name), "project"
	}
	if _, err := os.Stat(targetDir); err == nil && !force {
		c.Out.Warn(fmt.Sprintf("Skill %q already exists at %s. Use --force to overwrite.", name, targetDir))
		return
	}
	if err := copyBuiltinSkill(name, targetDir); err != nil {
		c.Out.Warn(fmt.Sprintf("Failed to install skill %q: %v", name, err))
		return
	}
	if s := sess(c); s != nil {
		_ = s.Reload()
		if hook, ok := host(c).(ResourcesReloadedHook); ok {
			hook.ResourcesReloaded()
		}
	}
	c.Out.Status(fmt.Sprintf("Installed skill %q to %s (%s)", name, targetDir, scope))
}

func copyBuiltinSkill(name, targetDir string) error {
	prefix := "builtin_skills/" + name
	return fs.WalkDir(resources.BuiltinSkillsFS, prefix, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		dest := filepath.Join(targetDir, strings.TrimPrefix(path, prefix))
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		// WalkDir visits a directory before its files, so dest's parent
		// already exists.
		data, err := fs.ReadFile(resources.BuiltinSkillsFS, path)
		if err == nil {
			err = os.WriteFile(dest, data, 0o644)
		}
		return err
	})
}
