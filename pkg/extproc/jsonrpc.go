package extproc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Error represents a JSON-RPC 2.0 error object.
type Error struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    *json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      any              `json:"id"`
	Method  string           `json:"method"`
	Params  *json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      any              `json:"id"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

// Notification is a JSON-RPC 2.0 notification (no id).
type Notification struct {
	JSONRPC string           `json:"jsonrpc"`
	Method  string           `json:"method"`
	Params  *json.RawMessage `json:"params,omitempty"`
}

// Codec reads and writes JSON-RPC 2.0 messages over newline-delimited JSON.
// Write methods are safe for concurrent use.
type Codec struct {
	reader  *bufio.Reader
	writer  io.Writer
	encoder *json.Encoder
	writeMu sync.Mutex
}

// NewCodec creates a Codec from separate reader and writer.
func NewCodec(r io.Reader, w io.Writer) *Codec {
	return &Codec{
		reader:  bufio.NewReader(r),
		writer:  w,
		encoder: json.NewEncoder(w),
	}
}

// rawMessage is used internally to classify incoming messages.
type rawMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      any              `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  *json.RawMessage `json:"params,omitempty"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

// ReadMessage reads one newline-delimited JSON-RPC message and returns
// *Request, *Response, or *Notification.
func (c *Codec) ReadMessage() (any, error) {
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	var raw rawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON-RPC message: %w", err)
	}

	// Response: has id but no method
	if raw.Method == "" {
		return &Response{
			JSONRPC: raw.JSONRPC,
			ID:      raw.ID,
			Result:  raw.Result,
			Error:   raw.Error,
		}, nil
	}

	// Request: has method and id
	if raw.ID != nil {
		return &Request{
			JSONRPC: raw.JSONRPC,
			ID:      raw.ID,
			Method:  raw.Method,
			Params:  raw.Params,
		}, nil
	}

	// Notification: has method but no id
	return &Notification{
		JSONRPC: raw.JSONRPC,
		Method:  raw.Method,
		Params:  raw.Params,
	}, nil
}

// WriteRequest writes a JSON-RPC 2.0 request.
func (c *Codec) WriteRequest(id int, method string, params any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	msg := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params}
	return c.encoder.Encode(msg)
}

// WriteNotification writes a JSON-RPC 2.0 notification (no id).
func (c *Codec) WriteNotification(method string, params any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	msg := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", method, params}
	return c.encoder.Encode(msg)
}

// WriteResponse writes a JSON-RPC 2.0 response.
// Per JSON-RPC 2.0, a successful response (no error) always includes "result",
// even if null.
func (c *Codec) WriteResponse(id any, result any, rpcErr *Error) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// If this is a success response (no error) and result is nil,
	// we must emit "result": null rather than omitting the field.
	type respWithResult struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      any            `json:"id"`
		Result  any            `json:"result"`
		Error   *Error         `json:"error,omitempty"`
	}
	type respWithError struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      any            `json:"id"`
		Result  any            `json:"result,omitempty"`
		Error   *Error         `json:"error"`
	}

	if rpcErr != nil {
		return c.encoder.Encode(respWithError{"2.0", id, result, rpcErr})
	}
	return c.encoder.Encode(respWithResult{"2.0", id, result, nil})
}
