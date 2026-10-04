package main

import (
	"fmt"
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

// runLeadingExtensionVerb dispatches an extension CLI verb when it is the
// very first argument, before global flags such as -C/--cwd are applied and
// stripped from os.Args — the verb owns its argv. Reserved subcommands take
// precedence. Exits the process when a verb ran; returns otherwise.
func runLeadingExtensionVerb() {
	if len(os.Args) < 2 || dispatchSubcommand(os.Args[1]) != nil {
		return
	}
	runExtensionVerbAt1()
}

// verbLookupDone records the (verb, cwd) pair already looked up and found
// unclaimed, so run() does not repeat extension discovery for every plain
// `fir "prompt"` invocation. A -C chdir changes cwd and forces a re-check.
var verbLookupDone struct{ verb, cwd string }

// runExtensionVerbAt1 runs os.Args[1] as an extension-registered CLI verb
// (frontmatter `cli_verbs:`) and exits, or returns when no extension claims
// it so the caller can fall through to normal argument parsing. Flags are
// skipped — they can never be a verb, and discovery walks the extension tree.
func runExtensionVerbAt1() {
	first := os.Args[1]
	if first == "" || first[0] == '-' {
		return
	}
	cwd, _ := os.Getwd()
	if verbLookupDone.verb == first && verbLookupDone.cwd == cwd {
		return
	}
	code, ok, err := tryRunExtensionVerb(first, os.Args[2:])
	if !ok {
		verbLookupDone.verb, verbLookupDone.cwd = first, cwd
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	}
	os.Exit(code)
}
