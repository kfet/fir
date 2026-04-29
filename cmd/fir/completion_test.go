package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestCompletionScripts_CoverAllFlagsAndSubcommands is a build-time guard that
// parses cmd/fir/args.go for every "--flag" / "-x" the parser handles and
// cmd/fir/app.go for every os.Args[1] subcommand, then asserts both the bash
// and zsh completion scripts mention them.
//
// If you add a new flag or subcommand, you MUST also add it to:
//   - cmd/fir/completions/_fir
//   - cmd/fir/completions/fir.bash
//
// (or add it to the allowedMissing set below if it is intentionally hidden).
func TestCompletionScripts_CoverAllFlagsAndSubcommands(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	flags, shorts, err := extractFlagsFromArgsGo(filepath.Join(wd, "args.go"))
	if err != nil {
		t.Fatalf("extract flags: %v", err)
	}
	subs, err := extractSubcommandsFromAppGo(filepath.Join(wd, "app.go"))
	if err != nil {
		t.Fatalf("extract subcommands: %v", err)
	}
	if len(subs) == 0 {
		t.Fatalf("extracted zero subcommands from app.go — dispatch shape may have changed; update extractSubcommandsFromAppGo")
	}
	if len(flags) == 0 {
		t.Fatalf("extracted zero long flags from args.go — parser shape may have changed; update extractFlagsFromArgsGo")
	}

	zshPath := filepath.Join(wd, "completions", "_fir")
	bashPath := filepath.Join(wd, "completions", "fir.bash")
	zsh, err := os.ReadFile(zshPath)
	if err != nil {
		t.Fatalf("read zsh completion: %v", err)
	}
	bash, err := os.ReadFile(bashPath)
	if err != nil {
		t.Fatalf("read bash completion: %v", err)
	}
	zshStr := string(zsh)
	bashStr := string(bash)

	// Hidden / intentionally-omitted entries.
	allowedMissingFlags := map[string]bool{
		// --login is covered as the `login` subcommand; the legacy --login
		// flag is still parsed for back-compat but not advertised.
		"--login": true,
	}
	allowedMissingSubs := map[string]bool{
		"pty": true, // internal helper, not user-facing
	}

	checkAll := func(name, script string, items []string, allowMissing map[string]bool) {
		t.Helper()
		var missing []string
		for _, it := range items {
			if allowMissing[it] {
				continue
			}
			if !strings.Contains(script, it) {
				missing = append(missing, it)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Errorf("%s completion is missing %d entries: %v\n\nAdd them to cmd/fir/completions/_fir and cmd/fir/completions/fir.bash.",
				name, len(missing), missing)
		}
	}

	checkAll("zsh (long flags)", zshStr, flags, allowedMissingFlags)
	checkAll("zsh (short flags)", zshStr, shorts, nil)
	checkAll("zsh (subcommands)", zshStr, subs, allowedMissingSubs)
	checkAll("bash (long flags)", bashStr, flags, allowedMissingFlags)
	checkAll("bash (short flags)", bashStr, shorts, nil)
	checkAll("bash (subcommands)", bashStr, subs, allowedMissingSubs)
}

// TestCompletionScripts_BashSyntaxValid sanity-checks that the embedded bash
// completion parses as valid bash via `bash -n`. Catches stray-$, unclosed
// case/if, etc.
func TestCompletionScripts_BashSyntaxValid(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	wd, _ := os.Getwd()
	cmd := exec.Command("bash", "-n", filepath.Join(wd, "completions", "fir.bash"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n cmd/fir/completions/fir.bash failed: %v\n%s", err, out)
	}
}

// TestCompletionScripts_ZshSyntaxValid sanity-checks the zsh completion script
// loads cleanly under `zsh -fn`. Skipped when zsh is not installed.
func TestCompletionScripts_ZshSyntaxValid(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not available")
	}
	wd, _ := os.Getwd()
	cmd := exec.Command("zsh", "-fn", filepath.Join(wd, "completions", "_fir"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zsh -fn cmd/fir/completions/_fir failed: %v\n%s", err, out)
	}
}

var (
	reLongFlag  = regexp.MustCompile(`"(--[a-z][a-z0-9-]*)"`)
	reShortFlag = regexp.MustCompile(`"(-[a-z])"`)
	reSubcmd    = regexp.MustCompile(`os\.Args\[1\]\s*==\s*"([a-z-]+)"`)
)

func extractFlagsFromArgsGo(path string) (long, short []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	body := string(data)

	// Restrict to ParseArgs body so the help-text doesn't pollute matches.
	const startMarker = "func ParseArgs"
	const endMarker = "func PrintHelp"
	si := strings.Index(body, startMarker)
	ei := strings.Index(body, endMarker)
	if si < 0 || ei < 0 || ei < si {
		return nil, nil, errParseRange
	}
	parseBody := body[si:ei]

	longSet := map[string]bool{}
	for _, m := range reLongFlag.FindAllStringSubmatch(parseBody, -1) {
		longSet[m[1]] = true
	}
	shortSet := map[string]bool{}
	for _, m := range reShortFlag.FindAllStringSubmatch(parseBody, -1) {
		shortSet[m[1]] = true
	}

	for f := range longSet {
		long = append(long, f)
	}
	for f := range shortSet {
		short = append(short, f)
	}
	sort.Strings(long)
	sort.Strings(short)
	return long, short, nil
}

func extractSubcommandsFromAppGo(path string) ([]string, error) {
	// Subcommands are now declared in subcommands.go (the registry), not via
	// individual os.Args[1] checks in app.go.  Try subcommands.go first;
	// fall back to the old os.Args pattern for compatibility.
	dir := filepath.Dir(path)
	regPath := filepath.Join(dir, "subcommands.go")
	if data, err := os.ReadFile(regPath); err == nil {
		reReg := regexp.MustCompile(`Name:\s*"([a-z-]+)"`)
		seen := map[string]bool{}
		for _, m := range reReg.FindAllStringSubmatch(string(data), -1) {
			seen[m[1]] = true
		}
		if len(seen) > 0 {
			out := make([]string, 0, len(seen))
			for s := range seen {
				out = append(out, s)
			}
			sort.Strings(out)
			return out, nil
		}
	}
	// Legacy fallback: parse os.Args[1] == "..." patterns in app.go.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, m := range reSubcmd.FindAllStringSubmatch(string(data), -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

// errParseRange is returned when the args.go ParseArgs / PrintHelp markers
// can't be located.
var errParseRange = errors.New("could not locate ParseArgs..PrintHelp range in args.go (markers moved?)")
