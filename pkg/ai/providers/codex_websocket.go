// Ported from: packages/ai/src/providers/openai-codex-responses.ts (WebSocket transport section)
// Upstream hash: 9e22d391
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
func acquireWebSocket(ctx context.Context, wsURL string, headers map[string]string, sessionID string) (conn *websocket.Conn, release func(keep bool), err error) {
	if sessionID == "" {
		c, err := connectWebSocket(ctx, wsURL, headers)
		if err != nil {
			return nil, nil, err
		}
		return c, func(keep bool) {
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
			return cached.conn, func(keep bool) {
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
			return nil, nil, err
		}
		return c, func(keep bool) {
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
			return nil, nil, ctx.Err()
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
		return nil, nil, dialErr
	}
	entry := &cachedWSConn{conn: c, busy: true}
	wsSessionCache[sessionID] = entry
	wsSessionCacheMu.Unlock()

	return c, func(keep bool) {
		wsSessionCacheMu.Lock()
		defer wsSessionCacheMu.Unlock()
		if !keep {
			c.Close(websocket.StatusNormalClosure, "done")
			if entry.idleTimer != nil {
				entry.idleTimer.Stop()
			}
			if wsSessionCache[sessionID] == entry {
				delete(wsSessionCache, sessionID)
			}
			return
		}
		entry.busy = false
		scheduleWSExpiry(sessionID, entry)
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
	if options != nil {
		sessionID = options.SessionID
	}

	conn, release, err := acquireWebSocket(ctx, wsURL, headers, sessionID)
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
	bodyMap["type"] = "response.create"
	wsMsg, err := json.Marshal(bodyMap)
	if err != nil {
		keepConnection = false
		return false, fmt.Errorf("marshaling ws message: %w", err)
	}

	if err := conn.Write(ctx, websocket.MessageText, wsMsg); err != nil {
		keepConnection = false
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
			return true, fmt.Errorf("request was aborted")
		}

		_, msg, err := conn.Read(ctx)
		if err != nil {
			if sawCompletion {
				break
			}
			keepConnection = false
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
			return true, err
		}
		if done {
			break
		}
	}

	if ctx.Err() != nil {
		keepConnection = false
	}

	return true, nil
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
