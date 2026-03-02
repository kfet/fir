package extension

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCodec_RoundTripRequest(t *testing.T) {
	var buf bytes.Buffer
	c := NewCodec(&buf, &buf)

	if err := c.WriteRequest(1, "init", map[string]string{"version": "1"}); err != nil {
		t.Fatal(err)
	}

	msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	req, ok := msg.(*Request)
	if !ok {
		t.Fatalf("expected *Request, got %T", msg)
	}
	if req.Method != "init" {
		t.Errorf("method = %q, want init", req.Method)
	}
	// ID comes back as float64 from JSON
	if id, ok := req.ID.(float64); !ok || id != 1 {
		t.Errorf("id = %v (%T), want 1", req.ID, req.ID)
	}
}

func TestCodec_RoundTripNotification(t *testing.T) {
	var buf bytes.Buffer
	c := NewCodec(&buf, &buf)

	if err := c.WriteNotification("event/session_start", nil); err != nil {
		t.Fatal(err)
	}

	msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	notif, ok := msg.(*Notification)
	if !ok {
		t.Fatalf("expected *Notification, got %T", msg)
	}
	if notif.Method != "event/session_start" {
		t.Errorf("method = %q", notif.Method)
	}
}

func TestCodec_RoundTripResponse(t *testing.T) {
	var buf bytes.Buffer
	c := NewCodec(&buf, &buf)

	if err := c.WriteResponse(1, map[string]string{"name": "test"}, nil); err != nil {
		t.Fatal(err)
	}

	msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := msg.(*Response)
	if !ok {
		t.Fatalf("expected *Response, got %T", msg)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %v", resp.Error)
	}
	// Verify result
	var result map[string]string
	if err := json.Unmarshal(*resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result["name"] != "test" {
		t.Errorf("result name = %q", result["name"])
	}
}

func TestCodec_RoundTripErrorResponse(t *testing.T) {
	var buf bytes.Buffer
	c := NewCodec(&buf, &buf)

	if err := c.WriteResponse(2, nil, &Error{Code: -32600, Message: "Invalid Request"}); err != nil {
		t.Fatal(err)
	}

	msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := msg.(*Response)
	if !ok {
		t.Fatalf("expected *Response, got %T", msg)
	}
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32600 {
		t.Errorf("error code = %d", resp.Error.Code)
	}
}

func TestCodec_MultipleMessages(t *testing.T) {
	var buf bytes.Buffer
	c := NewCodec(&buf, &buf)

	c.WriteRequest(1, "init", nil)
	c.WriteNotification("event/start", nil)
	c.WriteResponse(1, "ok", nil)

	msg1, _ := c.ReadMessage()
	msg2, _ := c.ReadMessage()
	msg3, _ := c.ReadMessage()

	if _, ok := msg1.(*Request); !ok {
		t.Errorf("msg1: expected *Request, got %T", msg1)
	}
	if _, ok := msg2.(*Notification); !ok {
		t.Errorf("msg2: expected *Notification, got %T", msg2)
	}
	if _, ok := msg3.(*Response); !ok {
		t.Errorf("msg3: expected *Response, got %T", msg3)
	}
}

func TestCodec_InvalidJSON(t *testing.T) {
	r := strings.NewReader("not json\n")
	c := NewCodec(r, nil)
	_, err := c.ReadMessage()
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestCodec_WriteResponse_NilResult(t *testing.T) {
	// JSON-RPC 2.0: successful response must include "result" even if null.
	var buf bytes.Buffer
	c := NewCodec(&buf, &buf)

	if err := c.WriteResponse(1, nil, nil); err != nil {
		t.Fatal(err)
	}

	// Check raw JSON contains "result":null
	raw := buf.String()
	if !strings.Contains(raw, `"result":null`) {
		t.Fatalf("expected result:null in response, got: %s", raw)
	}
	if strings.Contains(raw, `"error"`) {
		t.Fatalf("unexpected error field in response: %s", raw)
	}
}

func TestCodec_WriteResponse_ErrorOmitsResult(t *testing.T) {
	var buf bytes.Buffer
	c := NewCodec(&buf, &buf)

	if err := c.WriteResponse(1, nil, &Error{Code: -32600, Message: "bad"}); err != nil {
		t.Fatal(err)
	}

	raw := buf.String()
	if strings.Contains(raw, `"result"`) {
		t.Fatalf("result should be omitted on error response, got: %s", raw)
	}
	if !strings.Contains(raw, `"error"`) {
		t.Fatalf("expected error field, got: %s", raw)
	}
}

func TestError_ErrorInterface(t *testing.T) {
	e := &Error{Code: -32601, Message: "Method not found"}
	s := e.Error()
	if !strings.Contains(s, "-32601") || !strings.Contains(s, "Method not found") {
		t.Errorf("unexpected error string: %s", s)
	}
}
