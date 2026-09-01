// Slash invocation: `fir /<name> <args...>`.
//
// When the first positional argument starts with `/`, it references either an
// installed skill or a slash command. Skills win (back-compat): the skill is
// looked up via the usual resource loader (project + user + builtin) and the
// invocation is rewritten into an initial agent message instructing the agent
// to use it, with the remaining positional arguments forming the task body.
// Otherwise the raw invocation is handed to the interactive mode, which
// dispatches it as a builtin or extension slash command.
//
// Slash invocations compose with all other CLI flags: `--model` etc. work
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

// resolveSlashInvocation inspects args.Messages and, if the first message is
// a slash invocation, resolves it in this order:
//
//  1. Skill — rewritten into a directive message (back-compat: a skill always
//     wins over a same-named command).
//  2. Builtin slash command — passed through verbatim as the initial prompt so
//     the interactive mode dispatches it exactly as if it had been typed.
//  3. Otherwise — passed through verbatim in interactive mode, where it is
//     resolved against extension-provided commands once extensions have
//     loaded (their names are only known after the handshake).
//
// Slash commands are TUI-bound: most are selectors or render into TUI
// containers. In headless modes (-p / --output-mode json / acp) there is
// nothing to dispatch them to, so they error rather than hang or leak to the
// model — and an unresolvable name errors immediately, since extension
// commands cannot apply there either.
func resolveSlashInvocation(args *Args, headless bool) error {
	if len(args.Messages) == 0 {
		return nil
	}
	first := args.Messages[0]
	if !hasSlashSkillPrefix(first) {
		return nil
	}
	name := first[1:]
	// `/skill:<name>` is expanded by the session itself (in every mode) —
	// leave it alone.
	if strings.HasPrefix(name, "skill:") {
		return nil
	}
	skill, available, err := findSkillByRef(name)
	if err != nil {
		return err
	}
	if skill == nil {
		isCommand := resources.IsBuiltinSlashCommandName(name)
		if headless {
			if isCommand {
				return fmt.Errorf("command /%s requires interactive mode; run `fir /%s` without -p/--output-mode", name, name)
			}
			return fmt.Errorf("unknown skill or command %q\nAvailable skills: %s\nRun `fir skills list` for descriptions, or start fir and type /help for slash commands", name, strings.Join(available, ", "))
		}
		// Interactive: hand the raw invocation to the mode, which dispatches
		// builtins and extension commands and reports unknown names itself.
		args.Messages = []string{strings.TrimSpace(strings.Join(args.Messages, " "))}
		return nil
	}
	rest := strings.TrimSpace(strings.Join(args.Messages[1:], " "))
	msg := fmt.Sprintf("Use the `%s` skill (defined at %s). Read its SKILL.md and follow it.", skill.Name, skill.FilePath)
	if rest != "" {
		msg += "\n\nTask: " + rest
	}
	args.Messages = []string{msg}
	return nil
}
