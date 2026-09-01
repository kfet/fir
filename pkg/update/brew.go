package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

// standardBrewPrefixes are the prefixes Homebrew itself documents and uses by
// default. A Cellar under one of these is trusted without consulting `brew`,
// which is what lets us produce a useful error when the exe is clearly
// brew-managed but `brew` is missing from PATH.
var standardBrewPrefixes = []string{
	"/opt/homebrew",              // macOS arm64
	"/usr/local",                 // macOS amd64
	"/home/linuxbrew/.linuxbrew", // Linuxbrew
}

// BrewInstall describes a detected Homebrew-managed installation of fir.
type BrewInstall struct {
	// ExePath is the running executable with symlinks resolved — the real
	// file inside the Cellar.
	ExePath string
	// Prefix is the Homebrew prefix the Cellar belongs to, e.g. "/opt/homebrew".
	Prefix string
	// Formula is the name to hand to `brew upgrade`. Fully qualified when we
	// could resolve it (e.g. "kfet/ai/fir"), otherwise the bare keg name.
	Formula string
	// BrewPath is the resolved `brew` executable, empty when brew is not on
	// PATH. An install with a non-empty Prefix and an empty BrewPath is the
	// one case we must refuse to self-update.
	BrewPath string
}

// brewEnv holds the environment lookups brew detection performs. It exists so
// tests can drive detection over synthetic paths and injected prefixes without
// requiring Homebrew — or even the filesystem — to be present.
type brewEnv struct {
	// executablePath returns the path of the running executable.
	executablePath func() (string, error)
	// evalSymlinks resolves symlinks in a path.
	evalSymlinks func(string) (string, error)
	// lookPath finds an executable on PATH.
	lookPath func(string) (string, error)
	// brewPrefix returns the prefix reported by `brew --prefix`, or an error.
	brewPrefix func(ctx context.Context, brewPath string) (string, error)
	// fullFormulaName resolves a bare keg name to its fully-qualified name.
	fullFormulaName func(ctx context.Context, brewPath, keg string) (string, error)
	// readFile reads a file from disk — used for the install receipt.
	readFile func(string) ([]byte, error)
}

// defaultBrewEnv is the production environment.
func defaultBrewEnv() brewEnv {
	return brewEnv{
		executablePath:  selfupdate.ExecutablePath,
		evalSymlinks:    filepath.EvalSymlinks,
		lookPath:        exec.LookPath,
		brewPrefix:      execBrewPrefix,
		fullFormulaName: execFullFormulaName,
		readFile:        os.ReadFile,
	}
}

// DetectBrewInstall reports whether the running executable is managed by
// Homebrew. It returns (nil, nil) when the install is not brew-managed —
// including every case where we are merely unsure, since a false positive
// breaks `fir update` on non-brew hosts while a false negative only leaves us
// on the pre-existing self-update path.
func DetectBrewInstall(ctx context.Context) (*BrewInstall, error) {
	return detectBrewInstall(ctx, defaultBrewEnv())
}

func detectBrewInstall(ctx context.Context, env brewEnv) (*BrewInstall, error) {
	exePath, err := env.executablePath()
	if err != nil {
		return nil, fmt.Errorf("locate current executable: %w", err)
	}
	real, err := env.evalSymlinks(exePath)
	if err != nil {
		// Cannot resolve — stay conservative and let self-update proceed.
		return nil, nil //nolint:nilerr // deliberate: unsure means "not brew".
	}

	// Cheap gate first: no Cellar component means not brew-managed, and we
	// never pay for an exec on the overwhelmingly common non-brew host.
	cellar, ok := splitCellarPath(real)
	if !ok {
		return nil, nil
	}

	brewPath, lookErr := env.lookPath("brew")
	if lookErr != nil {
		brewPath = ""
	}

	// Confirm the candidate prefix is a real Homebrew prefix rather than a
	// directory that merely happens to be called "Cellar".
	confirmed := isStandardBrewPrefix(cellar.prefix)
	if !confirmed && brewPath != "" {
		if reported, err := env.brewPrefix(ctx, brewPath); err == nil {
			confirmed = filepath.Clean(reported) == cellar.prefix
		}
	}
	if !confirmed {
		return nil, nil
	}

	inst := &BrewInstall{
		ExePath:  real,
		Prefix:   cellar.prefix,
		Formula:  cellar.keg,
		BrewPath: brewPath,
	}

	// Prefer the fully-qualified name so `brew upgrade` cannot bind to a
	// same-named formula from a different tap. Falling back to the bare keg
	// name is safe: it is exactly what Homebrew laid down on disk.
	//
	// The install receipt is authoritative for the *name* only — never for
	// detection. It is written by Homebrew at install time, so it names the
	// exact tap this keg came from, costs no exec, works with `brew` absent,
	// and — unlike `brew info` — is unambiguous when several taps provide the
	// same formula. Its presence is deliberately not allowed to confirm
	// brew-management: receipts survive copied or relocated Cellars, and the
	// conservative rule above is what keeps `fir update` working on non-brew
	// hosts.
	if full, ok := receiptFormulaName(env.readFile, cellar.kegDir, cellar.keg); ok {
		inst.Formula = full
	} else if brewPath != "" {
		if full, err := env.fullFormulaName(ctx, brewPath, cellar.keg); err == nil && full != "" {
			inst.Formula = full
		}
	}
	return inst, nil
}

