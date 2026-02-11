// Mock OpenAI Chat Completions server for E2E testing.
// Speaks the SSE streaming protocol that the openai-completions provider expects.
//
// Keyword-based responses:
//   - "READ_FILE <path>"  → returns a tool call for the "read" tool
//   - "WRITE_FILE <path> <content>" → returns a tool call for the "write" tool
//   - "RUN_BASH <command>" → returns a tool call for the "bash" tool
//   - anything else → returns a plain text response "MOCK_RESPONSE: <echo of prompt>"
//
// After a tool result round-trip, the server returns a text summary.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handleCompletions)

	// Use MOCK_PORT env var if set, otherwise pick a random port.
	addr := "127.0.0.1:0"
	if p := os.Getenv("MOCK_PORT"); p != "" {
		addr = "127.0.0.1:" + p
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen on %s: %v\n", addr, err)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// Write port to a known file so the test harness can read it.
	// Use MOCK_PORT_FILE env var if set, otherwise fall back to TMPDIR/mock-e2e-port, then /tmp/mock-e2e-port.
	portFile := os.Getenv("MOCK_PORT_FILE")
	if portFile == "" {
		tmpDir := os.Getenv("TMPDIR")
		if tmpDir == "" {
			tmpDir = "/tmp"
		}
		portFile = tmpDir + "/mock-e2e-port"
	}
	os.WriteFile(portFile, []byte(fmt.Sprintf("%d", port)), 0o644)
	fmt.Printf("MOCK_PORT=%d\n", port)

	if err := http.Serve(listener, mux); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}

// --- request types ---

type chatRequest struct {
	Messages []chatMessage `json:"messages"`
	Tools    []chatTool    `json:"tools"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"` // string or array
	ToolCalls  []reqToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type reqToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function json.RawMessage `json:"function"`
}

type chatTool struct {
	Type     string   `json:"type"`
	Function toolFunc `json:"function"`
}

type toolFunc struct {
	Name string `json:"name"`
}

func handleCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}

	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json", 400)
		return
	}

	// Extract the last user message text.
	userText := lastUserText(req.Messages)

	// Check if the last message is a tool result — if so, return a text summary.
	if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "tool" {
		writeSSETextResponse(w, "Tool execution complete. MOCK_TOOL_DONE")
		return
	}

	// Check for tool-call keywords.
	availableTools := toolSet(req.Tools)

	upper := strings.ToUpper(userText)
	switch {
	case strings.HasPrefix(upper, "READ_FILE") && availableTools["read"]:
		parts := strings.Fields(userText)
		path := "testfile.txt"
		if len(parts) >= 2 {
			path = parts[1]
		}
		writeSSEToolCall(w, "call_read_1", "read", map[string]any{"path": path})
		return

	case strings.HasPrefix(upper, "WRITE_FILE") && availableTools["write"]:
		parts := strings.Fields(userText)
		path := "output.txt"
		content := "WRITTEN_BY_PI"
		if len(parts) >= 2 {
			path = parts[1]
		}
		if len(parts) >= 3 {
			content = strings.Join(parts[2:], " ")
		}
		writeSSEToolCall(w, "call_write_1", "write", map[string]any{"path": path, "content": content})
		return

	case strings.HasPrefix(upper, "RUN_BASH") && availableTools["bash"]:
		cmd := "echo BASH_E2E_OK"
		if idx := strings.Index(userText, " "); idx != -1 {
			cmd = strings.TrimSpace(userText[idx+1:])
		}
		writeSSEToolCall(w, "call_bash_1", "bash", map[string]any{"command": cmd})
		return
	}

	// Default: plain text response.
	writeSSETextResponse(w, "MOCK_RESPONSE: "+userText)
}

// lastUserText extracts the text from the last user message.
func lastUserText(messages []chatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			// Content can be a string or an array of content blocks.
			var s string
			if err := json.Unmarshal(messages[i].Content, &s); err == nil {
				return s
			}
			// Try array format.
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(messages[i].Content, &blocks); err == nil {
				for _, b := range blocks {
					if b.Type == "text" {
						return b.Text
					}
				}
			}
			return string(messages[i].Content)
		}
	}
	return ""
}

