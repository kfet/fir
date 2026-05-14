// Wire-summary tracing for outgoing provider requests.
//
// One Trace line per outgoing wire message at -vv, summarising role,
// block count, and per-block-type structure. Bounded cost per request
// — no body bytes are logged — so it's safe to leave on for whole
// sessions while investigating prefix-reconstruction / replay bugs
// (e.g. Anthropic 400 "thinking blocks cannot be modified").
//
// Provider-agnostic: marshals the body to JSON, walks the message
// array under one of the well-known keys ("messages", "input",
// "contents"), and renders each block's summary based on its "type"
// (or Gemini "parts" / OpenAI "tool_calls" / "reasoning_content"
// siblings). Providers using SDK types (e.g. Bedrock) round-trip
// through JSON to reach the same shape.

package providers

import (
	"encoding/json"
	"fmt"
	"strings"

	firlog "github.com/kfet/fir/pkg/log"
)

// traceWireMessages emits one firlog.Trace line per outgoing wire
// message. No-op when Trace is not enabled.
func traceWireMessages(provider string, body any) {
	if !firlog.TraceEnabled() {
		return
	}
	var raw []byte
	switch v := body.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			firlog.Trace(provider+" wire", "marshalError", err.Error())
			return
		}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		firlog.Trace(provider+" wire", "unmarshalError", err.Error())
		return
	}
	var msgs []any
	var key string
	for _, k := range []string{"messages", "input", "contents"} {
		if v, ok := m[k].([]any); ok {
			msgs, key = v, k
			break
		}
	}
	sysBlocks := 0
	if s, ok := m["system"].([]any); ok {
		sysBlocks = len(s)
	} else if _, ok := m["system"].(string); ok {
		sysBlocks = 1
	}
	firlog.Trace(provider+" wire", "key", key, "messages", len(msgs), "systemBlocks", sysBlocks)
	for i, msg := range msgs {
		mm, _ := msg.(map[string]any)
		role, _ := mm["role"].(string)
		t, _ := mm["type"].(string)
		summary := summarizeWireMessage(mm)
		firlog.Trace(provider+" wire msg", "idx", i, "role", role, "type", t, "blocks", summary)
	}
}

// summarizeWireMessage renders a comma-separated summary of the
// blocks inside one wire message, handling content (string or
// []block), parts (Gemini), tool_calls (OpenAI chat), reasoning
// content, and the responses-API top-level type variants.
func summarizeWireMessage(msg map[string]any) string {
	var parts []string

	switch c := msg["content"].(type) {
	case string:
		if c != "" {
			parts = append(parts, fmt.Sprintf("text(%d)", len(c)))
		}
	case []any:
		for _, b := range c {
			bm, _ := b.(map[string]any)
			t, _ := bm["type"].(string)
			parts = append(parts, describeBlock(t, bm))
		}
	}

	// Gemini-style parts.
	if pp, ok := msg["parts"].([]any); ok {
		for _, p := range pp {
			pm, _ := p.(map[string]any)
			parts = append(parts, describeGooglePart(pm))
		}
	}

	// OpenAI chat-completions tool_calls sibling.
	if tc, ok := msg["tool_calls"].([]any); ok {
		for _, c := range tc {
			cm, _ := c.(map[string]any)
			name := ""
			if fn, ok := cm["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
			}
			parts = append(parts, fmt.Sprintf("tool_call(%s)", name))
		}
	}
	if rc, ok := msg["reasoning_content"].(string); ok && rc != "" {
		parts = append(parts, fmt.Sprintf("reasoning_content(%d)", len(rc)))
	}

	// OpenAI Responses API: each "message" carries top-level fields
	// like {type: "function_call", name: ...} or
	// {type: "reasoning", summary: [...]}.
	if t, _ := msg["type"].(string); t != "" && msg["content"] == nil {
		parts = append(parts, describeBlock(t, msg))
	}

	return strings.Join(parts, ",")
}

func describeBlock(t string, b map[string]any) string {
	switch t {
	case "text", "input_text", "output_text":
		tx, _ := b["text"].(string)
		return fmt.Sprintf("%s(%d)", t, len(tx))
	case "thinking":
		th, _ := b["thinking"].(string)
		sig, _ := b["signature"].(string)
		return fmt.Sprintf("thinking(th=%d,sig=%d)", len(th), len(sig))
	case "redacted_thinking":
		d, _ := b["data"].(string)
		return fmt.Sprintf("redacted_thinking(data=%d)", len(d))
	case "tool_use":
		name, _ := b["name"].(string)
		return fmt.Sprintf("tool_use(%s)", name)
	case "tool_result":
		id, _ := b["tool_use_id"].(string)
		return fmt.Sprintf("tool_result(%s)", id)
	case "function_call":
		name, _ := b["name"].(string)
		return fmt.Sprintf("function_call(%s)", name)
	case "function_call_output":
		id, _ := b["call_id"].(string)
		return fmt.Sprintf("function_call_output(%s)", id)
	case "reasoning":
		n := 0
		if s, ok := b["summary"].([]any); ok {
			n = len(s)
		}
		sig, _ := b["encrypted_content"].(string)
		return fmt.Sprintf("reasoning(parts=%d,enc=%d)", n, len(sig))
	case "image", "image_url", "input_image":
		return t
	case "":
		return "(typeless)"
	default:
		return t
	}
}

func describeGooglePart(p map[string]any) string {
	if tx, ok := p["text"].(string); ok {
		thought := ""
		if t, ok := p["thought"].(bool); ok && t {
			thought = ",thought"
		}
		return fmt.Sprintf("text(%d%s)", len(tx), thought)
	}
	if fc, ok := p["functionCall"].(map[string]any); ok {
		name, _ := fc["name"].(string)
		return fmt.Sprintf("functionCall(%s)", name)
	}
	if fr, ok := p["functionResponse"].(map[string]any); ok {
		name, _ := fr["name"].(string)
		return fmt.Sprintf("functionResponse(%s)", name)
	}
	if _, ok := p["inlineData"]; ok {
		return "inlineData"
	}
	if _, ok := p["fileData"]; ok {
		return "fileData"
	}
	return "(unknown-part)"
}
