// Ported from: packages/ai/src/providers/openai-codex-responses.ts (WebSocket transport section)
// Upstream hash: 036bde0a
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"github.com/kfet/fir/pkg/ai"
)

const (
	openAIBetaResponsesWebsockets = "responses_websockets=2026-02-06"
	sessionWebSocketCacheTTLMs    = 5 * 60 * 1000 // 5 minutes
)

// cachedWSConn represents a cached WebSocket connection.
type cachedWSConn struct {
	conn      *websocket.Conn
	busy      bool
	idleTimer *time.Timer
	// continuation tracks the most recent successful response on this socket
	// so the next request can be sent as `previous_response_id` + delta input
	// when transport is "websocket-cached" or "auto".
	continuation *wsContinuation
}

// wsContinuation captures the state needed to construct a follow-up Codex
// Responses request that reuses server-side context via previous_response_id.
//
// We deliberately do not store the previous response's converted "items": the
// converter is allowed to embed positional ids (e.g. `msg_<index>`) that won't
// match across requests. Instead we rely on the simpler invariant that the
// next request's input begins with the previous `LastInput`, immediately
// followed by zero or more assistant-output items (`message[role=assistant]`,
// `reasoning`, `function_call`), and that everything after those is the true
// delta the caller wants to add (`function_call_output`, new user messages, …).
type wsContinuation struct {
	// LastBodyJSONNoInput is the JSON of the previous request body with the
	// "input", "previous_response_id", and "type" keys stripped — the
	// comparison key for continuation matching.
	LastBodyJSONNoInput string
	// LastInput is the previous request's input array.
	LastInput []any
	// LastResponseID is the id of the previous response.
	LastResponseID string
}

var (
	wsSessionCache   = make(map[string]*cachedWSConn)
	wsSessionCacheMu sync.Mutex
	wsInflight       = make(map[string]chan struct{}) // coalesces concurrent dials for the same sessionID
)

// resetWSCache clears the global WebSocket session cache. For testing only.
func resetWSCache() {
	wsSessionCacheMu.Lock()
	defer wsSessionCacheMu.Unlock()
	for id, entry := range wsSessionCache {
		if entry != nil {
			if entry.idleTimer != nil {
				entry.idleTimer.Stop()
			}
			if entry.conn != nil {
				entry.conn.Close(websocket.StatusNormalClosure, "reset")
			}
		}
		delete(wsSessionCache, id)
	}
	// Clear any stale inflight markers (e.g., if a test is reset mid-dial).
	for id := range wsInflight {
		delete(wsInflight, id)
	}
}

// resolveCodexWebSocketURL converts a Codex REST URL to its WebSocket equivalent.
func resolveCodexWebSocketURL(baseURL string) string {
	restURL := resolveCodexURL(baseURL)
	u, err := url.Parse(restURL)
	if err != nil {
		return strings.Replace(restURL, "https://", "wss://", 1)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	return u.String()
}

// connectWebSocket dials a new WebSocket connection with the given headers.
func connectWebSocket(ctx context.Context, wsURL string, headers map[string]string) (*websocket.Conn, error) {
	httpHeaders := http.Header{}
	for k, v := range headers {
		httpHeaders.Set(k, v)
	}
	httpHeaders.Set("OpenAI-Beta", openAIBetaResponsesWebsockets)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: httpHeaders,
	})
	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}
	// Set a generous read limit for large responses
	conn.SetReadLimit(32 * 1024 * 1024) // 32MB
	return conn, nil
}

