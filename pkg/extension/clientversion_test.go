package extension

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/models"
)

// fixedLookup is a client-version lookup over a literal map.
func fixedLookup(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestExpandClientVersions(t *testing.T) {
	lookup := fixedLookup(map[string]string{"claudeCode": "2.1.280", "other": "9"})
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no placeholder", "claude-cli/x (external, cli)", "claude-cli/x (external, cli)"},
		{"single", "claude-cli/{clientVersion.claudeCode} (external, cli)", "claude-cli/2.1.280 (external, cli)"},
		{"multiple, different keys", "{clientVersion.claudeCode}/{clientVersion.other}", "2.1.280/9"},
		{"repeated key", "{clientVersion.claudeCode} {clientVersion.claudeCode}", "2.1.280 2.1.280"},
		// An unknown key is left LITERAL: a greppable, self-describing
		// failure beats a silently malformed header.
		{"unknown key", "claude-cli/{clientVersion.nope}", "claude-cli/{clientVersion.nope}"},
		{"malformed placeholder", "{clientVersion.}", "{clientVersion.}"},
		{"not a placeholder", "{state}", "{state}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandClientVersions(tc.in, lookup); got != tc.want {
				t.Fatalf("expandClientVersions(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	if got := expandClientVersions("{clientVersion.claudeCode}", nil); got != "{clientVersion.claudeCode}" {
		t.Fatalf("nil lookup must leave the template alone, got %q", got)
	}
}

func TestExpandClientVersionsWarnsOncePerKey(t *testing.T) {
	// The warning is deduplicated per key per process; exercising the path
	// twice must not panic or change the result, and the second call must
	// find the key already recorded.
	key := "keyUsedOnlyByThisTest"
	tmpl := "x/{clientVersion." + key + "}"
	lookup := fixedLookup(nil)
	for i := 0; i < 2; i++ {
		if got := expandClientVersions(tmpl, lookup); got != tmpl {
			t.Fatalf("call %d: got %q", i, got)
		}
	}
	if _, seen := unknownClientVersionKeys.Load(key); !seen {
		t.Fatal("unknown key must be recorded so the warning fires only once")
	}
}

func TestExpandClientVersionHeadersValuesOnly(t *testing.T) {
	// Header NAMES are never expanded — the extension owns them, and that is
	// half of why a remote scalar is safe here at all.
	in := map[string]string{
		"User-Agent":                 "claude-cli/{clientVersion.claudeCode} (external, cli)",
		"{clientVersion.claudeCode}": "literal-name",
	}
	out := expandClientVersionHeaders(in, fixedLookup(map[string]string{"claudeCode": "2.1.280"}))
	if out["User-Agent"] != "claude-cli/2.1.280 (external, cli)" {
		t.Fatalf("value not expanded: %q", out["User-Agent"])
	}
	if _, ok := out["{clientVersion.claudeCode}"]; !ok {
		t.Fatalf("header name must be untouched, got %v", out)
	}
	// The input map must not be mutated in place.
	if in["User-Agent"] != "claude-cli/{clientVersion.claudeCode} (external, cli)" {
		t.Fatal("expansion must not mutate the caller's map")
	}
	if got := expandClientVersionHeaders(nil, fixedLookup(nil)); got != nil {
		t.Fatalf("nil headers = %v", got)
	}
}

func TestBridgeClientVersionFallsBackToFloor(t *testing.T) {
	// A bridge with no host API (e.g. a bare test bridge, or `fir login`
	// before any session exists) answers with the compiled-in floor.
	b := NewBridge(nil, &InitResult{Name: "test-ext"})
	floor := models.DefaultClientVersions().Get(models.ClientVersionKeyClaudeCode)
	if got := b.clientVersion(models.ClientVersionKeyClaudeCode); got != floor {
		t.Fatalf("clientVersion = %q, want floor %q", got, floor)
	}

	api := newMockAPI()
	api.clientVersions = map[string]string{models.ClientVersionKeyClaudeCode: "99.0.0"}
	b.SetAPI(api)
	if got := b.clientVersion(models.ClientVersionKeyClaudeCode); got != "99.0.0" {
		t.Fatalf("clientVersion = %q, want the host's 99.0.0", got)
	}
	if got := b.clientVersionsParam()[models.ClientVersionKeyClaudeCode]; got != "99.0.0" {
		t.Fatalf("clientVersionsParam = %q", got)
	}
}

func TestTokenClientExpandsHeaderValues(t *testing.T) {
	b := NewBridge(nil, &InitResult{Name: "test-ext"})
	api := newMockAPI()
	api.clientVersions = map[string]string{models.ClientVersionKeyClaudeCode: "3.2.1"}
	b.SetAPI(api)

	p := &genericAuthProvider{bridge: b, spec: AuthProviderSpec{ID: "x"}}
	fl := &OAuthFlowSpec{
		TokenURL:     "https://example.invalid/token",
		TokenHeaders: map[string]string{"User-Agent": "claude-cli/{clientVersion.claudeCode} (external, cli)"},
	}
	got := p.tokenClient(fl).Headers.Get("User-Agent")
	if got != "claude-cli/3.2.1 (external, cli)" {
		t.Fatalf("User-Agent = %q", got)
	}
}

// serveHook answers a single JSON-RPC request on extCodec with result.
func serveHook(t *testing.T, extCodec *Codec, result any) {
	t.Helper()
	go func() {
		msg, err := extCodec.ReadMessage()
		if err != nil {
			return
		}
		req, ok := msg.(*Request)
		if !ok {
			return
		}
		_ = extCodec.WriteResponse(req.ID, result, nil)
	}()
}

func TestCallExtModifyModelsExpandsReturnedHeaders(t *testing.T) {
	b, extCodec := pipePair(&InitResult{Name: "test-ext"})
	api := newMockAPI()
	api.clientVersions = map[string]string{models.ClientVersionKeyClaudeCode: "4.5.6"}
	b.SetAPI(api)
	// The bridge's read loop is what delivers hook responses.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	serveHook(t, extCodec, map[string]any{
		"models": []map[string]any{{
			"id":       "claude-x",
			"provider": "anthropic",
			"headers": map[string]string{
				"user-agent": "claude-cli/{clientVersion.claudeCode} (external, cli)",
				"x-app":      "cli",
			},
		}},
	})

	out := b.callExtModifyModels("anthropic", &ai.OAuthCredentials{Access: "tok"}, nil)
	if len(out) != 1 {
		t.Fatalf("models = %v", out)
	}
	if got := out[0].Headers["user-agent"]; got != "claude-cli/4.5.6 (external, cli)" {
		t.Fatalf("user-agent = %q", got)
	}
	if got := out[0].Headers["x-app"]; got != "cli" {
		t.Fatalf("untouched header changed: %q", got)
	}
}

