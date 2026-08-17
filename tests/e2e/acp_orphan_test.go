//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// e2e tests may import pkg/ only for pure helpers (path and format
	// derivations like this one), never to drive behaviour in-process: the
	// built binary stays the only thing under test. Deriving the session dir
	// from the real function keeps the fixture correct by construction — a
	// hand-rolled copy of the slug format would silently plant the transcript
	// where the binary does not look if that format ever changed.
	"github.com/kfet/fir/pkg/session/store"
)

// A session killed mid tool call leaves an assistant message whose toolCall
// blocks have no matching toolResult. On load, fir must synthesize an error
// result for each orphan so the provider sees a well-formed history — and must
// replay those results to the ACP client without persisting them.
//
// pkg/session/store/orphan_test.go covers the store layer. This test covers the
// layer above it: the real binary, over a real session/load, emitting real
// session/update notifications.
func TestACP_SessionLoad_ReplaysOrphanedToolCallResults(t *testing.T) {
	agentDir := t.TempDir()
	cwd := t.TempDir()

	// Session dir keyed by cwd, exactly as the store computes it.
	sessDir := store.SessionDirForCwd(agentDir, cwd)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessPath := filepath.Join(sessDir, "orphan.jsonl")

	// Header, a user message, then an assistant message carrying TWO parallel
	// toolCall blocks and no toolResult — then a partial line, as a SIGKILL
	// mid-write leaves behind. Note: no trailing newline on the partial line.
	const truncatedLine = `{"type":"message","id":"e3","parentId":"e2","timestamp":"2026-08-02T07:39:34.0","message":{"role":"toolRes`
	transcript := `{"type":"session","version":3,"id":"orphan-demo","timestamp":"2026-08-02T07:39:00.000Z","cwd":"` + cwd + `"}` + "\n" +
		`{"type":"message","id":"e1","parentId":"","timestamp":"2026-08-02T07:39:10.000Z","message":{"role":"user","content":"deploy the thing","timestamp":1754120350000}}` + "\n" +
		`{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-08-02T07:39:18.100Z","message":{"role":"assistant","model":"claude-test","provider":"anthropic","stopReason":"toolUse","timestamp":1754120358100,"content":[{"type":"text","text":"Deploying now."},{"type":"toolCall","id":"call-push","name":"bash","arguments":{"command":"git push"}},{"type":"toolCall","id":"call-post","name":"bash","arguments":{"command":"curl -XPOST https://deploy"}}]}}` + "\n" +
		truncatedLine
	if err := os.WriteFile(sessPath, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(firBinary, "--mode", "acp", "--no-extensions")
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(), "FIR_AGENT_DIR="+agentDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	type result struct {
		status string
		body   string
	}
	// Stream tool_call_update notifications out of stdout as they arrive, so the
	// test finishes the instant both orphans have been replayed rather than
	// holding stdin open for a fixed duration and racing EOF under load.
	results := make(chan map[string]result, 8)
	go func() {
		got := map[string]result{}
		dec := json.NewDecoder(stdout)
		for {
			var m map[string]any
			if err := dec.Decode(&m); err != nil {
				return
			}
			if m["method"] != "session/update" {
				continue
			}
			params, _ := m["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			if update["sessionUpdate"] != "tool_call_update" {
				continue
			}
			id, _ := update["toolCallId"].(string)
			status, _ := update["status"].(string)
			var texts []string
			content, _ := update["content"].([]any)
			for _, c := range content {
				cm, _ := c.(map[string]any)
				inner, _ := cm["content"].(map[string]any)
				if inner["type"] == "text" {
					text, _ := inner["text"].(string)
					texts = append(texts, text)
				}
			}
			got[id] = result{status: status, body: strings.Join(texts, " ")}
			if _, ok := got["call-push"]; ok {
				if _, ok := got["call-post"]; ok {
					snapshot := map[string]result{}
					for k, v := range got {
						snapshot[k] = v
					}
					results <- snapshot
					return
				}
			}
		}
	}()

	if _, err := io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false}}}}`+"\n"+
		`{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"`+sessPath+`","cwd":"`+cwd+`"}}`+"\n"); err != nil {
		t.Fatal(err)
	}

	var got map[string]result
	select {
	case got = <-results:
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for replayed tool results for both orphaned calls")
	}

	for _, id := range []string{"call-push", "call-post"} {
		r := got[id]
		if r.status != "failed" {
			t.Errorf("orphan %s: status = %q, want %q", id, r.status, "failed")
		}
		for _, want := range []string{"MAY OR MAY NOT", "UNKNOWN", "verify the current state"} {
			if !strings.Contains(r.body, want) {
				t.Errorf("orphan %s: result body missing %q; got: %s", id, want, r.body)
			}
		}
	}

	// The synthesized results are a load-time repair of the in-memory history
	// only — they must never be written back to the transcript.
	onDisk, err := os.ReadFile(sessPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "MAY OR MAY NOT") {
		t.Errorf("synthesized tool result was persisted to the transcript:\n%s", onDisk)
	}

	// The truncated final line is repaired additively: a newline is appended so
	// the next append cannot glue onto it, but the partial bytes are left in
	// place as inert garbage that loading skips. Nothing is rewritten.
	if !strings.HasSuffix(string(onDisk), "\n") {
		t.Errorf("truncated final line was not repaired — file still lacks a trailing newline:\n%s", onDisk)
	}
	if !strings.Contains(string(onDisk), truncatedLine) {
		t.Errorf("the partial line was destroyed rather than left inert:\n%s", onDisk)
	}
	if n := strings.Count(string(onDisk), `"type":"message"`); n != 3 {
		t.Errorf("transcript entry count = %d, want 3 (nothing added or removed)", n)
	}
}
