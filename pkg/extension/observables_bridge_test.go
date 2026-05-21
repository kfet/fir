package extension

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/fir/pkg/session/store"
)

// runBridgeWithStore wires a fresh Bridge backed by pipePair to a real
// (in-memory) ObservableStore, starts it, and returns the codec the
// "extension" side uses to drive RPCs plus the store the test can
// inspect.
func runBridgeWithStore(t *testing.T, extName string) (*Codec, *store.ObservableStore, func()) {
	t.Helper()
	caps := &InitResult{Name: extName}
	b, extCodec := pipePair(caps)
	s := store.NewObservableStore("")
	b.SetObservableStore(s)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = b.Run(ctx, newMockAPI()) }()

	return extCodec, s, cancel
}

// roundtrip is a tiny helper: send a request, drain its response.
func roundtrip(t *testing.T, codec *Codec, id int, method string, params any) *Response {
	t.Helper()
	if err := codec.WriteRequest(id, method, params); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	for {
		msg, err := codec.ReadMessage()
		if err != nil {
			t.Fatalf("read %s response: %v", method, err)
		}
		if resp, ok := msg.(*Response); ok {
			return resp
		}
		// Skip notifications etc.
	}
}

func TestBridge_PutObservable_StampsSource(t *testing.T) {
	codec, s, cancel := runBridgeWithStore(t, "myext")
	defer cancel()

	// Extension tries to write a card with a spoofed source field in
	// the params — the spoof must be ignored because the bridge stamps
	// source from b.caps.Name; the typed putObservableParams struct
	// has no Source field, so the spoof is dropped on JSON decode.
	resp := roundtrip(t, codec, 1, "put_observable", map[string]any{
		"key":      "active",
		"slug":     "3/8",
		"detail":   "step three",
		"source":   "plan",    // spoof attempt
		"entry_id": "spoofed", // spoof attempt
	})
	if resp.Error != nil {
		t.Fatalf("put_observable returned error: %v", resp.Error)
	}

	got := s.List()
	if len(got) != 1 {
		t.Fatalf("expected 1 card, got %d", len(got))
	}
	if got[0].Source != "myext" {
		t.Errorf("source spoofed: got %q, want myext", got[0].Source)
	}
	if got[0].EntryID != "" {
		t.Errorf("entry_id spoofed: got %q, want empty", got[0].EntryID)
	}
	if got[0].Key != "active" || got[0].Slug != "3/8" || got[0].Detail != "step three" {
		t.Errorf("payload mishandled: %#v", got[0])
	}
}

func TestBridge_PutObservable_RejectsEmptyKey(t *testing.T) {
	codec, s, cancel := runBridgeWithStore(t, "myext")
	defer cancel()

	resp := roundtrip(t, codec, 1, "put_observable", map[string]any{"key": "", "slug": "abc"})
	if resp.Error == nil {
		t.Fatal("expected error for empty key, got success")
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("store mutated despite error: %#v", got)
	}
}

func TestBridge_ClearObservable_RemovesByOwnSource(t *testing.T) {
	codec, s, cancel := runBridgeWithStore(t, "myext")
	defer cancel()

	// Seed a card for myext and another for a different source.
	s.Put("myext", "active", "1/3", "first", "")
	s.Put("foreign", "active", "f-1", "foreign", "")

	resp := roundtrip(t, codec, 1, "clear_observable", map[string]any{"key": "active"})
	if resp.Error != nil {
		t.Fatalf("clear_observable returned error: %v", resp.Error)
	}

	got := s.List()
	if len(got) != 1 {
		t.Fatalf("expected 1 card after clear, got %d: %#v", len(got), got)
	}
	if got[0].Source != "foreign" {
		t.Errorf("clear leaked across sources: %#v", got[0])
	}
}

func TestBridge_ClearObservable_RejectsEmptyKey(t *testing.T) {
	codec, _, cancel := runBridgeWithStore(t, "myext")
	defer cancel()

	resp := roundtrip(t, codec, 1, "clear_observable", map[string]any{"key": ""})
	if resp.Error == nil {
		t.Fatal("expected error for empty key, got success")
	}
}

// TestBridge_SetStatus_WritesObservableAndCallsCallback pins the design
// invariant: ctx.set_status is reimplemented as a thin wrapper over
// put_observable. The UI callback is also fired (downstream of Put) so
// the TUI footer continues to work.
func TestBridge_SetStatus_WritesObservableAndCallsCallback(t *testing.T) {
	caps := &InitResult{Name: "myext"}
	b, codec := pipePair(caps)
	s := store.NewObservableStore("")
	b.SetObservableStore(s)
	gotName := ""
	gotText := ""
	b.SetSetStatusFn(func(name, text string) {
		gotName = name
		gotText = text
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, newMockAPI()) }()

	resp := roundtrip(t, codec, 1, "set_status", map[string]any{"status": "working..."})
	if resp.Error != nil {
		t.Fatalf("set_status returned error: %v", resp.Error)
	}

	// Canonical write — must land as a footer card.
	cards := s.List()
	if len(cards) != 1 {
		t.Fatalf("expected 1 footer card, got %d", len(cards))
	}
	if cards[0].Source != "myext" || cards[0].Key != "footer" || cards[0].Slug != "working..." {
		t.Errorf("unexpected footer card: %#v", cards[0])
	}

	// Downstream UI callback — must also have fired.
	if gotName != "myext" || gotText != "working..." {
		t.Errorf("callback got (%q, %q); want (myext, working...)", gotName, gotText)
	}
}

