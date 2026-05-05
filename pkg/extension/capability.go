package extension

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

// InitParams is sent as params in the "init" request to an extension.
type InitParams struct {
	Version string `json:"version"`
	Cwd     string `json:"cwd"`
	// ConfigDirs is an ordered list of directories the extension may use to
	// read/write its configuration. Highest priority first (project-local
	// before global). Typically [projectDir/.fir, ~/.config/fir]. The SDK
	// exposes a friendly load_config()/config_path() API around this.
	ConfigDirs []string `json:"config_dirs,omitempty"`
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

// ApiSpec describes a wire-protocol adapter registered by an extension.
//
// Apis (wire protocols) are conceptually distinct from Providers (hosted
// services): an Api describes "how to talk on this HTTP/SSE shape", a
// Provider says "this is the hosted service that speaks <Api>". Multiple
// Providers can share an Api (e.g. several tenants on the same Cloud-
// Code-Assist family Api). Built-in Apis live in pkg/ai/providers/, but
// an extension can also ship an Api spec — useful when the wire shape
// is *data* (endpoints, headers, an envelope template) rather than
// custom Go code.
//
// The Kind field selects how the spec is interpreted. The ext-package is
// kind-agnostic: registration is delegated to an ApiKindHandler keyed by
// Kind, registered in pkg/ai/providers/. Currently:
//
//   - "decl-google": the spec carries a DeclGoogle JSON payload that
//     parameterises the generic Cloud-Code-Assist Gemini adapter
//     (StreamDeclGoogle).
//
// New kinds can be added by registering another ApiKindHandler — no
// changes needed in pkg/extension or the SDK protocol.
type ApiSpec struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// EnvKeysSpec is the wire shape mirroring ai.EnvKeySpec — the env-var(s) an
// extension-shipped provider sources its API key from.
type EnvKeysSpec struct {
	Primary       string   `json:"primary,omitempty"`
	Fallbacks     []string `json:"fallbacks,omitempty"`
	Authenticated bool     `json:"authenticated,omitempty"`
}

// ProviderModelSpec is the wire shape of a single model an extension-shipped
// provider declares at handshake. Maps to a subset of ai.Model.
type ProviderModelSpec struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name,omitempty"`
	BaseURL               string   `json:"base_url,omitempty"`
	Reasoning             bool     `json:"reasoning,omitempty"`
	Input                 []string `json:"input,omitempty"` // "text", "image", etc.
	ContextWindow         int      `json:"context_window,omitempty"`
	MaxTokens             int      `json:"max_tokens,omitempty"`
	CostInput             float64  `json:"cost_input,omitempty"`
	CostOutput            float64  `json:"cost_output,omitempty"`
	CostCacheRead         float64  `json:"cost_cache_read,omitempty"`
	CostCacheWrite        float64  `json:"cost_cache_write,omitempty"`
	ServerTools           []string `json:"server_tools,omitempty"`
	Compaction            bool     `json:"compaction,omitempty"`
	ReasoningEffortValues []string `json:"reasoning_effort_values,omitempty"`
	SWEScore              float64  `json:"swe_score,omitempty"`
	SWEInferred           bool     `json:"swe_inferred,omitempty"`
}

// ProviderSpec describes a hosted AI provider registered by an extension.
// All streaming, listing, and custom-id resolution for these providers is
// proxied to the extension via the provider/* JSON-RPC methods.
//
// Api selects the streaming dispatch mode:
//
//   - Empty: fir allocates a synthetic “ext:<id>“ Api and routes streams
//     back to the extension via provider/stream/start (full Python wire
//     code). Use this for providers whose wire protocol fir doesn't
//     speak natively.
//
//   - Set to a built-in Api identifier (e.g. “openai-completions“,
//     “anthropic-messages“): fir reuses its in-process stream function
//     for that wire protocol. The extension ships only metadata
//     (display name, env keys, OAuth wiring, model catalogue) — no
//     Python streaming code is needed. The Api must already be
//     registered in ai.DefaultRegistry (every built-in provider
//     self-registers its Api at init).
type ProviderSpec struct {
	ID                 string              `json:"id"`
	Api                string              `json:"api,omitempty"`
	DisplayName        string              `json:"display_name,omitempty"`
	ShortName          string              `json:"short_name,omitempty"`
	Priority           int                 `json:"priority,omitempty"`
	DefaultModelID     string              `json:"default_model_id,omitempty"`
	KeyLink            string              `json:"key_link,omitempty"`
	EnvKeys            EnvKeysSpec         `json:"env_keys,omitempty"`
	OAuthProviderID    string              `json:"oauth_provider_id,omitempty"`
	ClaimsModelIDGlobs []string            `json:"claims_model_id_globs,omitempty"`
	RefuseFuzzyMatch   bool                `json:"refuse_fuzzy_match,omitempty"`
	SupportsLiveList   bool                `json:"supports_live_list,omitempty"`
	SupportsCustomID   bool                `json:"supports_custom_id,omitempty"`
	Models             []ProviderModelSpec `json:"models,omitempty"`
}

