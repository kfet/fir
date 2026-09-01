package update

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"testing"
)

// -----------------------------------------------------------------------------
// splitCellarPath
// -----------------------------------------------------------------------------

func TestSplitCellarPath(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantOK     bool
		wantPrefix string
		wantKeg    string
		wantKegDir string
	}{
		{
			name:       "macos arm64 cellar",
			path:       "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
			wantOK:     true,
			wantPrefix: "/opt/homebrew",
			wantKeg:    "fir",
			wantKegDir: "/opt/homebrew/Cellar/fir/1.3.2",
		},
		{
			name:       "macos amd64 cellar",
			path:       "/usr/local/Cellar/fir/1.3.2/bin/fir",
			wantOK:     true,
			wantPrefix: "/usr/local",
			wantKeg:    "fir",
			wantKegDir: "/usr/local/Cellar/fir/1.3.2",
		},
		{
			name:       "linuxbrew cellar",
			path:       "/home/linuxbrew/.linuxbrew/Cellar/fir/1.3.2/bin/fir",
			wantOK:     true,
			wantPrefix: "/home/linuxbrew/.linuxbrew",
			wantKeg:    "fir",
			wantKegDir: "/home/linuxbrew/.linuxbrew/Cellar/fir/1.3.2",
		},
		{
			name:       "custom prefix cellar",
			path:       "/Users/kfet/homebrew/Cellar/fir/1.3.2/bin/fir",
			wantOK:     true,
			wantPrefix: "/Users/kfet/homebrew",
			wantKeg:    "fir",
			wantKegDir: "/Users/kfet/homebrew/Cellar/fir/1.3.2",
		},
		{
			name:       "uncleaned path",
			path:       "/opt/homebrew/../homebrew/Cellar/fir/1.3.2/bin/fir",
			wantOK:     true,
			wantPrefix: "/opt/homebrew",
			wantKeg:    "fir",
			wantKegDir: "/opt/homebrew/Cellar/fir/1.3.2",
		},
		{
			name:       "keg with no version component has no keg dir",
			path:       "/opt/homebrew/Cellar/fir",
			wantOK:     true,
			wantPrefix: "/opt/homebrew",
			wantKeg:    "fir",
			wantKegDir: "",
		},
		{name: "plain local install", path: "/home/kfet/.local/bin/fir"},
		{name: "system install", path: "/usr/bin/fir"},
		{name: "go bin install", path: "/home/kfet/go/bin/fir"},
		{name: "cellar is last component", path: "/opt/homebrew/Cellar"},
		{name: "cellar at root with nothing after", path: "/Cellar"},
		{name: "lowercase cellar is not a match", path: "/opt/homebrew/cellar/fir/1.3.2/bin/fir"},
		{name: "cellar as filename only", path: "/home/kfet/Cellar"},
		{name: "empty path", path: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := splitCellarPath(tt.path)
			if ok != tt.wantOK {
				t.Fatalf("splitCellarPath(%q) ok = %v, want %v", tt.path, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if got.prefix != tt.wantPrefix {
				t.Errorf("prefix = %q, want %q", got.prefix, tt.wantPrefix)
			}
			if got.keg != tt.wantKeg {
				t.Errorf("keg = %q, want %q", got.keg, tt.wantKeg)
			}
			if got.kegDir != tt.wantKegDir {
				t.Errorf("kegDir = %q, want %q", got.kegDir, tt.wantKegDir)
			}
		})
	}
}

// A "Cellar" directory nested deeper than the prefix must still resolve to the
// segment before it, so confirmation against a real prefix is what rejects it.
func TestSplitCellarPath_NestedCellarUsesFirstMatch(t *testing.T) {
	got, ok := splitCellarPath("/tmp/Cellar/fir/1.0.0/bin/fir")
	if !ok {
		t.Fatal("expected a match")
	}
	if got.prefix != "/tmp" || got.keg != "fir" {
		t.Errorf("got (%q, %q), want (/tmp, fir)", got.prefix, got.keg)
	}
	if got.kegDir != "/tmp/Cellar/fir/1.0.0" {
		t.Errorf("kegDir = %q, want /tmp/Cellar/fir/1.0.0", got.kegDir)
	}
}

// -----------------------------------------------------------------------------
// isStandardBrewPrefix
// -----------------------------------------------------------------------------

