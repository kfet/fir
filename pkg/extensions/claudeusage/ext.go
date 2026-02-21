// Package claudeusage displays Claude rate-limit usage in the session footer.
//
// It fetches utilization data from the Anthropic OAuth usage API using the
// existing Anthropic provider credentials managed by AuthStorage. If no
// OAuth credentials are configured, it shows a prompt to use /login.
//
// Import this package to enable the extension:
//
//	import _ "github.com/kfet/fir/pkg/extensions/claudeusage"
package claudeusage

import (
	"strings"

	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/extension"
)

const (
	extName     = "claude-usage"
	statusKey   = "claude-usage"
	commandName = "claude-usage"
	providerID  = "anthropic"
)

func init() {
	extension.Register(extName, func(api extension.API) {
		// getOAuthToken retrieves the Anthropic OAuth access token from the
		// model registry's auth storage. Returns ("", false) if no OAuth
		// credentials are configured.
		getOAuthToken := func(ctx extension.Context) (string, bool) {
			mr := ctx.ModelRegistry()
			if mr == nil {
				return "", false
			}
			as := mr.AuthStorage()
			if as == nil {
				return "", false
			}
			cred := as.Get(providerID)
			if cred == nil || cred.Type != core.CredentialTypeOAuth {
				return "", false
			}
			// GetApiKey handles token refresh automatically.
			token := as.GetApiKey(providerID)
			return token, token != ""
		}

		// updateStatus fetches usage and updates the footer.
		updateStatus := func(ctx extension.Context) {
			token, ok := getOAuthToken(ctx)
			if !ok {
				// No OAuth credentials — don't show anything.
				// The user may be using an API key which doesn't support usage.
				ctx.UI().SetStatus(statusKey, "")
				return
			}

			data, err := fetchUsage(token)
			if err == errUnauthorized {
				ctx.UI().SetStatus(statusKey, "◎ Claude token expired — /login")
				return
			}
			if err != nil {
				ctx.UI().SetStatus(statusKey, "◎ Claude usage error")
				return
			}

			ctx.UI().SetStatus(statusKey, formatStatusLine(data))
		}

		// Update on session start.
		api.On("session_start", func(event *extension.Event, ctx extension.Context) (any, error) {
			updateStatus(ctx)
			return nil, nil
		})

		// Update after each agent turn.
		api.On("agent_end", func(event *extension.Event, ctx extension.Context) (any, error) {
			updateStatus(ctx)
			return nil, nil
		})

		// /claude-usage command: show detailed Claude usage.
		api.RegisterCommand(commandName, extension.Command{
			Description: "Show Claude rate limit usage",
			Handler: func(args string, ctx extension.CommandContext) error {
				args = strings.TrimSpace(args)
				return handleShow(ctx)
			},
		})
	})
}

// handleShow displays detailed Claude usage information.
func handleShow(ctx extension.CommandContext) error {
	mr := ctx.ModelRegistry()
	if mr == nil {
		ctx.UI().Notify("Model registry not available.", "error")
		return nil
	}
	as := mr.AuthStorage()
	if as == nil {
		ctx.UI().Notify("Auth storage not available.", "error")
		return nil
	}

	cred := as.Get(providerID)
	if cred == nil || cred.Type != core.CredentialTypeOAuth {
		ctx.UI().Notify("Claude usage requires Anthropic OAuth login.\nRun /login to authenticate.", "warning")
		return nil
	}

	token := as.GetApiKey(providerID)
	if token == "" {
		ctx.UI().Notify("Failed to get Anthropic token. Try /login to re-authenticate.", "warning")
		return nil
	}

	data, err := fetchUsage(token)
	if err == errUnauthorized {
		ctx.UI().Notify("Anthropic token expired or invalid. Run /login to re-authenticate.", "warning")
		return nil
	}
	if err != nil {
		ctx.UI().Notify("Failed to fetch Claude usage: "+err.Error(), "error")
		return nil
	}

	ctx.UI().Notify(formatFull(data), "info")
	ctx.UI().SetStatus(statusKey, formatStatusLine(data))
	return nil
}
