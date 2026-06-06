package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// buildDemo compiles this package to a temp binary and returns its path.
func buildDemo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "go-demo")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build demo: %v\n%s", err, out)
	}
	return bin
}

type proc struct {
	t   *testing.T
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func startDemo(t *testing.T) *proc {
	t.Helper()
	bin := buildDemo(t)
	cmd := exec.Command(bin)
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
	return &proc{t: t, cmd: cmd, in: stdin, out: bufio.NewReader(stdout)}
}

func (p *proc) send(v any) {
	data, _ := json.Marshal(v)
	if _, err := p.in.Write(append(data, '\n')); err != nil {
		p.t.Fatalf("send: %v", err)
	}
}

func (p *proc) read() map[string]any {
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := p.out.ReadBytes('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			p.t.Fatalf("read: %v", r.err)
		}
		var m map[string]any
		if err := json.Unmarshal(r.line, &m); err != nil {
			p.t.Fatalf("unmarshal %q: %v", r.line, err)
		}
		return m
	case <-time.After(10 * time.Second):
		p.t.Fatal("timeout waiting for demo output")
		return nil
	}
}

func (p *proc) close() {
	_ = p.in.Close()
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = p.cmd.Process.Kill()
	}
}

func TestDemoEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build-and-run integration test in -short mode")
	}
	p := startDemo(t)
	defer p.close()

	// init
	p.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "init", "params": map[string]any{"version": "1"}})
	init := p.read()
	res, _ := init["result"].(map[string]any)
	if res["name"] != "go-demo" {
		t.Fatalf("init name = %v", res["name"])
	}
	tools, _ := res["tools"].([]any)
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}

	// tool_call: go_wordcount
	p.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tool_call",
		"params": map[string]any{"name": "go_wordcount", "params": map[string]any{"text": "one two three"}}})
	tc := p.read()
	tcres, _ := tc["result"].(map[string]any)
	content, _ := tcres["content"].([]any)
	block, _ := content[0].(map[string]any)
	if block["text"] != "3 word(s)" {
		t.Errorf("wordcount text = %v", block["text"])
	}

	// hook/tool_call: block rm -rf /
	p.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "hook/tool_call",
		"params": map[string]any{"tool_name": "bash", "params": map[string]any{"command": "rm -rf /tmp/x && rm -rf /"}}})
	hk := p.read()
	hkres, _ := hk["result"].(map[string]any)
	if hkres == nil || hkres["block"] != true {
		t.Errorf("expected block, got %v", hk["result"])
	}

	// hook/tool_call: allow normal command (result null)
	p.send(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "hook/tool_call",
		"params": map[string]any{"tool_name": "bash", "params": map[string]any{"command": "ls"}}})
	hk2 := p.read()
	if _, hasResult := hk2["result"]; !hasResult {
		t.Errorf("expected result key, got %v", hk2)
	}
	if hk2["result"] != nil {
		t.Errorf("expected null result for allow, got %v", hk2["result"])
	}

	// tool_call: go_uname — exercises an outbound exec callback. We play fir:
	// read the demo's outbound exec request, answer it, then read the result.
	p.send(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tool_call",
		"params": map[string]any{"name": "go_uname", "params": map[string]any{}}})
	execReq := p.read()
	if execReq["method"] != "exec" {
		t.Fatalf("expected exec callback, got %v", execReq["method"])
	}
	execID := execReq["id"]
	p.send(map[string]any{"jsonrpc": "2.0", "id": execID,
		"result": map[string]any{"stdout": "FakeKernel 1.0\n", "stderr": "", "exit_code": 0}})
	unameRes := p.read()
	ur, _ := unameRes["result"].(map[string]any)
	uc, _ := ur["content"].([]any)
	ub, _ := uc[0].(map[string]any)
	if ub["text"] != "FakeKernel 1.0" {
		t.Errorf("uname text = %v", ub["text"])
	}

	// command hook
	p.send(map[string]any{"jsonrpc": "2.0", "id": 6, "method": "hook/command",
		"params": map[string]any{"name": "go-demo", "args": []string{"world"}}})
	cmd := p.read()
	cr, _ := cmd["result"].(map[string]any)
	if cr["message"] != "hello, world — from Go" {
		t.Errorf("command message = %v", cr["message"])
	}
}