// acquireWebSocket gets a WebSocket connection, reusing a cached one if available.
// Returns the connection, optional cached entry (nil for non-session or non-cached
// reuse), and a release func that takes a `keep` flag.
func acquireWebSocket(ctx context.Context, wsURL string, headers map[string]string, sessionID string) (conn *websocket.Conn, entry *cachedWSConn, release func(keep bool), err error) {
	if sessionID == "" {
		c, err := connectWebSocket(ctx, wsURL, headers)
		if err != nil {
			return nil, nil, nil, err
		}
		return c, nil, func(keep bool) {
			c.Close(websocket.StatusNormalClosure, "done")
		}, nil
	}

	wsSessionCacheMu.Lock()
	cached := wsSessionCache[sessionID]

	if cached != nil {
		if cached.idleTimer != nil {
			cached.idleTimer.Stop()
			cached.idleTimer = nil
		}
		if !cached.busy {
			// Reuse cached connection
			cached.busy = true
			wsSessionCacheMu.Unlock()
			return cached.conn, cached, func(keep bool) {
				wsSessionCacheMu.Lock()
				defer wsSessionCacheMu.Unlock()
				if !keep {
					cached.conn.Close(websocket.StatusNormalClosure, "done")
					delete(wsSessionCache, sessionID)
					return
				}
				cached.busy = false
				scheduleWSExpiry(sessionID, cached)
			}, nil
		}
		// Busy — create a new non-cached connection
		wsSessionCacheMu.Unlock()
		c, err := connectWebSocket(ctx, wsURL, headers)
		if err != nil {
			return nil, nil, nil, err
		}
		return c, nil, func(keep bool) {
			c.Close(websocket.StatusNormalClosure, "done")
		}, nil
	}

	// Check for an inflight dial from a concurrent goroutine.
	if waitCh, ok := wsInflight[sessionID]; ok {
		wsSessionCacheMu.Unlock()
		select {
		case <-waitCh:
			// Dial completed; retry to pick up the cached entry.
			return acquireWebSocket(ctx, wsURL, headers, sessionID)
		case <-ctx.Done():
			return nil, nil, nil, ctx.Err()
		}
	}

	// We are the first goroutine for this sessionID — register inflight.
	ready := make(chan struct{})
	wsInflight[sessionID] = ready
	wsSessionCacheMu.Unlock()

	// No cached connection — connect and cache
	c, dialErr := connectWebSocket(ctx, wsURL, headers)

	wsSessionCacheMu.Lock()
	delete(wsInflight, sessionID)
	close(ready) // wake any waiters (they will retry and find the entry or fail)
	if dialErr != nil {
		wsSessionCacheMu.Unlock()
		return nil, nil, nil, dialErr
	}
	newEntry := &cachedWSConn{conn: c, busy: true}
	wsSessionCache[sessionID] = newEntry
	wsSessionCacheMu.Unlock()

	return c, newEntry, func(keep bool) {
		wsSessionCacheMu.Lock()
		defer wsSessionCacheMu.Unlock()
		if !keep {
			c.Close(websocket.StatusNormalClosure, "done")
			if newEntry.idleTimer != nil {
				newEntry.idleTimer.Stop()
			}
			if wsSessionCache[sessionID] == newEntry {
				delete(wsSessionCache, sessionID)
			}
			return
		}
		newEntry.busy = false
		scheduleWSExpiry(sessionID, newEntry)
	}, nil
}

// scheduleWSExpiry schedules a cached WebSocket for cleanup after the idle TTL.
// Must be called with wsSessionCacheMu held.
func scheduleWSExpiry(sessionID string, entry *cachedWSConn) {
	if entry.idleTimer != nil {
		entry.idleTimer.Stop()
	}
	entry.idleTimer = time.AfterFunc(time.Duration(sessionWebSocketCacheTTLMs)*time.Millisecond, func() {
		wsSessionCacheMu.Lock()
		defer wsSessionCacheMu.Unlock()
		if entry.busy {
			return
		}
		entry.conn.Close(websocket.StatusNormalClosure, "idle_timeout")
		if wsSessionCache[sessionID] == entry {
			delete(wsSessionCache, sessionID)
		}
	})
}

