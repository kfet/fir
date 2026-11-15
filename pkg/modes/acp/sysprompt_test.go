package acp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func spParam(text string) map[string]any {
	return map[string]any{"mode": "append", "content": []any{map[string]any{"type": "text", "text": text}}}
}

func TestInitialize_AdvertisesSystemPrompt(t *testing.T) {
	pa := &firAgent{sessions: make(map[string]*firSession)}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	params, _ := json.Marshal(acpsdk.InitializeRequest{ProtocolVersion: 1})
	resp, rerr := handler(context.Background(), "initialize", params)
	if rerr != nil {
		t.Fatal(rerr)
	}
	raw, _ := json.Marshal(resp)
	var got struct {
		AgentCapabilities struct {
			SessionCapabilities struct {
				SystemPrompt *map[string]any `json:"systemPrompt"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	sp := got.AgentCapabilities.SessionCapabilities.SystemPrompt
	if sp == nil || *sp == nil {
		t.Fatalf("systemPrompt capability missing: %s", raw)
	}
	if _, ok := (*sp)["mode"]; ok {
		t.Fatalf("override must not be advertised: %s", raw)
	}
}

func TestParseSystemPrompt(t *testing.T) {
	if _, present, err := parseSystemPrompt([]byte(`{"cwd":"/"}`)); present || err != nil {
		t.Fatalf("absent: present=%v err=%v", present, err)
	}
	if _, _, err := parseSystemPrompt([]byte(`{"systemPrompt":{"mode":"override","content":[{"type":"text","text":"x"}]}}`)); err == nil {
		t.Fatal("override accepted")
	}
	if _, _, err := parseSystemPrompt([]byte(`{"systemPrompt":{"mode":"append","content":[]}}`)); err == nil {
		t.Fatal("empty content accepted")
	}
	text, present, err := parseSystemPrompt([]byte(`{"systemPrompt":{"mode":"append","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}`))
	if err != nil || !present || text != "a\n\nb" {
		t.Fatalf("got %q %v %v", text, present, err)
	}
}

func TestSessionNew_RejectsBadSystemPrompt(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	params, _ := json.Marshal(map[string]any{"cwd": cwd, "mcpServers": []any{},
		"systemPrompt": map[string]any{"mode": "override", "content": []any{}}})
	if _, rerr := handler(context.Background(), "session/new", params); rerr == nil || rerr.Code != -32602 {
		t.Fatalf("want InvalidParams, got %+v", rerr)
	}
}

const spMarker = "CLIENT-SP-MARKER-7f3a"

func TestSystemPrompt_NewAndLoadNoDuplication(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	ctx := context.Background()

	params, _ := json.Marshal(map[string]any{"cwd": cwd, "mcpServers": []any{}, "systemPrompt": spParam(spMarker)})
	resp, rerr := handler(ctx, "session/new", params)
	if rerr != nil {
		t.Fatal(rerr)
	}
	sid, _ := resp.(map[string]any)["sessionId"].(string)
	cleanupChild(t, pa, sid)
	entry := pa.lookupSession(sid)
	if n := strings.Count(entry.session.GetSystemPrompt(), spMarker); n != 1 {
		t.Fatalf("after new: %d copies", n)
	}

	for _, m := range []string{"session/load", "session/resume", "session/load"} {
		params, _ = json.Marshal(map[string]any{"sessionId": sid, "cwd": cwd, "mcpServers": []any{}, "systemPrompt": spParam(spMarker)})
		if _, rerr := handler(ctx, m, params); rerr != nil {
			t.Fatalf("%s: %+v", m, rerr)
		}
		entry = pa.lookupSession(sid)
		if n := strings.Count(entry.session.GetSystemPrompt(), spMarker); n != 1 {
			t.Fatalf("after %s: %d copies", m, n)
		}
	}

	// A different prompt on load replaces the earlier one.
	params, _ = json.Marshal(map[string]any{"sessionId": sid, "cwd": cwd, "mcpServers": []any{}, "systemPrompt": spParam("REPLACED-PROMPT")})
	if _, rerr := handler(ctx, "session/load", params); rerr != nil {
		t.Fatal(rerr)
	}
	got := pa.lookupSession(sid).session.GetSystemPrompt()
	if strings.Contains(got, spMarker) || strings.Count(got, "REPLACED-PROMPT") != 1 {
		t.Fatalf("load did not replace prompt")
	}
}

func TestSystemPrompt_Fork(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	writeForkParent(t, pa.agentDir, cwd)
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	params, _ := json.Marshal(map[string]any{"sessionId": "parent-uuid", "cwd": cwd, "mcpServers": []any{}, "systemPrompt": spParam(spMarker)})
	resp, rerr := handler(context.Background(), "session/fork", params)
	if rerr != nil {
		t.Fatal(rerr)
	}
	sid, _ := resp.(map[string]any)["sessionId"].(string)
	cleanupChild(t, pa, sid)
	if n := strings.Count(pa.lookupSession(sid).session.GetSystemPrompt(), spMarker); n != 1 {
		t.Fatalf("fork: %d copies", n)
	}
}

func TestSystemPrompt_SurvivesReapRehydrate(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	const sid = "acp-sid-sp"
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd})
	entry.session.SessionStore.NewSession(nil)
	entry.session.SetClientSystemPrompt(spMarker)
	reapNow(t, pa, sid, entry)
	entry = rehydrate(t, pa, sid)
	if n := strings.Count(entry.session.GetSystemPrompt(), spMarker); n != 1 {
		t.Fatalf("after rehydrate: %d copies", n)
	}
}