// InitResult is the result returned by an extension in response to "init".
type InitResult struct {
	Name          string             `json:"name"`
	Tools         []ToolSpec         `json:"tools,omitempty"`
	Commands      []CommandSpec      `json:"commands,omitempty"`
	Events        []string           `json:"events,omitempty"`
	AuthProviders []AuthProviderSpec `json:"auth_providers,omitempty"`
	Apis          []ApiSpec          `json:"apis,omitempty"`
	Providers     []ProviderSpec     `json:"providers,omitempty"`
	// ToolNameMap is a static mapping from fir tool names to canonical
	// provider-side tool names (e.g. Claude Code's "KillShell" for fir's
	// "bash_kill"). Collected once at handshake by the extension manager
	// and consumed by provider adapters (currently pkg/ai/providers
	// anthropic OAuth mode) to translate tool names to and from the LLM.
	ToolNameMap map[string]string `json:"tool_name_map,omitempty"`
}

// extIdentRE validates extension-supplied identifiers (command names,
// auth provider IDs, hosted-provider IDs, Api IDs). All four use the
// same dash-separated lowercase grammar.
var extIdentRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// builtinAuthProviderIDs is the set of OAuth provider IDs reserved for
// built-in providers.  Sourced from the ai.RegisteredProvider registry: any
// record with a built-in source (Source == "builtin" or a "builtin-ext:"
// prefix from a builtin-scope extension) that declares an OAuthProviderID
// claims that ID.
//
// Defined as a function (not a static map) so that providers added at init
// time — including those registered after this file loads — are picked up.
func builtinAuthProviderIDs() map[string]bool {
	out := map[string]bool{}
	for _, p := range ai.GetRegisteredProviders() {
		if p.OAuthProviderID == "" {
			continue
		}
		if p.Source == "builtin" || strings.HasPrefix(p.Source, "builtin-ext:") {
			out[p.OAuthProviderID] = true
		}
	}
	return out
}

// ValidateCommandName checks that a command name is well-formed. It returns
// the name unchanged on success.
func ValidateCommandName(name string) error {
	if !extIdentRE.MatchString(name) {
		return fmt.Errorf("extension: command name %q must match [a-z][a-z0-9-]*", name)
	}
	return nil
}

// ValidateProviderID checks that a hosted-provider ID is well-formed and does
// not collide with a built-in provider's ID.
func ValidateProviderID(id string, allowBuiltinOverride bool) error {
	if !extIdentRE.MatchString(id) {
		return fmt.Errorf("extension: provider ID %q must match [a-z][a-z0-9-]*", id)
	}
	if !allowBuiltinOverride && ai.IsBuiltInProviderID(id) {
		return fmt.Errorf("extension: provider ID %q conflicts with built-in provider", id)
	}
	return nil
}

// ValidateApiID checks that an Api ID is well-formed and does not collide
// with a built-in Api in ai.DefaultRegistry (covers core "builtin"
// adapters and Apis shipped by builtin-scope extensions).
func ValidateApiID(id string, allowBuiltinOverride bool) error {
	if !extIdentRE.MatchString(id) {
		return fmt.Errorf("extension: api ID %q must match [a-z][a-z0-9-]*", id)
	}
	if !allowBuiltinOverride && ai.IsBuiltInApi(ai.Api(id)) {
		return fmt.Errorf("extension: api ID %q conflicts with built-in api", id)
	}
	return nil
}

// ValidateAuthProviderID checks that an auth provider ID is well-formed and
// does not collide with built-in provider IDs (unless allowBuiltinOverride is true).
func ValidateAuthProviderID(id string, allowBuiltinOverride bool) error {
	if !extIdentRE.MatchString(id) {
		return fmt.Errorf("extension: auth provider ID %q must match [a-z][a-z0-9-]*", id)
	}
	if !allowBuiltinOverride && builtinAuthProviderIDs()[id] {
		return fmt.Errorf("extension: auth provider ID %q conflicts with built-in provider", id)
	}
	return nil
}

// Handshake sends an "init" request to the extension process and waits for a
// response. If timeout is zero, defaults to 30 seconds (overridable via
// FIR_EXT_TIMEOUT environment variable, in seconds). The default is generous
// to accommodate very slow hardware (e.g. Raspberry Pi Zero) where Python
// interpreter startup alone can take many seconds. Returns the parsed
// InitResult.
func Handshake(proc *Process, cwd string, configDirs []string, timeout time.Duration) (*InitResult, error) {
	if timeout == 0 {
		timeout = 30 * time.Second
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

	if err := codec.WriteRequest(1, "init", InitParams{Version: "1", Cwd: cwd, ConfigDirs: configDirs}); err != nil {
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
		for _, ps := range result.Providers {
			allowOverride := proc.cfg.Scope == "builtin"
			if err := ValidateProviderID(ps.ID, allowOverride); err != nil {
				return nil, err
			}
		}
		for _, as := range result.Apis {
			allowOverride := proc.cfg.Scope == "builtin"
			if err := ValidateApiID(as.ID, allowOverride); err != nil {
				return nil, err
			}
			if as.Kind == "" {
				return nil, fmt.Errorf("extension: api %q missing kind", as.ID)
			}
		}
		return &result, nil
	}
}