// processWebSocketStream runs a Codex response over WebSocket transport.
// It sends the request body over the socket, reads SSE-like events back,
// and feeds them through the shared responses processor.
//
// When transport is "auto" or "websocket-cached" and a cached connection has
// continuation state from a prior response on the same session, the request
// body is rewritten to send only the new input items plus a
// `previous_response_id` reference. After a successful response, continuation
// state is updated for the next request.
func processWebSocketStream(
	ctx context.Context,
	wsURL string,
	body []byte,
	headers map[string]string,
	output *ai.AssistantMessage,
	stream *ai.AssistantMessageEventStream,
	model *ai.Model,
	options *ai.StreamOptions,
) (started bool, err error) {
	sessionID := ""
	transport := ai.TransportSSE
	if options != nil {
		sessionID = options.SessionID
		if options.Transport != "" {
			transport = options.Transport
		}
	}
	useCachedContinuation := transport == ai.TransportWebSocketCached || transport == ai.TransportAuto

	conn, entry, release, err := acquireWebSocket(ctx, wsURL, headers, sessionID)
	if err != nil {
		return false, err
	}

	keepConnection := true
	defer func() {
		release(keepConnection)
	}()

	// Wrap the body in a response.create message
	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		keepConnection = false
		return false, fmt.Errorf("parsing request body: %w", err)
	}

	// Snapshot the full input before any continuation rewrite — this is what
	// we'll record as `LastInput` for the next request.
	fullInput, _ := bodyMap["input"].([]any)

	// Try to compress to a continuation request when we have cached state.
	if useCachedContinuation && entry != nil {
		if delta, ok := computeWSContinuationDelta(bodyMap, entry.continuation); ok {
			bodyMap["input"] = delta
			bodyMap["previous_response_id"] = entry.continuation.LastResponseID
		} else {
			// Continuation state no longer applies — drop it.
			entry.continuation = nil
		}
	}

	bodyMap["type"] = "response.create"
	wsMsg, err := json.Marshal(bodyMap)
	if err != nil {
		keepConnection = false
		return false, fmt.Errorf("marshaling ws message: %w", err)
	}

	if err := conn.Write(ctx, websocket.MessageText, wsMsg); err != nil {
		keepConnection = false
		// Drop continuation: we don't know if the server saw the request.
		if entry != nil {
			entry.continuation = nil
		}
		return false, fmt.Errorf("websocket write: %w", err)
	}

	// Mark as started — we've sent the request and are about to push EventStart.
	// Even if reading fails later, the caller must NOT fall through to SSE.
	started = true

	stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})

	// Read events from WebSocket
	proc := &responsesSSEProcessor{output: output, stream: stream, model: model}
	sawCompletion := false

	for {
		if ctx.Err() != nil {
			keepConnection = false
			if entry != nil {
				entry.continuation = nil
			}
			return true, fmt.Errorf("request was aborted")
		}

		_, msg, err := conn.Read(ctx)
		if err != nil {
			if sawCompletion {
				break
			}
			keepConnection = false
			if entry != nil {
				entry.continuation = nil
			}
			return true, fmt.Errorf("websocket read: %w", err)
		}

		var parsed map[string]any
		if err := json.Unmarshal(msg, &parsed); err != nil {
			continue
		}

		eventType, _ := parsed["type"].(string)
		if eventType == "response.completed" || eventType == "response.done" {
			sawCompletion = true
		}

		// Map Codex events and process
		data := mapCodexEventFromMap(parsed)
		if data == "" {
			continue
		}

		done, err := proc.processEvent(data)
		if err != nil {
			keepConnection = false
			if entry != nil {
				entry.continuation = nil
			}
			return true, err
		}
		if done {
			break
		}
	}

	if ctx.Err() != nil {
		keepConnection = false
		if entry != nil {
			entry.continuation = nil
		}
		return true, nil
	}

	// Record continuation state for the next call on this socket. Only applies
	// when we're using the cached transport mode and the response carried an id.
	if useCachedContinuation && entry != nil && output.ResponseID != "" {
		entry.continuation = &wsContinuation{
			LastBodyJSONNoInput: requestBodyJSONWithoutInput(bodyMap),
			LastInput:           fullInput,
			LastResponseID:      output.ResponseID,
		}
	} else if entry != nil && output.ResponseID == "" {
		entry.continuation = nil
	}

	return true, nil
}

