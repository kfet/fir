// Command aside-default-advisor keeps the aside extension's default advisor
// in sync with the bundled model registry.
//
// The aside extension exposes an "escalate" parameter that routes side
// queries to a stronger advisor model. When no user config is present,
// it falls back to a built-in default — and that default must point at
// the highest Anthropic Opus tier that fir actually knows about.
//
// This tool:
//
//  1. Parses pkg/ai/models_generated.go for every (id, provider) pair
//     where provider == "anthropic" and id matches claude-opus-<major>-<minor>.
//  2. Picks the lexically-numerically-highest such id.
//  3. Either writes it into pkg/resources/builtin_extensions/aside.py
//     (replacing the value between the BEGIN/END sentinels) or verifies
//     that the file already matches.
//
// Two flags:
//
//	-check  exit 1 if aside.py is out of sync
//	-write  rewrite aside.py with the current top model
//
// Default is -check, suitable for `make all`. Run with -write from the
// generate-models target whenever the model registry changes.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const (
	beginMarker = "# BEGIN_DEFAULT_ADVISOR (auto-generated; bump via `make generate-models`)"
	endMarker   = "# END_DEFAULT_ADVISOR"
)

func main() {
	check := flag.Bool("check", false, "verify aside.py is in sync with the model registry")
	write := flag.Bool("write", false, "rewrite aside.py with the current highest Anthropic Opus")
	modelsPath := flag.String("models", "", "path to models_generated.go (default: <repo>/pkg/ai/models_generated.go)")
	asidePath := flag.String("aside", "", "path to aside.py (default: <repo>/pkg/resources/builtin_extensions/aside.py)")
	flag.Parse()

	if *check == *write {
		log.Fatal("specify exactly one of -check or -write")
	}

	root := repoRoot()
	if *modelsPath == "" {
		*modelsPath = filepath.Join(root, "pkg", "ai", "models_generated.go")
	}
	if *asidePath == "" {
		*asidePath = filepath.Join(root, "pkg", "resources", "builtin_extensions", "aside.py")
	}

	top, err := highestAnthropicOpus(*modelsPath)
	if err != nil {
		log.Fatalf("scan %s: %v", *modelsPath, err)
	}
	if top == "" {
		log.Fatalf("no anthropic claude-opus-N-M models found in %s", *modelsPath)
	}
	spec := "anthropic/" + top

	if *write {
		if err := writeAsideDefault(*asidePath, spec); err != nil {
			log.Fatalf("write %s: %v", *asidePath, err)
		}
		fmt.Printf("aside-default-advisor: wrote %s\n", spec)
		return
	}

	got, err := readAsideDefault(*asidePath)
	if err != nil {
		log.Fatalf("read %s: %v", *asidePath, err)
	}
	if got != spec {
		fmt.Fprintf(os.Stderr,
			"aside-default-advisor: %s is out of sync\n"+
				"  expected: %s\n"+
				"  actual:   %s\n"+
				"Fix: make generate-models (or: go run ./cmd/aside-default-advisor -write)\n",
			*asidePath, spec, got)
		os.Exit(1)
	}
}

// highestAnthropicOpus scans models_generated.go and returns the largest
// claude-opus-<major>-<minor> id registered under the "anthropic" provider.
// We deliberately ignore date-suffixed aliases (e.g. claude-opus-4-1-20250805,
// claude-opus-4-20250514) because the bare claude-opus-X-Y id is the
// long-lived alias users pin to. Date-suffixed aliases are detected by:
//   - the trailing component having more than two digits, OR
//   - the id having a fourth dash-separated component
//
// Real version numbers stay short (4-7, 5-0); date stamps are 8 digits.
func highestAnthropicOpus(path string) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// Match the bare `claude-opus-<major>-<minor>` form only — anchored by
	// the closing quote, so a fourth dash-separated component (date stamp)
	// is rejected by construction. Minor capped at 2 digits to also reject
	// `claude-opus-4-20250514` which has only two components but a date.
	idRe := regexp.MustCompile(`ID:\s*"(claude-opus-(\d+)-(\d{1,2}))"`)
	provRe := regexp.MustCompile(`Provider:\s*"([^"]+)"`)

	type cand struct {
		id           string
		major, minor int
	}
	var cands []cand

	// Walk RegisterModel blocks. A simple split-on-RegisterModel works
	// because each call is its own self-contained struct literal.
	for _, block := range strings.Split(string(src), "RegisterModel(&Model{") {
		idM := idRe.FindStringSubmatch(block)
		if idM == nil {
			continue
		}
		provM := provRe.FindStringSubmatch(block)
		if provM == nil || provM[1] != "anthropic" {
			continue
		}
		major, minor := atoi(idM[2]), atoi(idM[3])
		cands = append(cands, cand{id: idM[1], major: major, minor: minor})
	}

	if len(cands) == 0 {
		return "", nil
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].major != cands[j].major {
			return cands[i].major < cands[j].major
		}
		return cands[i].minor < cands[j].minor
	})
	return cands[len(cands)-1].id, nil
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// readAsideDefault returns the spec string currently embedded between the
// BEGIN/END_DEFAULT_ADVISOR markers in aside.py.
func readAsideDefault(path string) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	startIdx := strings.Index(string(src), beginMarker)
	endIdx := strings.Index(string(src), endMarker)
	if startIdx == -1 || endIdx == -1 || endIdx < startIdx {
		return "", fmt.Errorf("BEGIN/END_DEFAULT_ADVISOR markers not found")
	}
	block := string(src[startIdx:endIdx])
	// The literal we expect inside the block is _DEFAULT_ADVISOR_SPEC = "..."
	specRe := regexp.MustCompile(`_DEFAULT_ADVISOR_SPEC\s*=\s*"([^"]+)"`)
	m := specRe.FindStringSubmatch(block)
	if m == nil {
		return "", fmt.Errorf("_DEFAULT_ADVISOR_SPEC literal not found between markers")
	}
	return m[1], nil
}

// writeAsideDefault replaces the _DEFAULT_ADVISOR_SPEC literal in aside.py
// with *spec*, leaving everything else untouched.
func writeAsideDefault(path, spec string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	startIdx := strings.Index(string(src), beginMarker)
	endIdx := strings.Index(string(src), endMarker)
	if startIdx == -1 || endIdx == -1 || endIdx < startIdx {
		return fmt.Errorf("BEGIN/END_DEFAULT_ADVISOR markers not found")
	}

	// Within the marked block, replace the spec literal value.
	block := string(src[startIdx:endIdx])
	specRe := regexp.MustCompile(`(_DEFAULT_ADVISOR_SPEC\s*=\s*")([^"]+)(")`)
	if !specRe.MatchString(block) {
		return fmt.Errorf("_DEFAULT_ADVISOR_SPEC literal not found between markers")
	}
	newBlock := specRe.ReplaceAllString(block, "${1}"+spec+"${3}")
	if newBlock == block {
		return nil // already up to date
	}

	out := string(src[:startIdx]) + newBlock + string(src[endIdx:])
	return os.WriteFile(path, []byte(out), 0o644)
}

// repoRoot finds the repo root by walking up from this file's directory
// until it finds go.mod. Mirrors the helper in cmd/generate-models.
func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		log.Fatal("cannot determine source file location")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	// Fallback to cwd.
	cwd, _ := os.Getwd()
	return cwd
}
