package extension

import (
	"encoding/json"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestHandleProviderStreamEvent_ReportsHeaders(t *testing.T) {
	b := &Bridge{}
	out := ai.NewAssistantMessageEventStream()
	var got map[string]string
	b.activeStreams.Store("s1", &extActiveStream{out: out, sink: func(h map[string]string) { got = h }})

	raw := json.RawMessage(`{"stream_id":"s1","event":{"type":"text_start","contentIndex":0},"response_headers":{"X-RateLimit-Remaining":"9","Set-Cookie":"c"}}`)
	b.handleProviderStreamEvent(&raw)
	if len(got) != 1 || got["x-ratelimit-remaining"] != "9" {
		t.Fatalf("headers = %v", got)
	}

	// No sink and no headers: event still delivered without panicking.
	b.activeStreams.Store("s2", &extActiveStream{out: out})
	raw = json.RawMessage(`{"stream_id":"s2","event":{"type":"text_start","contentIndex":0},"response_headers":{"a":"b"}}`)
	b.handleProviderStreamEvent(&raw)
}
