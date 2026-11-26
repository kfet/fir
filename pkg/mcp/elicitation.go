package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultElicitFn returns an elicitation handler that always declines without
// prompting the user. This is the safe default for non-interactive (headless)
// sessions where no UI is available to present the request.
//
// Assign Manager.ElicitationFn to an interactive implementation to allow MCP
// servers to ask the user questions through fir's UI.
func DefaultElicitFn(_ context.Context, _ *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
	return &sdk.ElicitResult{Action: "decline"}, nil
}

// ElicitFormResult is a convenience wrapper that returns an "accept" result
// with the given key-value content, suitable for responding to form-mode
// elicitations.
func ElicitFormResult(content map[string]any) *sdk.ElicitResult {
	return &sdk.ElicitResult{
		Action:  "accept",
		Content: content,
	}
}

// ElicitMessage extracts the human-readable message from an ElicitRequest,
// falling back to a generic description if the message is empty.
func ElicitMessage(req *sdk.ElicitRequest) string {
	if req.Params.Message != "" {
		return req.Params.Message
	}
	switch req.Params.Mode {
	case "url":
		return fmt.Sprintf("Open URL: %s", req.Params.URL)
	default:
		return "An MCP server is requesting input."
	}
}

// elicitHandler returns fn if non-nil, otherwise DefaultElicitFn.
// This ensures the SDK always has a handler wired up, so MCP servers receive
// a proper "decline" response rather than a JSON-RPC "not supported" error.
func elicitHandler(fn ElicitFn) ElicitFn {
	if fn != nil {
		return fn
	}
	return DefaultElicitFn
}

// LateElicit is an ElicitFn whose front end is installed after MCP starts:
// the interactive UI is built after the session, but servers must learn at
// connect that fir can elicit. Until Set is called, Ask declines.
type LateElicit struct {
	fn atomic.Pointer[ElicitFn]
}

// Set installs fn (nil reverts to declining).
func (l *LateElicit) Set(fn ElicitFn) {
	if fn == nil {
		l.fn.Store(nil)
		return
	}
	l.fn.Store(&fn)
}

// Ask answers req with the installed front end, or declines.
func (l *LateElicit) Ask(ctx context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
	if p := l.fn.Load(); p != nil {
		return (*p)(ctx, req)
	}
	return DefaultElicitFn(ctx, req)
}

// ElicitField is one field of a form elicitation's requested schema. The
// schema is a flat object of primitive properties (string, number,
// integer, boolean, or a string enum).
type ElicitField struct {
	Name        string
	Title       string
	Description string
	Type        string // "string", "number", "integer" or "boolean"
	Enum        []string
	Required    bool
	Default     any
}

// Label is the field's title, else its name.
func (f ElicitField) Label() string {
	if f.Title != "" {
		return f.Title
	}
	return f.Name
}

// ElicitFields lists the fields of a form elicitation, required first, then
// by name.
func ElicitFields(req *sdk.ElicitRequest) ([]ElicitField, error) {
	if req.Params == nil || req.Params.RequestedSchema == nil {
		return nil, nil
	}
	b, err := json.Marshal(req.Params.RequestedSchema)
	if err != nil {
		return nil, err
	}
	var schema struct {
		Properties map[string]struct {
			Type        string   `json:"type"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Enum        []string `json:"enum"`
			OneOf       []struct {
				Const string `json:"const"`
			} `json:"oneOf"`
			Default any `json:"default"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(b, &schema); err != nil {
		return nil, fmt.Errorf("requested schema: %w", err)
	}
	fields := make([]ElicitField, 0, len(schema.Properties))
	for name, p := range schema.Properties {
		f := ElicitField{
			Name: name, Title: p.Title, Description: p.Description,
			Type: p.Type, Enum: p.Enum, Default: p.Default,
			Required: slices.Contains(schema.Required, name),
		}
		for _, o := range p.OneOf {
			f.Enum = append(f.Enum, o.Const)
		}
		if f.Type == "" {
			f.Type = "string"
		}
		fields = append(fields, f)
	}
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].Required != fields[j].Required {
			return fields[i].Required
		}
		return fields[i].Name < fields[j].Name
	})
	return fields, nil
}

// ParseElicitValue converts what the user typed for f into the JSON value
// the schema asks for. ok is false when the input is empty (skip the
// field).
func ParseElicitValue(f ElicitField, input string) (v any, ok bool, err error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return nil, false, nil
	}
	switch f.Type {
	case "boolean":
		switch strings.ToLower(s) {
		case "y", "yes", "true", "1":
			return true, true, nil
		case "n", "no", "false", "0":
			return false, true, nil
		}
		return nil, false, fmt.Errorf("%s: answer yes or no", f.Label())
	case "integer":
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, false, fmt.Errorf("%s: enter a whole number", f.Label())
		}
		return n, true, nil
	case "number":
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, false, fmt.Errorf("%s: enter a number", f.Label())
		}
		return n, true, nil
	}
	if len(f.Enum) > 0 && !slices.Contains(f.Enum, s) {
		return nil, false, fmt.Errorf("%s: choose one of %s", f.Label(), strings.Join(f.Enum, ", "))
	}
	return s, true, nil
}
