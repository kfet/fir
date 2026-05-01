package main

import (
	"os"

	"github.com/kfet/fir/pkg/extension"
)

// reservedSubcommandNames returns the set of fir-builtin subcommand names that
// must never be shadowed by extension-registered CLI verbs. Sourced from the
// single subcommands registry so the two stay in sync.
func reservedSubcommandNames() []string {
	out := make([]string, 0, len(subcommands))
	for _, sc := range subcommands {
		out = append(out, sc.Name)
	}
	return out
}

// tryRunExtensionVerb checks whether `verb` is registered by an extension via
// frontmatter `cli_verbs:`. If yes, spawns the extension, dispatches the
// invocation, and returns (exit_code, true, err). If no extension claims the
// verb, returns (_, false, nil) and the caller should continue with normal
// argument parsing.
func tryRunExtensionVerb(verb string, argv []string) (int, bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	binding, err := extension.LookupCLIVerb(verb, cwd, reservedSubcommandNames())
	if err != nil {
		// Discovery error (e.g. verb collision between two extensions). Treat
		// as authoritative — the user typed something that matches a verb,
		// surface the error rather than falling through.
		return 1, true, err
	}
	if binding == nil {
		return 0, false, nil
	}
	code, runErr := extension.RunCLIVerb(binding, argv, cwd, cwd)
	return code, true, runErr
}
