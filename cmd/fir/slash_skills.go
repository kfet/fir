// Slash-skill invocation: `fir /<skill-name> <task...>`.
//
// When the first positional argument starts with `/`, it is treated as a
// reference to an installed skill by name. The skill is looked up via the
// usual resource loader (project + user + builtin), and the invocation is
// rewritten into an initial agent message instructing the agent to use the
// skill, with the remaining positional arguments forming the task body.
//
// Slash-skills compose with all other CLI flags: `-p`, `--model`, etc. work
// unchanged.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/resources"
)

// hasSlashSkillPrefix reports whether s looks like a slash-skill invocation
// (starts with '/' followed by at least one rune, and is not a path).
func hasSlashSkillPrefix(s string) bool {
	if len(s) < 2 || s[0] != '/' {
		return false
	}
	// Heuristic: real filesystem paths contain '/' after the first char.
	// Skill names per spec are alphanumerics, dashes, underscores — never
	// contain '/'.
	if strings.ContainsRune(s[1:], '/') {
		return false
	}
	return true
}

// loadAllSkills returns every discoverable skill (project + user + builtin),
// de-duplicated by name and sorted.
func loadAllSkills() []resources.Skill {
	cwd, _ := os.Getwd()
	res := resources.LoadSkills(resources.LoadSkillsOptions{
		Cwd:             cwd,
		AgentDir:        resolveAgentDir(),
		IncludeDefaults: true,
	})
	seen := make(map[string]bool, len(res.Skills))
	out := make([]resources.Skill, 0, len(res.Skills))
	for _, s := range res.Skills {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// findSkillByName returns the named skill plus the full sorted name list.
// The caller can use the names slice to render an "available" message on
// miss without re-loading.
func findSkillByName(name string) (*resources.Skill, []string) {
	skills := loadAllSkills()
	names := make([]string, len(skills))
	var found *resources.Skill
	for i := range skills {
		names[i] = skills[i].Name
		if skills[i].Name == name {
			found = &skills[i]
		}
	}
	return found, names
}

// rewriteSlashSkillMessages inspects args.Messages and, if the first message
// is a slash-skill reference, replaces the message slice with a single
// directive message that points the agent at the skill and supplies the
// remaining positional arguments as the task body.
//
// Returns an error if the named skill is unknown — slash-prefix is an
// explicit signal of intent, so we surface mistyped names rather than
// passing them through as a literal message.
func rewriteSlashSkillMessages(args *Args) error {
	if len(args.Messages) == 0 {
		return nil
	}
	first := args.Messages[0]
	if !hasSlashSkillPrefix(first) {
		return nil
	}
	name := first[1:]
	skill, available := findSkillByName(name)
	if skill == nil {
		return fmt.Errorf("unknown skill %q\nAvailable skills: %s\nRun `fir skills list` for descriptions", name, strings.Join(available, ", "))
	}
	rest := strings.TrimSpace(strings.Join(args.Messages[1:], " "))
	msg := fmt.Sprintf("Use the `%s` skill (defined at %s). Read its SKILL.md and follow it.", skill.Name, skill.FilePath)
	if rest != "" {
		msg += "\n\nTask: " + rest
	}
	args.Messages = []string{msg}
	return nil
}
