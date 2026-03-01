package extproc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// InitParams is sent as params in the "init" request to an extension.
type InitParams struct {
	Version string `json:"version"`
	Cwd     string `json:"cwd"`
}

// ToolSpec describes a tool registered by an extension.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// InitResult is the result returned by an extension in response to "init".
type InitResult struct {
	Name   string     `json:"name"`
	Tools  []ToolSpec `json:"tools,omitempty"`
	Events []string   `json:"events,omitempty"`
}

// Handshake sends an "init" request to the extension process and waits
// up to 5 seconds for a response. Returns the parsed InitResult.
func Handshake(proc *Process, cwd string) (*InitResult, error) {
	codec := proc.GetCodec()
	if codec == nil {
		return nil, fmt.Errorf("extproc: process not started")
	}

	if err := codec.WriteRequest(1, "init", InitParams{Version: "1", Cwd: cwd}); err != nil {
		return nil, fmt.Errorf("extproc: send init: %w", err)
	}

	type readResult struct {
		msg any
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		msg, err := codec.ReadMessage()
		ch <- readResult{msg, err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	select {
	case <-ctx.Done():
		// The reader goroutine will be unblocked when the caller
		// calls proc.Stop(), which closes the process's pipes.
		return nil, fmt.Errorf("extproc: init handshake timed out")
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("extproc: read init response: %w", r.err)
		}
		resp, ok := r.msg.(*Response)
		if !ok {
			return nil, fmt.Errorf("extproc: expected Response, got %T", r.msg)
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		if resp.Result == nil {
			return nil, fmt.Errorf("extproc: init response has no result")
		}
		var result InitResult
		if err := json.Unmarshal(*resp.Result, &result); err != nil {
			return nil, fmt.Errorf("extproc: parse init result: %w", err)
		}
		return &result, nil
	}
}
