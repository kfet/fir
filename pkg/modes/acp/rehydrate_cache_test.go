package acp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai/providers"
)

// stubAnthropic is an Anthropic Messages endpoint that records every request
// body and answers each with a one-word text reply.
type stubAnthropic struct {
	mu     sync.Mutex
	bodies [][]byte
}

const stubSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"model":"claude-stub","stop_reason":null,"usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5,"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

func (s *stubAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, stubSSE)
}

func (s *stubAnthropic) last(t *testing.T) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		t.Fatal("no request reached the provider")
	}
	return s.bodies[len(s.bodies)-1]
}

// stripCacheControl removes cache_control markers: they move to the newest
// message each turn and are not part of the cached prefix.
func stripCacheControl(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "cache_control")
		for k, e := range x {
			x[k] = stripCacheControl(e)
		}
	case []any:
		for i, e := range x {
			x[i] = stripCacheControl(e)
		}
	}
	return v
}

func decodeRequest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return stripCacheControl(req).(map[string]any)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func waitMCPTools(t *testing.T, e *firSession, prefix string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		found := false
		// UpdateTools holds the agent's tool lock: a safe read.
		e.session.Agent.UpdateTools(func(ts *agent.ToolSet) {
			for _, n := range ts.Names() {
				found = found || strings.HasPrefix(n, prefix)
			}
		})
		if found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s* tool advertised", prefix)
}

// TestReapRehydrate_RequestPrefixByteIdentical guards the rule that a request
// is built only from saved session state: after an idle reap and rehydrate,
// the next request repeats the previous one byte for byte (system, tools and
// every earlier message), so the provider's prompt cache hits. Covers
// injected [SYS_EXT] context (re-injected by session_start in the new
// process) and MCP tools (here a server that never connects after the
// rehydrate).
func TestReapRehydrate_RequestPrefixByteIdentical(t *testing.T) {
	providers.RegisterDefaultProviders()
	pa, cwd := newRehydrateAgent(t)
	ctx := context.Background()
	const sid = "acp-sid-cache"

	stub := &stubAnthropic{}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	models := `{"providers":{"stub":{"baseUrl":"` + srv.URL + `","apiKey":"k","api":"anthropic-messages",` +
		`"models":[{"id":"claude-stub"}]}}}`
	if err := os.WriteFile(filepath.Join(pa.agentDir, "models.json"), []byte(models), 0o600); err != nil {
		t.Fatal(err)
	}

	const injected = "reminders: none pending"
	// A config-file MCP server (not awaited by rehydrate). After the reap
	// it never finishes connecting, so the rehydrated request can only get
	// its tools from saved state.
	hold := filepath.Join(t.TempDir(), "hold")
	mcpCfg := map[string]any{"mcpServers": map[string]any{"relay": map[string]any{
		"command": os.Args[0],
		"env":     map[string]string{"MCP_TEST_SERVER": "1", "MCP_TEST_HOLD": hold},
	}}}
	if err := os.WriteFile(filepath.Join(pa.agentDir, "mcp.json"), []byte(mustJSON(t, mcpCfg)), 0o600); err != nil {
		t.Fatal(err)
	}
	pa.options.NoMCP = false
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd})
	if _, err := pa.SetSessionModel(ctx, SetSessionModelRequest{
		SessionId: acpsdk.SessionId(sid), ModelId: "stub/claude-stub"}); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	waitMCPTools(t, entry, "mcp__relay__")
	entry.session.PrependContext(injected)

	prompt := func(text string) map[string]any {
		t.Helper()
		if _, err := pa.Prompt(ctx, acpsdk.PromptRequest{
			SessionId: acpsdk.SessionId(sid),
			Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock(text)},
		}); err != nil {
			t.Fatalf("Prompt %q: %v", text, err)
		}
		return decodeRequest(t, stub.last(t))
	}
	before := prompt("first")

	reapNow(t, pa, sid, entry)
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	back := rehydrate(t, pa, sid)
	if mcpConnected(back, "relay") {
		t.Fatal("held MCP server connected")
	}
	// The new process runs session_start again, re-injecting its context.
	back.session.PrependContext(injected)
	after := prompt("second")

	for _, key := range []string{"system", "tools"} {
		if got, want := mustJSON(t, after[key]), mustJSON(t, before[key]); got != want {
			t.Errorf("%s changed after rehydrate:\n got %s\nwant %s", key, got, want)
		}
	}
	if !strings.Contains(mustJSON(t, before["tools"]), "mcp__relay__echo") {
		t.Errorf("MCP tool missing from request tools: %s", mustJSON(t, before["tools"]))
	}
	prev, _ := before["messages"].([]any)
	next, _ := after["messages"].([]any)
	if len(next) <= len(prev) {
		t.Fatalf("messages: got %d after rehydrate, want more than %d", len(next), len(prev))
	}
	if got, want := mustJSON(t, next[:len(prev)]), mustJSON(t, prev); got != want {
		t.Errorf("message prefix changed after rehydrate:\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(mustJSON(t, prev[0]), injected) {
		t.Errorf("injected context not first: %s", mustJSON(t, prev[0]))
	}
	if n := strings.Count(mustJSON(t, next), injected); n != 1 {
		t.Errorf("injected context appears %d times, want 1", n)
	}
	for _, k := range []string{"model", "max_tokens", "thinking"} {
		if !reflect.DeepEqual(after[k], before[k]) {
			t.Errorf("%s changed: %v -> %v", k, before[k], after[k])
		}
	}
}