// cellarPath is a resolved executable path decomposed into its Homebrew parts.
type cellarPath struct {
	// prefix is the Homebrew prefix, e.g. "/opt/homebrew".
	prefix string
	// keg is the formula's bare name, e.g. "fir".
	keg string
	// kegDir is the installed version's directory, e.g.
	// "/opt/homebrew/Cellar/fir/1.3.2". Empty when nothing follows the keg
	// name.
	kegDir string
}

// splitCellarPath splits a resolved executable path of the shape
// <prefix>/Cellar/<keg>/<version>/bin/<exe> into its parts.
// ok is false when the path has no "Cellar" component with a name after it.
func splitCellarPath(path string) (cellarPath, bool) {
	path = filepath.Clean(path)
	sep := string(filepath.Separator)
	parts := strings.Split(path, sep)
	for i, p := range parts {
		if p != "Cellar" {
			continue
		}
		// Need a keg name after "Cellar" and a non-empty prefix before it.
		if i == 0 || i+1 >= len(parts) || parts[i+1] == "" {
			continue
		}
		out := cellarPath{prefix: strings.Join(parts[:i], sep), keg: parts[i+1]}
		if out.prefix == "" {
			out.prefix = sep
		}
		if i+2 < len(parts) && parts[i+2] != "" {
			out.kegDir = filepath.Join(out.prefix, "Cellar", out.keg, parts[i+2])
		}
		return out, true
	}
	return cellarPath{}, false
}

// isStandardBrewPrefix reports whether prefix is one of Homebrew's documented
// default prefixes.
func isStandardBrewPrefix(prefix string) bool {
	prefix = filepath.Clean(prefix)
	for _, p := range standardBrewPrefixes {
		if prefix == p {
			return true
		}
	}
	return false
}

// execBrewPrefix runs `brew --prefix`, which is fast and side-effect free.
func execBrewPrefix(ctx context.Context, brewPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, brewPath, "--prefix").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// installReceipt is the slice of Homebrew's INSTALL_RECEIPT.json we care
// about. Homebrew writes it into every keg directory at install time.
type installReceipt struct {
	Source struct {
		Tap string `json:"tap"`
	} `json:"source"`
}

// receiptFormulaName resolves the fully-qualified formula name "<tap>/<keg>"
// from <kegDir>/INSTALL_RECEIPT.json.
//
// Every failure mode — no reader, no receipt, unreadable, malformed JSON,
// empty tap, tap not of the form owner/repo — reports ok=false so the caller
// falls through to the next resolution step. It never errors out.
func receiptFormulaName(readFile func(string) ([]byte, error), kegDir, keg string) (string, bool) {
	if readFile == nil || kegDir == "" || keg == "" {
		return "", false
	}
	data, err := readFile(filepath.Join(kegDir, "INSTALL_RECEIPT.json"))
	if err != nil {
		return "", false
	}
	var receipt installReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return "", false
	}
	tap := strings.TrimSpace(receipt.Source.Tap)
	if !validTapName(tap) {
		return "", false
	}
	return tap + "/" + keg, true
}

// validTapName reports whether tap is a plausible "owner/repo" Homebrew tap.
// Anything else is rejected rather than handed to `brew upgrade` — notably a
// component starting with "-", which brew would parse as a flag.
func validTapName(tap string) bool {
	parts := strings.Split(tap, "/")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "-") ||
			strings.ContainsAny(p, " \t\n\r\\") {
			return false
		}
	}
	return true
}

// brewInfoV2 is the slice of `brew info --json=v2` output we care about.
type brewInfoV2 struct {
	Formulae []struct {
		FullName string `json:"full_name"`
	} `json:"formulae"`
}

// execFullFormulaName resolves the fully-qualified formula name (e.g.
// "kfet/ai/fir") for an installed keg.
func execFullFormulaName(ctx context.Context, brewPath, keg string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, brewPath, "info", "--json=v2", "--formula", keg).Output()
	if err != nil {
		return "", err
	}
	var info brewInfoV2
	if err := json.Unmarshal(out, &info); err != nil {
		return "", err
	}
	if len(info.Formulae) == 0 || info.Formulae[0].FullName == "" {
		return "", fmt.Errorf("brew info: no formula named %q", keg)
	}
	return info.Formulae[0].FullName, nil
}

// UpgradeViaBrew runs `brew update` followed by `brew upgrade <formula>`,
// streaming both commands' output to the supplied writers so a multi-minute
// upgrade is not swallowed into silence.
//
// No timeout is imposed: a brew upgrade can legitimately take many minutes
// (it may compile). Cancellation is the caller's ctx.
func UpgradeViaBrew(ctx context.Context, inst *BrewInstall, stdout, stderr io.Writer) error {
	if inst == nil {
		return fmt.Errorf("no brew install detected")
	}
	if inst.BrewPath == "" {
		return fmt.Errorf(
			"fir is installed by Homebrew (%s) but `brew` is not on PATH; "+
				"self-updating would overwrite the keg and be reverted by the next "+
				"`brew upgrade`. Put brew on PATH and re-run `fir update`",
			inst.ExePath)
	}

	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, inst.BrewPath, args...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("brew %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}

	if err := run("update"); err != nil {
		return err
	}
	return run("upgrade", inst.Formula)
}