// toolSet builds a set of available tool names.
func toolSet(tools []chatTool) map[string]bool {
	m := make(map[string]bool, len(tools))
	for _, t := range tools {
		m[t.Function.Name] = true
	}
	return m
}

// --- SSE helpers ---

func writeSSETextResponse(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	// Chunk the text to simulate streaming.
	chunks := chunkString(text, 10)

	for i, chunk := range chunks {
		data := sseChunk("chatcmpl-mock", i, chunk, nil, nil)
		fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}

	// Final chunk with finish_reason and usage.
	stop := "stop"
	data := sseChunkFinal("chatcmpl-mock", len(chunks), &stop)
	fmt.Fprintf(w, "data: %s\n\n", data)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeSSEToolCall(w http.ResponseWriter, callID, toolName string, args map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	argsJSON, _ := json.Marshal(args)

	// First chunk: tool call start (id + name, empty arguments).
	tc := toolCallDelta{
		Index: 0,
		ID:    callID,
		Type:  "function",
		Function: &toolCallFunc{
			Name:      toolName,
			Arguments: "",
		},
	}
	data := sseChunk("chatcmpl-mock", 0, "", nil, []toolCallDelta{tc})
	fmt.Fprintf(w, "data: %s\n\n", data)
	if flusher != nil {
		flusher.Flush()
	}

	// Second chunk: arguments.
	tc2 := toolCallDelta{
		Index: 0,
		Function: &toolCallFunc{
			Arguments: string(argsJSON),
		},
	}
	data2 := sseChunk("chatcmpl-mock", 1, "", nil, []toolCallDelta{tc2})
	fmt.Fprintf(w, "data: %s\n\n", data2)
	if flusher != nil {
		flusher.Flush()
	}

	// Final chunk with finish_reason=tool_calls.
	reason := "tool_calls"
	data3 := sseChunkFinal("chatcmpl-mock", 2, &reason)
	fmt.Fprintf(w, "data: %s\n\n", data3)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// --- SSE chunk builders ---

type sseResponse struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Choices []sseChoice `json:"choices"`
	Usage   *sseUsage   `json:"usage,omitempty"`
}

type sseChoice struct {
	Index        int            `json:"index"`
	Delta        sseDelta       `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type sseDelta struct {
	Content   *string         `json:"content,omitempty"`
	ToolCalls []toolCallDelta `json:"tool_calls,omitempty"`
}

type toolCallDelta struct {
	Index    int           `json:"index"`
	ID       string        `json:"id,omitempty"`
	Type     string        `json:"type,omitempty"`
	Function *toolCallFunc `json:"function,omitempty"`
}

type toolCallFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type sseUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func sseChunk(id string, index int, content string, finishReason *string, toolCalls []toolCallDelta) string {
	resp := sseResponse{
		ID:     id,
		Object: "chat.completion.chunk",
		Choices: []sseChoice{{
			Index:        0,
			FinishReason: finishReason,
		}},
	}
	if content != "" {
		resp.Choices[0].Delta.Content = &content
	}
	if len(toolCalls) > 0 {
		resp.Choices[0].Delta.ToolCalls = toolCalls
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func sseChunkFinal(id string, index int, finishReason *string) string {
	resp := sseResponse{
		ID:     id,
		Object: "chat.completion.chunk",
		Choices: []sseChoice{{
			Index:        0,
			Delta:        sseDelta{},
			FinishReason: finishReason,
		}},
		Usage: &sseUsage{
			PromptTokens:     50,
			CompletionTokens: 20,
			TotalTokens:      70,
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func chunkString(s string, size int) []string {
	var chunks []string
	for len(s) > 0 {
		end := size
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[:end])
		s = s[end:]
	}
	return chunks
}