func TestCallExtListModelsPassesClientVersions(t *testing.T) {
	b, extCodec := pipePair(&InitResult{Name: "test-ext"})
	api := newMockAPI()
	api.clientVersions = map[string]string{models.ClientVersionKeyClaudeCode: "7.8.9"}
	b.SetAPI(api)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	go func() { _ = b.Run(runCtx, api) }()

	seen := make(chan map[string]any, 1)
	go func() {
		msg, err := extCodec.ReadMessage()
		if err != nil {
			return
		}
		req, ok := msg.(*Request)
		if !ok {
			return
		}
		var params map[string]any
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &params)
		}
		seen <- params
		_ = extCodec.WriteResponse(req.ID, map[string]any{"models": []string{"m"}}, nil)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := b.callExtListModels(ctx, "anthropic", &ai.OAuthCredentials{Access: "tok"}); err != nil {
		t.Fatal(err)
	}

	select {
	case params := <-seen:
		cv, ok := params["client_versions"].(map[string]any)
		if !ok {
			t.Fatalf("client_versions missing from %v", params)
		}
		if cv[models.ClientVersionKeyClaudeCode] != "7.8.9" {
			t.Fatalf("client_versions = %v", cv)
		}
	case <-ctx.Done():
		t.Fatal("hook never arrived")
	}
}
