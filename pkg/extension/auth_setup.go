package extension

import (
	"context"
	"fmt"
	"time"

	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/session"
)

// AuthSetupOptions configures auth-provider-only extension startup.
// Use this when a mode needs OAuth providers registered before any session
// exists (e.g. ACP Initialize, which must advertise AuthMethods).
type AuthSetupOptions struct {
	ProjectDir         string
	Cwd                string
	Mode               string
	Version            string
	TrustStorePath     string
	ConfirmFn          ConfirmFunc
	EnabledNames       []string
	DisabledNames      []string
	ExtraExtensionDirs []string
}

// AuthSetupResult is the running state of auth-provider extensions.
// Started extensions are reused across sessions — the returned Names list
// should be added to session-level DisabledNames to avoid double-starting.
type AuthSetupResult struct {
	Manager *Manager
	// Names of extensions that were started (one per auth-provider extension).
	Names []string
}

// Stop shuts down the auth-provider extensions.
func (r *AuthSetupResult) Stop() {
	if r == nil || r.Manager == nil {
		return
	}
	_ = r.Manager.Stop()
}

// SetupAuthProviders discovers extensions declaring auth_providers in their
// frontmatter and starts them in parallel so that oauth.GetProvider(id)
// returns them for subsequent AuthMethod enumeration.
//
// Parallelism: the underlying Manager.Start already spawns one goroutine per
// eager extension, so each auth extension is handshaked concurrently.
//
// Returns (nil, nil) if ProjectDir is empty or no auth extensions are found.
func SetupAuthProviders(opts AuthSetupOptions) (*AuthSetupResult, error) {
	if opts.ProjectDir == "" {
		return nil, nil
	}

	// Discover all configs, then filter to auth-provider extensions.
	configs, err := Discover(opts.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	if len(opts.ExtraExtensionDirs) > 0 {
		existing := make(map[string]bool, len(configs))
		for _, c := range configs {
			existing[c.Name] = true
		}
		extra, xerr := DiscoverExtra(opts.ExtraExtensionDirs)
		if xerr == nil {
			for _, c := range extra {
				if !existing[c.Name] {
					configs = append(configs, c)
					existing[c.Name] = true
				}
			}
		}
	}

	var authNames []string
	for _, c := range configs {
		if len(c.AuthProviders) == 0 {
			continue
		}
		authNames = append(authNames, c.Name)
	}
	if len(authNames) == 0 {
		return nil, nil
	}

	// Reuse Manager's parallel startup path by restricting its allowlist
	// to just the auth-provider extensions. Preserve caller-supplied
	// disables; intersect enables if caller supplied them.
	allowed := authNames
	if len(opts.EnabledNames) > 0 {
		enabledSet := make(map[string]bool, len(opts.EnabledNames))
		for _, n := range opts.EnabledNames {
			enabledSet[n] = true
		}
		filtered := allowed[:0]
		for _, n := range authNames {
			if enabledSet[n] {
				filtered = append(filtered, n)
			}
		}
		allowed = filtered
	}
	if len(allowed) == 0 {
		return nil, nil
	}

	logger := firlog.With("component", "extension.auth")
	mgr := NewManager(logger)
	if opts.TrustStorePath != "" {
		mgr.SetTrustStore(NewTrustStoreWithPath(opts.TrustStorePath))
	}
	mgr.SetAllowedNames(allowed)
	if len(opts.DisabledNames) > 0 {
		mgr.SetDisabledNames(opts.DisabledNames)
	}
	if len(opts.ExtraExtensionDirs) > 0 {
		mgr.SetExtraExtensionDirs(opts.ExtraExtensionDirs)
	}
	mgr.ActiveMode = opts.Mode

	confirmFn := opts.ConfirmFn
	if confirmFn == nil {
		confirmFn = func(name, path string) bool {
			logger.Info("trusting auth extension", "ext", name, "path", path)
			return true
		}
	}
	mgr.ConfirmFn = confirmFn

	cwd := opts.Cwd
	if cwd == "" {
		cwd = opts.ProjectDir
	}

	api := &nopBridgeAPI{version: opts.Version, mode: opts.Mode}
	t0 := time.Now()
	if err := mgr.Start(context.Background(), opts.ProjectDir, cwd, api); err != nil {
		logger.Warn("auth-provider manager start failed", "err", err)
	}
	logger.Info("auth-provider extensions started",
		"count", len(allowed), "elapsed_ms", time.Since(t0).Milliseconds())

	// Compute the actually-started names (exclude those that failed).
	started := make([]string, 0, len(allowed))
	failedSet := make(map[string]bool)
	for _, f := range mgr.StartFailures() {
		failedSet[f.Name] = true
	}
	for _, n := range allowed {
		if !failedSet[n] {
			started = append(started, n)
		}
	}

	return &AuthSetupResult{Manager: mgr, Names: started}, nil
}

// nopBridgeAPI is a BridgeAPI that rejects session-scoped calls. Auth
// extensions only use auth/* helper RPCs (handled by Bridge directly), so
// they never invoke these methods during login discovery/handshake.
type nopBridgeAPI struct {
	version string
	mode    string
}

func (n *nopBridgeAPI) Exec(_ string, _ []string) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("exec not available outside a session")
}
func (n *nopBridgeAPI) SendMessage(_ CustomMessageSpec, _ *SendMessageOptions) {}
func (n *nopBridgeAPI) SendUserMessage(_ string, _ *SendUserMessageOptions)    {}
func (n *nopBridgeAPI) SetSessionName(_ string)                                {}
func (n *nopBridgeAPI) GetSessionName() string                                 { return "" }
func (n *nopBridgeAPI) GetSessionFile() string                                 { return "" }
func (n *nopBridgeAPI) GetSessionID() string                                   { return "" }
func (n *nopBridgeAPI) SetLabel(_, _ string)                                   {}
func (n *nopBridgeAPI) ClearLabel(_ string)                                    {}
func (n *nopBridgeAPI) SetModel(_ *ai.Model) bool                              { return false }
func (n *nopBridgeAPI) ContinueSession() error                                 { return nil }
func (n *nopBridgeAPI) SideQuery(_ string, _ *session.SideQueryOptions) (string, error) {
	return "", fmt.Errorf("side query not available outside a session")
}
func (n *nopBridgeAPI) RegisterTool(_ ToolDefinition)          {}
func (n *nopBridgeAPI) SetSessionData(_, _ string)             {}
func (n *nopBridgeAPI) GetSessionData(_ string) (string, bool) { return "", false }
func (n *nopBridgeAPI) CallTool(_ context.Context, _ string, _ map[string]any) (ToolResult, error) {
	return ToolResult{}, fmt.Errorf("call_tool not available outside a session")
}
func (n *nopBridgeAPI) ListTools() []ToolInfo   { return nil }
func (n *nopBridgeAPI) PrependContext(_ string) {}
func (n *nopBridgeAPI) ReportProgress(_ string) {}
func (n *nopBridgeAPI) Introspect() session.Introspection {
	return session.Introspection{Version: n.version, Mode: n.mode}
}

// Ensure agent import is used (ToolDefinition references agent.ToolDisplayHint
// transitively); keep this to avoid goimports churn.
