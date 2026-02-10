package providers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseSSE_Basic(t *testing.T) {
	input := "event: message\ndata: hello\n\n"
	events := make(chan SSEEvent, 10)
	err := parseSSE(strings.NewReader(input), events)
	if err != nil {
		t.Fatal(err)
	}
	close(events)

	ev := <-events
	if ev.Event != "message" {
		t.Errorf("expected event 'message', got %q", ev.Event)
	}
	if ev.Data != "hello" {
		t.Errorf("expected data 'hello', got %q", ev.Data)
	}
}

func TestParseSSE_MultiLineData(t *testing.T) {
	input := "data: line1\ndata: line2\ndata: line3\n\n"
	events := make(chan SSEEvent, 10)
	err := parseSSE(strings.NewReader(input), events)
	if err != nil {
		t.Fatal(err)
	}
	close(events)

	ev := <-events
	if ev.Data != "line1\nline2\nline3" {
		t.Errorf("expected multi-line data, got %q", ev.Data)
	}
}

func TestParseSSE_CommentSkipped(t *testing.T) {
	input := ": this is a comment\ndata: hello\n\n"
	events := make(chan SSEEvent, 10)
	err := parseSSE(strings.NewReader(input), events)
	if err != nil {
		t.Fatal(err)
	}
	close(events)

	ev := <-events
	if ev.Data != "hello" {
		t.Errorf("expected 'hello', got %q", ev.Data)
	}
}

func TestParseSSE_EmptyLineDispatchesEvent(t *testing.T) {
	input := "data: first\n\ndata: second\n\n"
	events := make(chan SSEEvent, 10)
	err := parseSSE(strings.NewReader(input), events)
	if err != nil {
		t.Fatal(err)
	}
	close(events)

	ev1 := <-events
	if ev1.Data != "first" {
		t.Errorf("expected 'first', got %q", ev1.Data)
	}
	ev2 := <-events
	if ev2.Data != "second" {
		t.Errorf("expected 'second', got %q", ev2.Data)
	}
}

func TestParseSSE_IDField(t *testing.T) {
	input := "id: 42\ndata: test\n\n"
	events := make(chan SSEEvent, 10)
	err := parseSSE(strings.NewReader(input), events)
	if err != nil {
		t.Fatal(err)
	}
	close(events)

	ev := <-events
	if ev.ID != "42" {
		t.Errorf("expected id '42', got %q", ev.ID)
	}
}

func TestParseSSE_LeadingSpaceRemoved(t *testing.T) {
	// Per SSE spec, if value starts with a space, remove it
	input := "data: hello world\n\n"
	events := make(chan SSEEvent, 10)
	err := parseSSE(strings.NewReader(input), events)
	if err != nil {
		t.Fatal(err)
	}
	close(events)

	ev := <-events
	if ev.Data != "hello world" {
		t.Errorf("expected 'hello world', got %q", ev.Data)
	}
}

func TestParseSSE_NoTrailingNewline(t *testing.T) {
	// Data without trailing empty line should still be dispatched
	input := "data: orphan"
	events := make(chan SSEEvent, 10)
	err := parseSSE(strings.NewReader(input), events)
	if err != nil {
		t.Fatal(err)
	}
	close(events)

	ev := <-events
	if ev.Data != "orphan" {
		t.Errorf("expected 'orphan', got %q", ev.Data)
	}
}

func TestParseSSE_MultipleEvents(t *testing.T) {
	input := "event: start\ndata: {\"type\":\"start\"}\n\nevent: delta\ndata: {\"type\":\"delta\"}\n\nevent: done\ndata: {\"type\":\"done\"}\n\n"
	events := make(chan SSEEvent, 10)
	err := parseSSE(strings.NewReader(input), events)
	if err != nil {
		t.Fatal(err)
	}
	close(events)

	expected := []struct{ event, data string }{
		{"start", `{"type":"start"}`},
		{"delta", `{"type":"delta"}`},
		{"done", `{"type":"done"}`},
	}
	for _, exp := range expected {
		ev := <-events
		if ev.Event != exp.event || ev.Data != exp.data {
			t.Errorf("expected (%q, %q), got (%q, %q)", exp.event, exp.data, ev.Event, ev.Data)
		}
	}
}

func TestParseSSE_ConsecutiveBlankLines(t *testing.T) {
	// Consecutive blank lines without data shouldn't produce events
	input := "\n\n\ndata: after-blanks\n\n"
	events := make(chan SSEEvent, 10)
	err := parseSSE(strings.NewReader(input), events)
	if err != nil {
		t.Fatal(err)
	}
	close(events)

	ev := <-events
	if ev.Data != "after-blanks" {
		t.Errorf("expected 'after-blanks', got %q", ev.Data)
	}
}

func TestParseSSE_FieldWithoutColon(t *testing.T) {
	// A line without a colon: the entire line is the field name, value is ""
	input := "data\n\n"
	events := make(chan SSEEvent, 10)
	err := parseSSE(strings.NewReader(input), events)
	if err != nil {
		t.Fatal(err)
	}
	close(events)

	ev := <-events
	// "data" with no colon means field="data", value=""
	if ev.Data != "" {
		t.Errorf("expected empty data for bare 'data' field, got %q", ev.Data)
	}
}

func TestSSEClient_Stream_OK(t *testing.T) {
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "data: hello\n\ndata: world\n\n")
	}))
	defer srv.Close()

	client := &SSEClient{HTTPClient: srv.Client()}
	events, errCh := client.Stream(context.Background(), srv.URL, nil, strings.NewReader("{}"))

	var collected []SSEEvent
	for ev := range events {
		collected = append(collected, ev)
	}

	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	if len(collected) != 2 {
		t.Fatalf("expected 2 events, got %d", len(collected))
	}
	if collected[0].Data != "hello" || collected[1].Data != "world" {
		t.Errorf("unexpected events: %+v", collected)
	}
}

func TestSSEClient_Stream_HTTPError(t *testing.T) {
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, "rate limited")
	}))
	defer srv.Close()

	client := &SSEClient{HTTPClient: srv.Client()}
	events, errCh := client.Stream(context.Background(), srv.URL, nil, strings.NewReader("{}"))

	for range events {
	}

	err := <-errCh
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("expected 429 in error, got %q", err.Error())
	}
}

func TestSSEClient_Stream_ContextCancellation(t *testing.T) {
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := &SSEClient{HTTPClient: srv.Client()}
	events, errCh := client.Stream(ctx, srv.URL, nil, strings.NewReader("{}"))

	cancel()

	for range events {
	}

	err := <-errCh
	if err != nil && !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSSEClient_Stream_Headers(t *testing.T) {
	var gotHeaders http.Header
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "data: ok\n\n")
	}))
	defer srv.Close()

	client := &SSEClient{HTTPClient: srv.Client()}
	headers := map[string]string{
		"Authorization": "Bearer test-key",
		"X-Custom":      "value",
	}
	events, errCh := client.Stream(context.Background(), srv.URL, headers, strings.NewReader("{}"))

	for range events {
	}
	<-errCh

	if gotHeaders.Get("Authorization") != "Bearer test-key" {
		t.Errorf("missing Authorization header")
	}
	if gotHeaders.Get("X-Custom") != "value" {
		t.Errorf("missing X-Custom header")
	}
	if gotHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("missing Content-Type header")
	}
	if gotHeaders.Get("Accept") != "text/event-stream" {
		t.Errorf("missing Accept header")
	}
}
