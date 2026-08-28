package update

import (
	"context"
	"encoding/json"
	"errors"
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
	}{
		{
			name:       "macos arm64 cellar",
			path:       "/opt/homebrew/Cellar/fir/1.3.2/bin/fir",
			wantOK:     true,
			wantPrefix: "/opt/homebrew",
			wantKeg:    "fir",
		},
		{
			name:       "macos amd64 cellar",
			path:       "/usr/local/Cellar/fir/1.3.2/bin/fir",
			wantOK:     true,
			wantPrefix: "/usr/local",
			wantKeg:    "fir",
		},
		{
			name:       "linuxbrew cellar",
			path:       "/home/linuxbrew/.linuxbrew/Cellar/fir/1.3.2/bin/fir",
			wantOK:     true,
			wantPrefix: "/home/linuxbrew/.linuxbrew",
			wantKeg:    "fir",
		},
		{
			name:       "custom prefix cellar",
			path:       "/Users/kfet/homebrew/Cellar/fir/1.3.2/bin/fir",
			wantOK:     true,
			wantPrefix: "/Users/kfet/homebrew",
			wantKeg:    "fir",
		},
		{
			name:       "uncleaned path",
			path:       "/opt/homebrew/../homebrew/Cellar/fir/1.3.2/bin/fir",
			wantOK:     true,
			wantPrefix: "/opt/homebrew",
			wantKeg:    "fir",
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
			prefix, keg, ok := splitCellarPath(tt.path)
			if ok != tt.wantOK {
				t.Fatalf("splitCellarPath(%q) ok = %v, want %v", tt.path, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if prefix != tt.wantPrefix {
				t.Errorf("prefix = %q, want %q", prefix, tt.wantPrefix)
			}
			if keg != tt.wantKeg {
				t.Errorf("keg = %q, want %q", keg, tt.wantKeg)
			}
		})
	}
}

// A "Cellar" directory nested deeper than the prefix must still resolve to the
// segment before it, so confirmation against a real prefix is what rejects it.
func TestSplitCellarPath_NestedCellarUsesFirstMatch(t *testing.T) {
	prefix, keg, ok := splitCellarPath("/tmp/Cellar/fir/1.0.0/bin/fir")
	if !ok {
		t.Fatal("expected a match")
	}
	if prefix != "/tmp" || keg != "fir" {
		t.Errorf("got (%q, %q), want (/tmp, fir)", prefix, keg)
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

	prefixCalls   int
	fullNameCalls int
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
	}
}

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
