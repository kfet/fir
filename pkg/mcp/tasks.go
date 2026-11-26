package mcp

// Client side of the MCP tasks extension (io.modelcontextprotocol/tasks,
// https://github.com/modelcontextprotocol/ext-tasks).
//
// fir declares the extension in its client capabilities, which the go-sdk
// sends in the clientCapabilities _meta of every request on 2026-07-28
// sessions. A server may then answer tools/call with a task handle
// (resultType "task") instead of a result. The SDK cannot decode that, so
// extConnection holds the handle back, taskRunner polls tasks/get until the
// task ends, answers input_required requests through fir's elicitation and
// sampling handlers, and the final result (or error) is handed to the SDK as
// the response to the original tools/call. The model sees an ordinary tool
// result.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	firlog "github.com/kfet/fir/pkg/log"
)

// TasksExtensionID identifies the MCP tasks extension.
const TasksExtensionID = "io.modelcontextprotocol/tasks"

const (
	defaultTaskPoll = time.Second
	minTaskPoll     = 100 * time.Millisecond
	maxTaskPoll     = 30 * time.Second
)

// taskHandle is the part of a CreateTaskResult / GetTaskResult fir reads.
type taskHandle struct {
	TaskID         string                     `json:"taskId"`
	Status         string                     `json:"status"`
	StatusMessage  string                     `json:"statusMessage,omitempty"`
	PollIntervalMs *int64                     `json:"pollIntervalMs,omitempty"`
	Result         json.RawMessage            `json:"result,omitempty"`
	Error          *jsonrpc.Error             `json:"error,omitempty"`
	InputRequests  map[string]json.RawMessage `json:"inputRequests,omitempty"`
}

// parseTaskHandle reports whether raw is a CreateTaskResult.
func parseTaskHandle(raw json.RawMessage) (*taskHandle, bool) {
	var probe struct {
		ResultType string `json:"resultType"`
	}
	if json.Unmarshal(raw, &probe) != nil || probe.ResultType != "task" {
		return nil, false
	}
	var t taskHandle
	if json.Unmarshal(raw, &t) != nil || t.TaskID == "" {
		return nil, false
	}
	return &t, true
}

// taskCapableMethod reports whether fir accepts a task handle as the result
// of method. Only tools/call: it is the one call whose result fir hands to
// the model, and the one servers make long-running.
func taskCapableMethod(method string) bool { return method == "tools/call" }

// taskRunner drives held tasks to completion. Its handlers answer the
// server's input requests; nil handlers decline (elicitation) or fail the
// request (sampling).
type taskRunner struct {
	server string
	elicit func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error)
	sample func(context.Context, *sdk.CreateMessageRequest) (*sdk.CreateMessageResult, error)
	// sleep waits d or until ctx ends; replaceable in tests.
	sleep func(ctx context.Context, d time.Duration) error
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run polls task until it ends and returns the response to deliver under
// id. It returns nil when ctx ends first (the SDK cancelled the call or the
// connection closed); the task is then cancelled on the server, best effort.
func (r *taskRunner) run(ctx context.Context, c *extConnection, method string, id jsonrpc.ID, task *taskHandle) *jsonrpc.Response {
	firlog.Debug("MCP task started", "server", r.server, "method", method, "task", task.TaskID)
	sleep := r.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	cur := task
	answered := map[string]bool{}
	for {
		switch cur.Status {
		case "completed":
			res := cur.Result
			if len(res) == 0 {
				res = json.RawMessage(`{}`)
			}
			return &jsonrpc.Response{ID: id, Result: res}
		case "failed":
			werr := cur.Error
			if werr == nil {
				werr = &jsonrpc.Error{Code: -32603, Message: "task failed: " + cur.StatusMessage}
			}
			return &jsonrpc.Response{ID: id, Error: werr}
		case "cancelled":
			msg := "task cancelled by the server"
			if cur.StatusMessage != "" {
				msg += ": " + cur.StatusMessage
			}
			return &jsonrpc.Response{ID: id, Error: &jsonrpc.Error{Code: -32603, Message: msg}}
		case "input_required":
			if err := r.answer(ctx, c, cur, answered); err != nil {
				if ctx.Err() != nil {
					r.cancel(c, cur.TaskID)
					return nil
				}
				r.cancel(c, cur.TaskID)
				return &jsonrpc.Response{ID: id, Error: &jsonrpc.Error{Code: -32603, Message: err.Error()}}
			}
		}
		if err := sleep(ctx, pollInterval(cur)); err != nil {
			r.cancel(c, cur.TaskID)
			return nil
		}
		var next taskHandle
		if err := c.Call(ctx, "tasks/get", map[string]string{"taskId": task.TaskID}, &next); err != nil {
			if ctx.Err() != nil {
				r.cancel(c, task.TaskID)
				return nil
			}
			return &jsonrpc.Response{ID: id, Error: &jsonrpc.Error{Code: -32603, Message: "tasks/get: " + err.Error()}}
		}
		if next.TaskID == "" {
			next.TaskID = task.TaskID
		}
		if next.PollIntervalMs == nil {
			next.PollIntervalMs = cur.PollIntervalMs
		}
		cur = &next
	}
}

func pollInterval(t *taskHandle) time.Duration {
	d := defaultTaskPoll
	if t.PollIntervalMs != nil {
		d = time.Duration(*t.PollIntervalMs) * time.Millisecond
	}
	return min(max(d, minTaskPoll), maxTaskPoll)
}

// cancel asks the server to cancel a task, best effort, off ctx (which is
// usually already done).
func (r *taskRunner) cancel(c *extConnection, taskID string) {
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := c.Call(ctx, "tasks/cancel", map[string]string{"taskId": taskID}, nil); err != nil {
		firlog.Debug("MCP tasks/cancel failed", "server", r.server, "task", taskID, "err", err)
	}
}

// answer fulfils the task's outstanding input requests and sends them with
// tasks/update. Keys already answered are skipped: the server may still list
// them until it has processed the update.
func (r *taskRunner) answer(ctx context.Context, c *extConnection, t *taskHandle, answered map[string]bool) error {
	responses := map[string]any{}
	for key, raw := range t.InputRequests {
		if answered[key] {
			continue
		}
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return fmt.Errorf("task input request %q: %w", key, err)
		}
		res, err := r.fulfil(ctx, req.Method, req.Params)
		if err != nil {
			return fmt.Errorf("task input request %q (%s): %w", key, req.Method, err)
		}
		responses[key] = res
	}
	if len(responses) == 0 {
		return nil
	}
	params := map[string]any{"taskId": t.TaskID, "inputResponses": responses}
	if err := c.Call(ctx, "tasks/update", params, nil); err != nil {
		return fmt.Errorf("tasks/update: %w", err)
	}
	for key := range responses {
		answered[key] = true
	}
	return nil
}

func (r *taskRunner) fulfil(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "elicitation/create":
		var p sdk.ElicitParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return elicitHandler(r.elicit)(ctx, &sdk.ElicitRequest{Params: &p})
	case "sampling/createMessage":
		if r.sample == nil {
			return nil, fmt.Errorf("sampling is not available")
		}
		var p sdk.CreateMessageParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return r.sample(ctx, &sdk.CreateMessageRequest{Params: &p})
	default:
		return nil, fmt.Errorf("unsupported input request")
	}
}
