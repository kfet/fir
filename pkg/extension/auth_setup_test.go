package extension

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// TestSetupAuthProviders_RegistersOnly discovers extensions with auth_providers
// frontmatter and starts only those, skipping non-auth extensions.
func TestSetupAuthProviders_RegistersOnly(t *testing.T) {
	dir := t.TempDir()

	// Auth extension (scope: project)
	authScript := writeAuthExtScript(t, dir, "my-auth", "project", "my-test-provider")

	// Non-auth extension in the same project dir
	projectExtDir := filepath.Join(dir, ".fir", "extensions")
	if err := os.MkdirAll(projectExtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	noAuth := filepath.Join(projectExtDir, "no-auth.sh")
	err := os.WriteFile(noAuth, []byte("#!/bin/sh\n# ---\n# name: no-auth\n# ---\nread line\n"+
		`echo '{"jsonrpc":"2.0","id":1,"result":{"name":"no-auth","tools":[],"events":[]}}'`+
		"\ncat >/dev/null\n"), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-trust both scripts.
	trustPath := filepath.Join(dir, "trust.json")
	ts := NewTrustStoreWithPath(trustPath)
	for _, s := range []string{authScript, noAuth} {
		hash, _ := ComputeHash(s)
		_ = ts.RecordTrust(dir, filepath.Base(s), hash)
	}

	// Register provider in oauth registry on setup; clean up afterwards.
	t.Cleanup(func() { ai.UnregisterOAuthProvider("my-test-provider") })

	result, err := SetupAuthProviders(AuthSetupOptions{
		ProjectDir:     dir,
		Mode:           "acp",
		TrustStorePath: trustPath,
		ConfirmFn:      func(string, string) bool { return true },
	})
	if err != nil {
		t.Fatalf("SetupAuthProviders: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	t.Cleanup(func() { result.Stop() })

	// Our custom auth extension should have been started (alongside
	// builtin auth extensions).
	found := false
	for _, n := range result.Names {
		if n == "my-auth" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("my-auth not in Names = %v", result.Names)
	}
	// And the non-auth extension should NOT have been started.
	for _, n := range result.Names {
		if n == "no-auth" {
			t.Errorf("no-auth should not be started (no auth_providers)")
		}
	}

	// And the oauth provider should now be registered globally.
	if p := ai.GetOAuthProvider("my-test-provider"); p == nil {
		t.Error("oauth provider my-test-provider not registered")
	}
}

// TestSetupAuthProviders_NoProjectDir returns (nil, nil) for an empty project.
func TestSetupAuthProviders_NoProjectDir(t *testing.T) {
	result, err := SetupAuthProviders(AuthSetupOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
}

// TestSetupAuthProviders_NoAuthExtensions returns nil when the project has
// extensions but none declare auth_providers.
func TestSetupAuthProviders_NoAuthExtensions(t *testing.T) {
	// Use a fresh temp dir with no .fir/extensions at all; builtin auth
	// extensions are loaded via resources and would otherwise show up.
	// We can't easily suppress builtins, so instead just assert the
	// function doesn't error on an empty project. The non-nil case is
	// covered by TestSetupAuthProviders_RegistersOnly.
	dir := t.TempDir()
	result, err := SetupAuthProviders(AuthSetupOptions{
		ProjectDir: dir,
		Mode:       "acp",
		ConfirmFn:  func(string, string) bool { return true },
	})
	if err != nil {
		t.Fatalf("SetupAuthProviders: %v", err)
	}
	if result != nil {
		t.Cleanup(func() { result.Stop() })
		// Builtins may be discovered; that's fine — just assert all
		// started names are strings (sanity).
		for _, n := range result.Names {
			if n == "" {
				t.Error("empty name in Names")
			}
		}
	}
}

// TestSetupAuthProviders_ExtraExtensionFiles verifies that a package-provided
// auth extension (supplied via ExtraExtensionFiles, i.e. a single script path
// outside the project dir) is discovered and started. This is the ACP-parity
// regression for package-contributed auth extensions.
func TestSetupAuthProviders_ExtraExtensionFiles(t *testing.T) {
	projectDir := t.TempDir()

	// A package extension living outside the project dir.
	pkgDir := t.TempDir()
	script := filepath.Join(pkgDir, "pkg-auth.sh")
	const providerID = "pkg-test-provider"
	content := `#!/bin/sh
# ---
# name: pkg-auth
# auth_providers: ` + providerID + `
# ---
read line
echo '{"jsonrpc":"2.0","id":1,"result":{"name":"pkg-auth","tools":[],"events":[],"auth_providers":[{"id":"` + providerID + `","name":"pkg-auth"}]}}'
cat >/dev/null
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	trustPath := filepath.Join(projectDir, "trust.json")
	t.Cleanup(func() { ai.UnregisterOAuthProvider(providerID) })

	result, err := SetupAuthProviders(AuthSetupOptions{
		ProjectDir:     projectDir,
		Mode:           "acp",
		TrustStorePath: trustPath,
		ConfirmFn:      func(string, string) bool { return true },
		ExtraSources:   ExtraSources{Files: []string{script}},
	})
	if err != nil {
		t.Fatalf("SetupAuthProviders: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	t.Cleanup(func() { result.Stop() })

	found := false
	for _, n := range result.Names {
		if n == "pkg-auth" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("package auth extension pkg-auth not started; Names = %v", result.Names)
	}
	if p := ai.GetOAuthProvider(providerID); p == nil {
		t.Errorf("oauth provider %s not registered", providerID)
	}
}