func TestBridge_SetStatus_EmptyClearsFooterCard(t *testing.T) {
	caps := &InitResult{Name: "myext"}
	b, codec := pipePair(caps)
	s := store.NewObservableStore("")
	b.SetObservableStore(s)
	cleared := false
	b.SetSetStatusFn(func(name, text string) {
		if text == "" {
			cleared = true
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, newMockAPI()) }()

	if r := roundtrip(t, codec, 1, "set_status", map[string]any{"status": "x"}); r.Error != nil {
		t.Fatal(r.Error)
	}
	if r := roundtrip(t, codec, 2, "set_status", map[string]any{"status": ""}); r.Error != nil {
		t.Fatal(r.Error)
	}

	if got := s.List(); len(got) != 0 {
		t.Errorf("footer card not cleared: %#v", got)
	}
	if !cleared {
		t.Errorf("callback was not invoked with empty status")
	}
}

// TestBridge_PutObservable_PersistsThroughFile pins the "sidecar IS
// canonical" invariant: live readers and at-rest readers see the same
// file. A read directly off disk must match what List() returns.
func TestBridge_PutObservable_PersistsThroughFile(t *testing.T) {
	caps := &InitResult{Name: "myext"}
	b, codec := pipePair(caps)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl.cards")
	s := store.NewObservableStore(path)
	b.SetObservableStore(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, newMockAPI()) }()

	resp := roundtrip(t, codec, 1, "put_observable", map[string]any{
		"key": "active", "slug": "3/8", "detail": "go",
	})
	if resp.Error != nil {
		t.Fatalf("put_observable: %v", resp.Error)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk []store.Card
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatalf("decode cards file: %v\nraw:\n%s", err, data)
	}
	if len(disk) != 1 || disk[0].Source != "myext" || disk[0].Key != "active" {
		t.Fatalf("on-disk mismatch: %#v", disk)
	}
}

// TestBridge_CurrentEntryID_StampedDuringToolDispatch exercises the
// provenance invariant: a put_observable issued *during* a tool call
// gets EntryID stamped with the tool_call_id.
func TestBridge_CurrentEntryID_StampedDuringToolDispatch(t *testing.T) {
	caps := &InitResult{
		Name: "myext",
		Tools: []ToolSpec{{
			Name:        "demo",
			Description: "d",
			Parameters:  map[string]any{"type": "object"},
		}},
	}
	b, codec := pipePair(caps)
	s := store.NewObservableStore("")
	b.SetObservableStore(s)

	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	// Register the tool and invoke its Execute directly — this mimics
	// what the agent would do, with the bridge's activeToolCallID
	// populated for the lifetime of CallHook.
	b.RegisterTools(api)
	if len(api.toolsRegistered) != 1 {
		t.Fatalf("expected 1 tool registered, got %d", len(api.toolsRegistered))
	}
	def := api.toolsRegistered[0]

	// Drive the extension side: read the tool_call hook, send a
	// put_observable *during* the window, then reply to the hook.
	done := make(chan struct{})
	go func() {
		defer close(done)
		msg, err := codec.ReadMessage()
		if err != nil {
			t.Errorf("ext read tool_call: %v", err)
			return
		}
		req, ok := msg.(*Request)
		if !ok || req.Method != "tool_call" {
			t.Errorf("expected tool_call request, got %T", msg)
			return
		}
		if err := codec.WriteRequest(7, "put_observable", map[string]any{
			"key": "progress", "slug": "halfway", "detail": "...",
		}); err != nil {
			t.Errorf("write put_observable: %v", err)
			return
		}
		// Drain the put_observable ack.
		for {
			rm, err := codec.ReadMessage()
			if err != nil {
				t.Errorf("read ack: %v", err)
				return
			}
			if _, ok := rm.(*Response); ok {
				break
			}
		}
		result := json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)
		_ = codec.WriteResponse(req.ID, &result, nil)
	}()

	res, err := def.Execute(ToolContext{
		Context:    context.Background(),
		ToolCallID: "tc-abc",
		Params:     map[string]any{},
	})
	if err != nil {
		t.Fatalf("def.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %#v", res)
	}
	<-done

	cards := s.List()
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].EntryID != "tc-abc" {
		t.Errorf("EntryID = %q, want tc-abc (in-flight tool_call_id)", cards[0].EntryID)
	}
}

// TestBridge_CurrentEntryID_EmptyOutsideToolDispatch verifies the
// negative invariant: a put_observable issued from an event-driven
// path (not inside a tool call) gets empty EntryID.
func TestBridge_CurrentEntryID_EmptyOutsideToolDispatch(t *testing.T) {
	codec, s, cancel := runBridgeWithStore(t, "myext")
	defer cancel()

	if r := roundtrip(t, codec, 1, "put_observable", map[string]any{
		"key": "k", "slug": "s", "detail": "d",
	}); r.Error != nil {
		t.Fatal(r.Error)
	}

	got := s.List()
	if len(got) != 1 {
		t.Fatalf("expected 1 card, got %d", len(got))
	}
	if got[0].EntryID != "" {
		t.Errorf("EntryID should be empty outside tool dispatch, got %q", got[0].EntryID)
	}
}

// TestBridge_PutObservable_NoStoreSilentlyNoOps confirms the bridge
// doesn't blow up if the store has not been wired (e.g. auth-helper).
func TestBridge_PutObservable_NoStoreSilentlyNoOps(t *testing.T) {
	caps := &InitResult{Name: "myext"}
	b, codec := pipePair(caps)
	// Deliberately do NOT call SetObservableStore.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, newMockAPI()) }()

	resp := roundtrip(t, codec, 1, "put_observable", map[string]any{"key": "k", "slug": "s"})
	if resp.Error != nil {
		t.Errorf("put_observable should ack ok with no store wired, got: %v", resp.Error)
	}
}