func TestIsStandardBrewPrefix(t *testing.T) {
	tests := []struct {
		prefix string
		want   bool
	}{
		{"/opt/homebrew", true},
		{"/usr/local", true},
		{"/home/linuxbrew/.linuxbrew", true},
		{"/opt/homebrew/", true}, // Clean() strips the trailing separator
		{"/Users/kfet/homebrew", false},
		{"/tmp", false},
		{"/opt", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isStandardBrewPrefix(tt.prefix); got != tt.want {
			t.Errorf("isStandardBrewPrefix(%q) = %v, want %v", tt.prefix, got, tt.want)
		}
	}
}

// -----------------------------------------------------------------------------
// detectBrewInstall
// -----------------------------------------------------------------------------

// stubEnv builds a brewEnv over synthetic values. Nothing touches the real
// filesystem, PATH, or Homebrew, so these tests pass on hosts without brew.
type stubEnv struct {
	exePath     string
	exeErr      error
	resolved    string // result of evalSymlinks; defaults to exePath
	resolveErr  error
	brewPath    string // "" means brew is not on PATH
	prefix      string
	prefixErr   error
	fullName    string
	fullNameErr error
	// receipts maps an absolute file path to its contents. A path that is
	// absent reads as "no such file".
	receipts map[string]string

	prefixCalls   int
	fullNameCalls int
	readCalls     int
}

func (s *stubEnv) env() brewEnv {
	return brewEnv{
		executablePath: func() (string, error) { return s.exePath, s.exeErr },
		evalSymlinks: func(p string) (string, error) {
			if s.resolveErr != nil {
				return "", s.resolveErr
			}
			if s.resolved != "" {
				return s.resolved, nil
			}
			return p, nil
		},
		lookPath: func(string) (string, error) {
			if s.brewPath == "" {
				return "", exec.ErrNotFound
			}
			return s.brewPath, nil
		},
		brewPrefix: func(context.Context, string) (string, error) {
			s.prefixCalls++
			return s.prefix, s.prefixErr
		},
		fullFormulaName: func(context.Context, string, string) (string, error) {
			s.fullNameCalls++
			return s.fullName, s.fullNameErr
		},
		readFile: func(path string) ([]byte, error) {
			s.readCalls++
			body, ok := s.receipts[path]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return []byte(body), nil
		},
	}
}

// receipt builds an INSTALL_RECEIPT.json body naming the given tap.
func receipt(tap string) string {
	return `{"homebrew_version":"4.6.1","source":{"tap":"` + tap +
		`","spec":"stable","versions":{"stable":"1.3.2"}}}`
}

const kegReceiptPath = "/opt/homebrew/Cellar/fir/1.3.2/INSTALL_RECEIPT.json"

func TestDetectBrewInstall(t *testing.T) {
	tests := []struct {
		name        string
		stub        stubEnv
		wantNil     bool
		wantPrefix  string
		wantFormula string
		wantBrew    string
	}{
		{
			name: "brew symlink resolving into the cellar",
			stub: stubEnv{
				exePath:  "/opt/homebrew/bin/fir",
				resolved: "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath: "/opt/homebrew/bin/brew",
				fullName: "kfet/ai/fir",
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "kfet/ai/fir",
			wantBrew:    "/opt/homebrew/bin/brew",
		},
		{
			name: "standard prefix with brew missing from PATH still detected",
			stub: stubEnv{
				exePath:  "/usr/local/Cellar/fir/1.3.2/bin/fir",
				brewPath: "",
			},
			wantPrefix:  "/usr/local",
			wantFormula: "fir", // no brew to resolve the tap
			wantBrew:    "",
		},
		{
			name: "brew info failure falls back to the keg name",
			stub: stubEnv{
				exePath:     "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath:    "/opt/homebrew/bin/brew",
				fullNameErr: errors.New("boom"),
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "fir",
			wantBrew:    "/opt/homebrew/bin/brew",
		},
		{
			name: "non-standard prefix confirmed by brew --prefix",
			stub: stubEnv{
				exePath:  "/Users/kfet/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath: "/Users/kfet/homebrew/bin/brew",
				prefix:   "/Users/kfet/homebrew",
				fullName: "kfet/ai/fir",
			},
			wantPrefix:  "/Users/kfet/homebrew",
			wantFormula: "kfet/ai/fir",
			wantBrew:    "/Users/kfet/homebrew/bin/brew",
		},
		{
			name: "linuxbrew",
			stub: stubEnv{
				exePath:  "/home/linuxbrew/.linuxbrew/bin/fir",
				resolved: "/home/linuxbrew/.linuxbrew/Cellar/fir/1.3.2/bin/fir",
				brewPath: "/home/linuxbrew/.linuxbrew/bin/brew",
				fullName: "kfet/ai/fir",
			},
			wantPrefix:  "/home/linuxbrew/.linuxbrew",
			wantFormula: "kfet/ai/fir",
			wantBrew:    "/home/linuxbrew/.linuxbrew/bin/brew",
		},

		// --- install receipt resolution (preferred over `brew info`) ---
		{
			name: "receipt names the tap",
			stub: stubEnv{
				exePath:  "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath: "/opt/homebrew/bin/brew",
				fullName: "kfet/fir/fir", // must not win over the receipt
				receipts: map[string]string{kegReceiptPath: receipt("kfet/ai")},
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "kfet/ai/fir",
			wantBrew:    "/opt/homebrew/bin/brew",
		},
		{
			// Production shape: os.Executable() reports the brew symlink, so
			// the receipt path must come from the *resolved* Cellar path.
			name: "receipt is read from the resolved symlink target",
			stub: stubEnv{
				exePath:  "/opt/homebrew/bin/fir",
				resolved: "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath: "/opt/homebrew/bin/brew",
				fullName: "kfet/fir/fir", // must not win over the receipt
				receipts: map[string]string{kegReceiptPath: receipt("kfet/ai")},
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "kfet/ai/fir",
			wantBrew:    "/opt/homebrew/bin/brew",
		},
		{
			// The reported regression: two taps provide `fir`, so
			// `brew info --json=v2 --formula fir` exits non-zero. The receipt
			// still resolves the right fully-qualified name.
			name: "receipt wins when brew info is ambiguous",
			stub: stubEnv{
				exePath:     "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath:    "/opt/homebrew/bin/brew",
				fullNameErr: errors.New("Error: Formulae found in multiple taps: * kfet/fir/fir * kfet/ai/fir"),
				receipts:    map[string]string{kegReceiptPath: receipt("kfet/ai")},
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "kfet/ai/fir",
			wantBrew:    "/opt/homebrew/bin/brew",
		},
		{
			name: "receipt works with brew missing from PATH",
			stub: stubEnv{
				exePath:  "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath: "",
				receipts: map[string]string{kegReceiptPath: receipt("kfet/ai")},
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "kfet/ai/fir",
			wantBrew:    "",
		},
		{
			name: "missing receipt falls back to brew info",
			stub: stubEnv{
				exePath:  "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath: "/opt/homebrew/bin/brew",
				fullName: "kfet/ai/fir",
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "kfet/ai/fir",
			wantBrew:    "/opt/homebrew/bin/brew",
		},
		{
			name: "malformed receipt falls back to brew info",
			stub: stubEnv{
				exePath:  "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath: "/opt/homebrew/bin/brew",
				fullName: "kfet/ai/fir",
				receipts: map[string]string{kegReceiptPath: "{not json"},
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "kfet/ai/fir",
			wantBrew:    "/opt/homebrew/bin/brew",
		},
		{
			name: "empty tap in receipt falls back to brew info",
			stub: stubEnv{
				exePath:  "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath: "/opt/homebrew/bin/brew",
				fullName: "kfet/ai/fir",
				receipts: map[string]string{kegReceiptPath: receipt("")},
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "kfet/ai/fir",
			wantBrew:    "/opt/homebrew/bin/brew",
		},
		{
			name: "malformed tap in receipt falls back to brew info",
			stub: stubEnv{
				exePath:  "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath: "/opt/homebrew/bin/brew",
				fullName: "kfet/ai/fir",
				receipts: map[string]string{kegReceiptPath: receipt("kfet")},
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "kfet/ai/fir",
			wantBrew:    "/opt/homebrew/bin/brew",
		},
		{
			name: "no receipt and no brew leaves the bare keg name",
			stub: stubEnv{
				exePath:  "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath: "",
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "fir",
			wantBrew:    "",
		},
		{
			name: "no receipt and brew info erroring leaves the bare keg name",
			stub: stubEnv{
				exePath:     "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath:    "/opt/homebrew/bin/brew",
				fullNameErr: errors.New("boom"),
			},
			wantPrefix:  "/opt/homebrew",
			wantFormula: "fir",
			wantBrew:    "/opt/homebrew/bin/brew",
		},

		// --- conservative: everything below must NOT be treated as brew ---
		{
			name:    "plain local install",
			stub:    stubEnv{exePath: "/home/kfet/.local/bin/fir"},
			wantNil: true,
		},
		{
			name:    "go install",
			stub:    stubEnv{exePath: "/home/kfet/go/bin/fir", brewPath: "/usr/bin/brew"},
			wantNil: true,
		},
		{
			name: "impostor Cellar under an unconfirmed prefix",
			stub: stubEnv{
				exePath:  "/tmp/Cellar/fir/1.3.2/bin/fir",
				brewPath: "/opt/homebrew/bin/brew",
				prefix:   "/opt/homebrew", // brew disagrees → not brew-managed
			},
			wantNil: true,
		},
		{
			name: "impostor Cellar with no brew at all",
			stub: stubEnv{
				exePath:  "/tmp/Cellar/fir/1.3.2/bin/fir",
				brewPath: "",
			},
			wantNil: true,
		},
		{
			name: "non-standard prefix and brew --prefix errors",
			stub: stubEnv{
				exePath:   "/Users/kfet/homebrew/Cellar/fir/1.3.2/bin/fir",
				brewPath:  "/Users/kfet/homebrew/bin/brew",
				prefixErr: errors.New("brew broken"),
			},
			wantNil: true,
		},
		{
			name: "unresolvable symlink is not brew",
			stub: stubEnv{
				exePath:    "/opt/homebrew/bin/fir",
				resolveErr: errors.New("dangling symlink"),
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := tt.stub
			inst, err := detectBrewInstall(context.Background(), stub.env())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if inst != nil {
					t.Fatalf("expected no brew install, got %+v", inst)
				}
				return
			}
			if inst == nil {
				t.Fatal("expected a brew install, got nil")
			}
			if inst.Prefix != tt.wantPrefix {
				t.Errorf("Prefix = %q, want %q", inst.Prefix, tt.wantPrefix)
			}
			if inst.Formula != tt.wantFormula {
				t.Errorf("Formula = %q, want %q", inst.Formula, tt.wantFormula)
			}
			if inst.BrewPath != tt.wantBrew {
				t.Errorf("BrewPath = %q, want %q", inst.BrewPath, tt.wantBrew)
			}
		})
	}
}

// The cheap path gate must hold: a non-Cellar path may not shell out to brew.
func TestDetectBrewInstall_NonCellarPathNeverExecsBrew(t *testing.T) {
	stub := stubEnv{exePath: "/home/kfet/.local/bin/fir", brewPath: "/usr/bin/brew"}
	if _, err := detectBrewInstall(context.Background(), stub.env()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.prefixCalls != 0 || stub.fullNameCalls != 0 {
		t.Errorf("execed brew on a non-Cellar path: prefix=%d fullName=%d",
			stub.prefixCalls, stub.fullNameCalls)
	}
	if stub.readCalls != 0 {
		t.Errorf("read a receipt on a non-Cellar path: %d reads", stub.readCalls)
	}
}

// A valid receipt is authoritative, so `brew info` must not be execed at all.
func TestDetectBrewInstall_ReceiptSkipsBrewInfoExec(t *testing.T) {
	stub := stubEnv{
		exePath:  "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
		brewPath: "/opt/homebrew/bin/brew",
		receipts: map[string]string{kegReceiptPath: receipt("kfet/ai")},
	}
	inst, err := detectBrewInstall(context.Background(), stub.env())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst == nil || inst.Formula != "kfet/ai/fir" {
		t.Fatalf("got %+v, want Formula kfet/ai/fir", inst)
	}
	if stub.fullNameCalls != 0 {
		t.Errorf("brew info called %d times despite a valid receipt", stub.fullNameCalls)
	}
}

// A standard prefix is trusted outright, so `brew --prefix` is not needed.
func TestDetectBrewInstall_StandardPrefixSkipsPrefixExec(t *testing.T) {
	stub := stubEnv{
		exePath:  "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
		brewPath: "/opt/homebrew/bin/brew",
		fullName: "kfet/ai/fir",
	}
	if _, err := detectBrewInstall(context.Background(), stub.env()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.prefixCalls != 0 {
		t.Errorf("brew --prefix called %d times for a standard prefix", stub.prefixCalls)
	}
}

func TestDetectBrewInstall_ExecutablePathError(t *testing.T) {
	stub := stubEnv{exeErr: errors.New("nope")}
	if _, err := detectBrewInstall(context.Background(), stub.env()); err == nil {
		t.Error("expected an error when the executable path cannot be located")
	}
}

// -----------------------------------------------------------------------------
// UpgradeViaBrew
// -----------------------------------------------------------------------------

func TestUpgradeViaBrew_NilInstall(t *testing.T) {
	if err := UpgradeViaBrew(context.Background(), nil, nil, nil); err == nil {
		t.Error("expected an error for a nil install")
	}
}

// The one case the brief requires us to refuse rather than silently corrupt.
func TestUpgradeViaBrew_BrewMissingFromPATH(t *testing.T) {
	inst := &BrewInstall{
		ExePath: "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
		Prefix:  "/opt/homebrew",
		Formula: "fir",
	}
	err := UpgradeViaBrew(context.Background(), inst, nil, nil)
	if err == nil {
		t.Fatal("expected an error when brew is not on PATH")
	}
	if !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("error should explain the missing brew, got: %v", err)
	}
	if !strings.Contains(err.Error(), inst.ExePath) {
		t.Errorf("error should name the executable, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// install receipt
// -----------------------------------------------------------------------------

func TestReceiptFormulaName(t *testing.T) {
	// Trimmed shape of a real INSTALL_RECEIPT.json.
	const real = `{"homebrew_version":"4.6.1","used_options":[],
		"source":{"path":"/opt/homebrew/Library/Taps/kfet/homebrew-ai/Formula/fir.rb",
		"tap":"kfet/ai","tap_git_head":"abc123","spec":"stable",
		"versions":{"stable":"1.3.2","head":null}}}`

	tests := []struct {
		name   string
		body   string // "" means the receipt file is absent
		absent bool
		want   string
		wantOK bool
	}{
		{name: "real receipt", body: real, want: "kfet/ai/fir", wantOK: true},
		{name: "minimal receipt", body: receipt("kfet/ai"), want: "kfet/ai/fir", wantOK: true},
		{name: "tap is padded", body: receipt("  kfet/ai  "), want: "kfet/ai/fir", wantOK: true},
		{name: "core tap", body: receipt("homebrew/core"), want: "homebrew/core/fir", wantOK: true},
		{name: "missing receipt", absent: true},
		{name: "empty file", body: ""},
		{name: "malformed json", body: "{not json"},
		{name: "no source key", body: `{"homebrew_version":"4.6.1"}`},
		{name: "empty tap", body: receipt("")},
		{name: "tap has no owner", body: receipt("fir")},
		{name: "tap has too many parts", body: receipt("kfet/ai/extra")},
		{name: "tap is a leading slash", body: receipt("/ai")},
		{name: "tap is a trailing slash", body: receipt("kfet/")},
		{name: "tap has dot components", body: receipt("../..")},
		{name: "tap owner starts with a dash", body: receipt("-x/ai")},
		{name: "tap repo starts with a dash", body: receipt("kfet/-x")},
		{name: "tap has whitespace inside", body: receipt("kfet/a i")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			read := func(path string) ([]byte, error) {
				if path != kegReceiptPath {
					t.Errorf("read %q, want %q", path, kegReceiptPath)
				}
				if tt.absent {
					return nil, fs.ErrNotExist
				}
				return []byte(tt.body), nil
			}
			got, ok := receiptFormulaName(read, "/opt/homebrew/Cellar/fir/1.3.2", "fir")
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tt.wantOK, got)
			}
			if ok && got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// Nothing may be read when the caller has no keg dir, keg name, or reader.
func TestReceiptFormulaName_NoInput(t *testing.T) {
	read := func(string) ([]byte, error) {
		t.Fatal("readFile must not be called")
		return nil, nil
	}
	if _, ok := receiptFormulaName(nil, "/opt/homebrew/Cellar/fir/1.3.2", "fir"); ok {
		t.Error("expected failure with a nil reader")
	}
	if _, ok := receiptFormulaName(read, "", "fir"); ok {
		t.Error("expected failure with an empty keg dir")
	}
	if _, ok := receiptFormulaName(read, "/opt/homebrew/Cellar/fir/1.3.2", ""); ok {
		t.Error("expected failure with an empty keg name")
	}
}

// -----------------------------------------------------------------------------
// brew info JSON parsing
// -----------------------------------------------------------------------------

func TestBrewInfoV2_Unmarshal(t *testing.T) {
	// Trimmed shape of real `brew info --json=v2 --formula fir` output.
	const payload = `{"formulae":[{"name":"fir","full_name":"kfet/ai/fir","tap":"kfet/ai"}],"casks":[]}`
	var info brewInfoV2
	if err := json.Unmarshal([]byte(payload), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(info.Formulae) != 1 || info.Formulae[0].FullName != "kfet/ai/fir" {
		t.Errorf("got %+v, want one formula named kfet/ai/fir", info.Formulae)
	}
}
