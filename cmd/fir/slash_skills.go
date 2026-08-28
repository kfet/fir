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
// sorted by ID. Same-named skills are kept (each carries a unique ID).
func loadAllSkills() []resources.Skill {
	out := loadCLISkills()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// findSkillByRef resolves a skill reference (bare name or full ID) against
// the loaded set. Returns the matched skill, a sorted list of all known
// references suitable for error messages, and an error when the bare name
// is ambiguous.
func findSkillByRef(ref string) (*resources.Skill, []string, error) {
	skills := loadAllSkills()
	refs := make([]string, 0, len(skills))
	seenRef := make(map[string]bool)
	addRef := func(r string) {
		if !seenRef[r] {
			seenRef[r] = true
			refs = append(refs, r)
		}
	}

	var byID *resources.Skill
	var byName []*resources.Skill
	nameCount := make(map[string]int)
	for _, s := range skills {
		nameCount[s.Name]++
	}
	for i := range skills {
		s := &skills[i]
		if nameCount[s.Name] == 1 {
			addRef(s.Name)
		} else {
			addRef(s.ID)
		}
		if s.ID == ref {
			byID = s
		}
		if s.Name == ref {
			byName = append(byName, s)
		}
	}
	sort.Strings(refs)
	if byID != nil {
		return byID, refs, nil
	}
	if len(byName) == 1 {
		return byName[0], refs, nil
	}
	if len(byName) > 1 {
		var ids []string
		for _, s := range byName {
			ids = append(ids, s.ID)
		}
		sort.Strings(ids)
		return nil, refs, fmt.Errorf("skill %q is ambiguous; specify one of: %s", ref, strings.Join(ids, ", "))
	}
	return nil, refs, nil
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
	skill, available, err := findSkillByRef(name)
	if err != nil {
		return err
	}
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