// computeWSContinuationDelta returns the input slice that should be sent as a
// follow-up `previous_response_id` request, or false if continuation cannot be
// applied (request shape mismatch or input prefix mismatch).
//
// Strategy:
//  1. The non-input portion of the body must match the previous request.
//  2. The current input must begin with the previous request's input.
//  3. After that prefix, we skip any contiguous run of assistant-output items
//     (message[role=assistant], reasoning, function_call). Those correspond
//     to the items the server will replay via previous_response_id.
//  4. Whatever remains is the delta the caller actually wants to add this turn
//     (function_call_output for tool results, plus any new user messages).
//
// If the prefix-match fails or the delta is empty, continuation cannot be
// applied and the caller should send the full request.
func computeWSContinuationDelta(body map[string]any, cont *wsContinuation) ([]any, bool) {
	if cont == nil || cont.LastResponseID == "" {
		return nil, false
	}
	if requestBodyJSONWithoutInput(body) != cont.LastBodyJSONNoInput {
		return nil, false
	}
	current, _ := body["input"].([]any)
	if len(current) < len(cont.LastInput) {
		return nil, false
	}
	prefix := current[:len(cont.LastInput)]
	if !responseInputsEqual(prefix, cont.LastInput) {
		return nil, false
	}
	rest := current[len(cont.LastInput):]
	// Skip the leading run of assistant-output items — the server will replay
	// them via previous_response_id.
	skip := 0
	for skip < len(rest) && isAssistantOutputItem(rest[skip]) {
		skip++
	}
	delta := rest[skip:]
	if len(delta) == 0 {
		// Nothing new to send. Don't apply continuation; let the caller
		// resend the full request (server would reject an empty input).
		return nil, false
	}
	out := make([]any, len(delta))
	copy(out, delta)
	return out, true
}

// isAssistantOutputItem reports whether the given item looks like an output
// item the server emitted as part of the previous response (and therefore
// will replay via previous_response_id).
func isAssistantOutputItem(item any) bool {
	m, ok := item.(map[string]any)
	if !ok {
		return false
	}
	t, _ := m["type"].(string)
	switch t {
	case "reasoning", "function_call":
		return true
	case "message":
		role, _ := m["role"].(string)
		return role == "assistant"
	}
	return false
}

// requestBodyJSONWithoutInput returns the canonical JSON of a request body with
// the "input", "previous_response_id", and "type" keys removed — the comparison
// key for continuation matching.
func requestBodyJSONWithoutInput(body map[string]any) string {
	clone := make(map[string]any, len(body))
	for k, v := range body {
		if k == "input" || k == "previous_response_id" || k == "type" {
			continue
		}
		clone[k] = v
	}
	out, _ := json.Marshal(clone)
	return string(out)
}

// responseInputsEqual deep-compares two response input slices via JSON equality.
func responseInputsEqual(a, b []any) bool {
	aj, err1 := json.Marshal(a)
	bj, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(aj) == string(bj)
}

// mapCodexEventFromMap maps a parsed Codex event map to a JSON string for the processor.
// This is the same as mapCodexEvent but works on already-parsed JSON.
func mapCodexEventFromMap(raw map[string]any) string {
	eventType, _ := raw["type"].(string)
	if eventType == "" {
		return ""
	}

	switch eventType {
	case "error":
		data, _ := json.Marshal(raw)
		return string(data)

	case "response.failed":
		if resp, ok := raw["response"].(map[string]any); ok {
			if errObj, ok := resp["error"].(map[string]any); ok {
				if msg, ok := errObj["message"].(string); ok && msg != "" {
					return fmt.Sprintf(`{"type":"error","code":"response_failed","message":%q}`, msg)
				}
			}
		}
		return `{"type":"error","code":"response_failed","message":"Codex response failed"}`

	case "response.done", "response.completed":
		if resp, ok := raw["response"].(map[string]any); ok {
			if status, ok := resp["status"].(string); ok {
				resp["status"] = normalizeCodexStatus(status)
			}
		}
		raw["type"] = "response.completed"
		result, _ := json.Marshal(raw)
		return string(result)

	default:
		data, _ := json.Marshal(raw)
		return string(data)
	}
}
