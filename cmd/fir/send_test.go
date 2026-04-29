package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// mockConn captures bytes written to it.
type mockConn struct {
	bytes.Buffer
}

func readJSONLine(t *testing.T, c *mockConn) map[string]string {
	t.Helper()
	line, err := c.ReadString('\n')
	if err != nil {
		t.Fatalf("read line: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &m); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	return m
}

func TestSendMsg_Basic(t *testing.T) {
	c := &mockConn{}
	if err := sendMsg(c, []string{"hello agent"}, ""); err != nil {
		t.Fatalf("sendMsg: %v", err)
	}
	m := readJSONLine(t, c)
	if m["content"] != "hello agent" {
		t.Errorf("content = %q", m["content"])
	}
	if m["deliver_as"] != "" {
		t.Errorf("deliver_as = %q, want empty", m["deliver_as"])
	}
}

func TestSendMsg_MultiLineJoined(t *testing.T) {
	c := &mockConn{}
	if err := sendMsg(c, []string{"line one", "line two", "line three"}, ""); err != nil {
		t.Fatalf("sendMsg: %v", err)
	}
	m := readJSONLine(t, c)
	if m["content"] != "line one\nline two\nline three" {
		t.Errorf("content = %q", m["content"])
	}
}

func TestSendMsg_BangSigil_Steer(t *testing.T) {
	c := &mockConn{}
	if err := sendMsg(c, []string{"!stop, read foo.go instead"}, ""); err != nil {
		t.Fatalf("sendMsg: %v", err)
	}
	m := readJSONLine(t, c)
	if m["deliver_as"] != "steer" {
		t.Errorf("deliver_as = %q, want steer", m["deliver_as"])
	}
	if m["content"] != "stop, read foo.go instead" {
		t.Errorf("content = %q (bang should be stripped)", m["content"])
	}
}

func TestSendMsg_PlusSigil_FollowUp(t *testing.T) {
	c := &mockConn{}
	if err := sendMsg(c, []string{"+also update changelog"}, ""); err != nil {
		t.Fatalf("sendMsg: %v", err)
	}
	m := readJSONLine(t, c)
	if m["deliver_as"] != "followUp" {
		t.Errorf("deliver_as = %q, want followUp", m["deliver_as"])
	}
	if m["content"] != "also update changelog" {
		t.Errorf("content = %q", m["content"])
	}
}

func TestSendMsg_EscapedBang(t *testing.T) {
	c := &mockConn{}
	if err := sendMsg(c, []string{`\!literal bang`}, ""); err != nil {
		t.Fatalf("sendMsg: %v", err)
	}
	m := readJSONLine(t, c)
	if m["deliver_as"] != "" {
		t.Errorf("deliver_as = %q, want empty (escaped sigil)", m["deliver_as"])
	}
	if m["content"] != "!literal bang" {
		t.Errorf("content = %q, want '!literal bang'", m["content"])
	}
}

func TestSendMsg_EscapedPlus(t *testing.T) {
	c := &mockConn{}
	if err := sendMsg(c, []string{`\+literal plus`}, ""); err != nil {
		t.Fatalf("sendMsg: %v", err)
	}
	m := readJSONLine(t, c)
	if m["content"] != "+literal plus" {
		t.Errorf("content = %q", m["content"])
	}
}

func TestSendMsg_DefaultDeliverAs_Steer(t *testing.T) {
	c := &mockConn{}
	if err := sendMsg(c, []string{"message without sigil"}, "steer"); err != nil {
		t.Fatalf("sendMsg: %v", err)
	}
	m := readJSONLine(t, c)
	if m["deliver_as"] != "steer" {
		t.Errorf("deliver_as = %q, want steer (from --steer flag)", m["deliver_as"])
	}
}

func TestSendMsg_SigilOverridesDefault(t *testing.T) {
	// Even if --steer is the default, a + sigil overrides to followUp.
	c := &mockConn{}
	if err := sendMsg(c, []string{"+override to followUp"}, "steer"); err != nil {
		t.Fatalf("sendMsg: %v", err)
	}
	m := readJSONLine(t, c)
	if m["deliver_as"] != "followUp" {
		t.Errorf("deliver_as = %q, want followUp (sigil overrides default)", m["deliver_as"])
	}
}

func TestSendMsg_EmptyContentSkipped(t *testing.T) {
	c := &mockConn{}
	if err := sendMsg(c, []string{"   ", "\t"}, ""); err != nil {
		t.Fatalf("sendMsg: %v", err)
	}
	if c.Len() > 0 {
		t.Errorf("whitespace-only content should not be sent, got %q", c.String())
	}
}

func TestSendMsg_EmptySlice(t *testing.T) {
	c := &mockConn{}
	if err := sendMsg(c, nil, ""); err != nil {
		t.Fatalf("sendMsg: %v", err)
	}
	if c.Len() > 0 {
		t.Error("nil lines should not be sent")
	}
}

func TestRunSend_NoArgs(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"fir", "send"}
	err := runSend()
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("expected 'required' error, got: %v", err)
	}
}

func TestRunSend_UnknownFlag(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"fir", "send", "--bogus"}
	err := runSend()
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("expected unknown-flag error, got: %v", err)
	}
}

func TestRunSend_ConflictingFlags(t *testing.T) {
	// --steer and --follow together must error before attempting any I/O.
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"fir", "send", "--steer", "--follow", "someid"}
	err := runSend()
	if err == nil {
		t.Fatal("expected error for --steer + --follow")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' error, got: %v", err)
	}
}
