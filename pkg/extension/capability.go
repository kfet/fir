package extension

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"
)

// InitParams is sent as params in the "init" request to an extension.
type InitParams struct {
	Version string `json:"version"`
	Cwd     string `json:"cwd"`
}

// ToolSpec describes a tool registered by an extension.
type ToolSpec struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Parameters  map[string]any   `json:"parameters,omitempty"`
	DisplayHint *ToolDisplayHint `json:"display_hint,omitempty"`
}

// CommandSpec describes a slash command registered by an extension.
// The Name must be a valid identifier (letters, digits, hyphens) and must not
// conflict with any built-in slash command.
type CommandSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// AuthProviderSpec describes an OAuth auth provider registered by an extension.
type AuthProviderSpec struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	UsesCallbackServer bool   `json:"uses_callback_server"`
}

// InitResult is the result returned by an extension in response to "init".
type InitResult struct {
	Name          string             `json:"name"`
	Tools         []ToolSpec         `json:"tools,omitempty"`
	Commands      []CommandSpec      `json:"commands,omitempty"`
	Events        []string           `json:"events,omitempty"`
	AuthProviders []AuthProviderSpec `json:"auth_providers,omitempty"`
	// ToolNameMap is a static mapping from fir tool names to canonical
	// provider-side tool names (e.g. Claude Code's "KillShell" for fir's
	// "bash_kill"). Collected once at handshake by the extension manager
	// and consumed by provider adapters (currently pkg/ai/providers
	// anthropic OAuth mode) to translate tool names to and from the LLM.
	ToolNameMap map[string]string `json:"tool_name_map,omitempty"`
}

// commandNameRE validates extension command names: lowercase letters, digits,
// hyphens; must start with a letter.
var commandNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// authProviderIDRE validates auth provider IDs: same pattern as command names.
var authProviderIDRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// builtinAuthProviderIDs lists IDs reserved for built-in OAuth providers.
var builtinAuthProviderIDs = map[string]bool{
	"anthropic":          true,
	"github-copilot":     true,
	"google-gemini-cli":  true,
	"google-antigravity": true,
	"openai-codex":       true,
	"poe":                true,
}

// ValidateCommandName checks that a command name is well-formed. It returns
// the name unchanged on success.
func ValidateCommandName(name string) error {
	if !commandNameRE.MatchString(name) {
		return fmt.Errorf("extension: command name %q must match [a-z][a-z0-9-]*", name)
	}
	return nil
}

// ValidateAuthProviderID checks that an auth provider ID is well-formed and
// does not collide with built-in provider IDs (unless allowBuiltinOverride is true).
func ValidateAuthProviderID(id string, allowBuiltinOverride bool) error {
	if !authProviderIDRE.MatchString(id) {
		return fmt.Errorf("extension: auth provider ID %q must match [a-z][a-z0-9-]*", id)
	}
	if !allowBuiltinOverride && builtinAuthProviderIDs[id] {
		return fmt.Errorf("extension: auth provider ID %q conflicts with built-in provider", id)
	}
	return nil
}

// Handshake sends an "init" request to the extension process and waits for a
// response. If timeout is zero, defaults to 5 seconds (overridable via
// FIR_EXT_TIMEOUT environment variable, in seconds). Returns the parsed InitResult.
func Handshake(proc *Process, cwd string, timeout time.Duration) (*InitResult, error) {
	if timeout == 0 {
		timeout = 5 * time.Second
		if v := os.Getenv("FIR_EXT_TIMEOUT"); v != "" {
			if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
				timeout = time.Duration(secs) * time.Second
			}
		}
	}

	codec := proc.GetCodec()
	if codec == nil {
		return nil, fmt.Errorf("extension: process not started")
	}

	if err := codec.WriteRequest(1, "init", InitParams{Version: "1", Cwd: cwd}); err != nil {
		return nil, fmt.Errorf("extension: send init: %w", err)
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

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-timer.C:
		// Close the codec reader to unblock the goroutine.
		proc.CloseStdin()
		return nil, fmt.Errorf("extension: init handshake timed out")
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("extension: read init response: %w", r.err)
		}
		resp, ok := r.msg.(*Response)
		if !ok {
			return nil, fmt.Errorf("extension: expected Response, got %T", r.msg)
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		if resp.Result == nil {
			return nil, fmt.Errorf("extension: init response has no result")
		}
		var result InitResult
		if err := json.Unmarshal(*resp.Result, &result); err != nil {
			return nil, fmt.Errorf("extension: parse init result: %w", err)
		}
		validName, err := ValidateExtensionName(result.Name, proc.cfg.Name)
		if err != nil {
			return nil, err
		}
		result.Name = validName
		for _, cmd := range result.Commands {
			if err := ValidateCommandName(cmd.Name); err != nil {
				return nil, err
			}
		}
		for _, ap := range result.AuthProviders {
			allowOverride := proc.cfg.Scope == "builtin"
			if err := ValidateAuthProviderID(ap.ID, allowOverride); err != nil {
				return nil, err
			}
		}
		return &result, nil
	}
}
