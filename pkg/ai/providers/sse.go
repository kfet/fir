// SSE (Server-Sent Events) client for LLM provider streaming.
// Not ported from a single TS file — the TS version uses provider SDKs.
// This is the Go equivalent, using raw HTTP.
package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

// SSEError is a structured error from an HTTP-level SSE failure.
// It captures the status code, a human-readable message (extracted from JSON
// bodies when possible), and the request-id header for provider support.
type SSEError struct {
	StatusCode int
	Message    string // cleaned message (from JSON body or raw body)
	RequestID  string // from x-request-id or request-id response header
	RawBody    string // original response body
}

func (e *SSEError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s", e.StatusCode, e.Message)
	if e.RequestID != "" {
		fmt.Fprintf(&b, " (request-id: %s)", e.RequestID)
	}
	// Include raw body when the extracted message is too vague (e.g. just
	// "Error") so the user/logs get the actual API error details.
	if e.RawBody != "" && e.RawBody != e.Message && len(e.Message) < 20 {
		fmt.Fprintf(&b, "\n%s", e.RawBody)
	}
	return b.String()
}

// extractRequestID reads the request-id from common response headers.
func extractRequestID(h http.Header) string {
	if v := h.Get("X-Request-Id"); v != "" {
		return v
	}
	return h.Get("Request-Id")
}

// extractHTTPErrorMessage tries to pull a human-readable message from a JSON error
// body. Falls back to the raw body string.
func extractHTTPErrorMessage(body string) string {
	// Try {"error":{"message":"..."}} (OpenAI / Anthropic style)
	var wrapper struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &wrapper) == nil && wrapper.Error.Message != "" {
		return wrapper.Error.Message
	}
	// Try {"message":"..."} (simpler style)
	var simple struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(body), &simple) == nil && simple.Message != "" {
		return simple.Message
	}
	return body
}

// SSEEvent represents a single Server-Sent Event.
type SSEEvent struct {
	Event string
	Data  string
	ID    string
	Retry int
}

// SSEClient handles streaming SSE from an HTTP endpoint.
type SSEClient struct {
	HTTPClient *http.Client
}

// sharedTransport is reused by all provider HTTP calls. The Go default
// Transport sets TLSHandshakeTimeout to 10s, which is too aggressive on slow
// networks / corporate proxies and surfaces as "net/http: TLS handshake
// timeout" against api.anthropic.com et al. Cloned from http.DefaultTransport
// so HTTP_PROXY / HTTPS_PROXY env vars still apply.
var sharedTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSHandshakeTimeout = 30 * time.Second
	return t
}()

// DefaultSSEClient is a shared SSE client with sensible defaults.
var DefaultSSEClient = &SSEClient{
	HTTPClient: &http.Client{
		Timeout:   0, // No timeout for streaming
		Transport: sharedTransport,
	},
}

// Stream sends a POST request and streams SSE events back via a channel.
// The channel is closed when the stream ends or an error occurs.
// The error channel receives at most one error.
func (c *SSEClient) Stream(ctx context.Context, url string, headers map[string]string, body io.Reader) (<-chan SSEEvent, <-chan error) {
	events := make(chan SSEEvent, 64)
	errCh := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errCh)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
		if err != nil {
			errCh <- fmt.Errorf("create request: %w", err)
			return
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			errCh <- fmt.Errorf("http request: %w", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			bodyStr := strings.TrimSpace(string(bodyBytes))
			if bodyStr == "" {
				bodyStr = "(no body)"
			}
			errCh <- &SSEError{
				StatusCode: resp.StatusCode,
				Message:    extractHTTPErrorMessage(bodyStr),
				RequestID:  extractRequestID(resp.Header),
				RawBody:    bodyStr,
			}
			return
		}

		if err := parseSSE(resp.Body, events); err != nil {
			errCh <- err
		}
	}()

	return events, errCh
}

// parseSSE reads SSE events from a reader and sends them to the channel.
func parseSSE(r io.Reader, events chan<- SSEEvent) error {
	scanner := bufio.NewScanner(r)
	// Allow large lines (some providers send huge JSON in a single data line)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var current SSEEvent
	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// Empty line = dispatch event
			if len(dataLines) > 0 {
				current.Data = strings.Join(dataLines, "\n")
				events <- current
				current = SSEEvent{}
				dataLines = nil
			}
			continue
		}

		if strings.HasPrefix(line, ":") {
			// Comment, skip
			continue
		}

		field, value, _ := strings.Cut(line, ":")
		// Per SSE spec: if value starts with a space, remove it
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}

		switch field {
		case "event":
			current.Event = value
		case "data":
			dataLines = append(dataLines, value)
		case "id":
			current.ID = value
		case "retry":
			// Parse retry delay (not typically used by LLM providers)
		default:
			// Unknown field, treat entire line as field name with empty value
		}
	}

	// If there's a pending event without trailing newline, dispatch it
	if len(dataLines) > 0 {
		current.Data = strings.Join(dataLines, "\n")
		events <- current
	}

	return scanner.Err()
}

// errorStreamProvider creates an AssistantMessageEventStream that immediately
// resolves with an error result. Used when a provider cannot start streaming
// (e.g., missing API key).
func errorStreamProvider(model *ai.Model, errMsg string) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		result := &ai.AssistantMessage{
			Role:         ai.RoleAssistant,
			Provider:     model.Provider,
			Api:          model.Api,
			Model:        model.ID,
			Content:      []ai.AssistantContent{},
			StopReason:   ai.StopReasonError,
			ErrorMessage: errMsg,
		}
		stream.Push(ai.AssistantMessageEvent{
			Type:   ai.EventError,
			Reason: ai.StopReasonError,
			Error:  result,
		})
		stream.End(nil)
	}()
	return stream
}

// DoJSONRequest sends a POST request and reads the full response body.
// Used for non-streaming API calls and error inspection.
func DoJSONRequest(ctx context.Context, url string, headers map[string]string, body io.Reader) ([]byte, int, error) {
	client := &http.Client{Timeout: 60 * time.Second, Transport: sharedTransport}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	return data, resp.StatusCode, nil
}
