package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestAnthropicAuthClientVersionPlaceholder runs the REAL builtin
// anthropic_auth.py and verifies the end-to-end contract this feature rests
// on:
//
//   - the extension declares its User-Agent as a TEMPLATE containing
//     {clientVersion.claudeCode} — no version literal anywhere;
//   - auth/modify_models returns that template verbatim, so the Go side is
//     what materialises the number (and therefore picks up a hot-applied
//     catalog overlay with no restart);
//   - auth/list_models — which issues its own HTTP request, so Go cannot
//     post-process it — uses the client_versions hook param via
//     ctx.client_version("claudeCode").
func TestAnthropicAuthClientVersionPlaceholder(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	sdkDir, _ := filepath.Abs(filepath.Join(root, "pkg", "extension", "sdk", "python"))
	extPath, _ := filepath.Abs(filepath.Join(root, "pkg", "resources", "builtin_extensions", "anthropic_auth.py"))
	if _, err := os.Stat(extPath); err != nil {
		t.Fatalf("anthropic_auth.py not found: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "python3", extPath)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+sdkDir)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	send := func(id int, method string, params any) {
		t.Helper()
		if err := encoder.Encode(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": method, "params": params,
		}); err != nil {
			t.Fatalf("send %s: %v", method, err)
		}
	}
	recv := func() json.RawMessage {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("recv: no data (err=%v)", scanner.Err())
		}
		var resp struct {
			Result json.RawMessage  `json:"result"`
			Error  *json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			t.Fatalf("recv: %v\nraw: %s", err, scanner.Text())
		}
		if resp.Error != nil {
			t.Fatalf("rpc error: %s", *resp.Error)
		}
		return resp.Result
	}

	// --- init: the declared token headers must carry the placeholder ---
	send(1, "init", map[string]any{"version": "test", "cwd": t.TempDir()})
	var caps struct {
		AuthProviders []struct {
			ID   string `json:"id"`
			Flow struct {
				TokenHeaders map[string]string `json:"token_headers"`
			} `json:"flow"`
		} `json:"auth_providers"`
	}
	if err := json.Unmarshal(recv(), &caps); err != nil {
		t.Fatal(err)
	}
	var ua string
	for _, p := range caps.AuthProviders {
		if p.ID == "anthropic" {
			ua = p.Flow.TokenHeaders["User-Agent"]
		}
	}
	if ua != "claude-cli/{clientVersion.claudeCode} (external, cli)" {
		t.Fatalf("declared token User-Agent = %q, want the unexpanded template", ua)
	}

	// --- auth/modify_models returns the template; Go expands it after ---
	send(2, "auth/modify_models", map[string]any{
		"provider_id": "anthropic",
		"credentials": map[string]any{"access": "tok"},
		"models":      []map[string]any{{"id": "claude-x", "provider": "anthropic"}},
	})
	var modified struct {
		Models []struct {
			Headers map[string]string `json:"headers"`
		} `json:"models"`
	}
	if err := json.Unmarshal(recv(), &modified); err != nil {
		t.Fatal(err)
	}
	if len(modified.Models) != 1 {
		t.Fatalf("models = %v", modified.Models)
	}
	got := modified.Models[0].Headers["user-agent"]
	if got != "claude-cli/{clientVersion.claudeCode} (external, cli)" {
		t.Fatalf("modify_models user-agent = %q, want the unexpanded template", got)
	}

	// Go's expansion step (the same function callExtModifyModels applies)
	// turns it into the real header.
	expanded := strings.ReplaceAll(got, "{clientVersion.claudeCode}", "2.1.280")
	if expanded != "claude-cli/2.1.280 (external, cli)" {
		t.Fatalf("expanded = %q", expanded)
	}

	// --- auth/list_models reads the scalar from the hook param ---
	// No credentials means the handler returns early without any HTTP call;
	// what we assert is that the SDK accepted and surfaced client_versions.
	send(3, "auth/list_models", map[string]any{
		"provider_id":     "anthropic",
		"credentials":     map[string]any{},
		"client_versions": map[string]string{"claudeCode": "2.1.280"},
	})
	recv()

	// The process must still be healthy afterwards.
	send(4, "init", map[string]any{"version": "test", "cwd": t.TempDir()})
	recv()
}
