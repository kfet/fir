// Invocation persistence and restore — `fir -c` / `-r` / `/resume` re-apply
// the original session's --mcp-config / --extension / --model / etc. by
// default, so the resumed session has the same tool/MCP/extension set as the
// session it claims to continue.
package main

import (
	"fmt"
	"io"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/session/store"
)

// BuildInvocation extracts the user-intent runtime config from parsed CLI
// args. Returns nil if no relevant flag was set (so we don't pollute the
// header with an empty struct on every plain `fir` invocation).
//
// File-referencing flags (--mcp-config) are stamped with a sha256 of the
// referenced file at build time so a later resume can warn on drift.
func BuildInvocation(args *Args) *store.SessionInvocation {
	if args == nil {
		return nil
	}
	inv := &store.SessionInvocation{
		Provider:           args.Provider,
		Model:              args.Model,
		Models:             append([]string(nil), args.Models...),
		Thinking:           string(args.Thinking),
		SystemPrompt:       args.SystemPrompt,
		AppendSystemPrompt: args.AppendSystemPrompt,
		Tools:              append([]string(nil), args.Tools...),
		NoTools:            args.NoTools,
		MCPConfig:          args.MCPConfig,
		NoMCP:              args.NoMCP,
		Extensions:         append([]string(nil), args.Extensions...),
		DisabledExtensions: append([]string(nil), args.DisabledExtensions...),
		NoExtensions:       args.NoExtensions,
		Skills:             append([]string(nil), args.Skills...),
		NoSkills:           args.NoSkills,
		Themes:             append([]string(nil), args.Themes...),
		NoThemes:           args.NoThemes,
	}
	if inv.MCPConfig != "" {
		inv.MCPConfigSHA256 = store.HashFile(inv.MCPConfig)
	}
	if inv.IsEmpty() {
		return nil
	}
	return inv
}

// ApplyInvocation merges a persisted SessionInvocation onto parsed CLI args.
// Per-flag override semantics: any flag explicitly present on the current
// argv (recorded in args.Seen) wins over the persisted value; otherwise the
// persisted value fills in. List-valued flags (--extension, --tools,
// --models, --skill, --theme, --disable-extension) replace — they do not
// union — matching the rule that "if you pass any -e, you're choosing the
// set yourself".
//
// warn is invoked for non-fatal restore warnings (e.g. --mcp-config file
// missing or its contents changed since session start). May be nil.
func ApplyInvocation(args *Args, inv *store.SessionInvocation, warn func(string)) {
	if args == nil || inv == nil || inv.IsEmpty() {
		return
	}
	if args.NoRestoreConfig {
		return
	}
	if args.Seen == nil {
		args.Seen = make(map[string]bool)
	}
	if warn == nil {
		warn = func(string) {}
	}

	if !args.Seen["--provider"] && inv.Provider != "" {
		args.Provider = inv.Provider
	}
	if !args.Seen["--model"] && inv.Model != "" {
		args.Model = inv.Model
	}
	if !args.Seen["--models"] && len(inv.Models) > 0 {
		args.Models = append([]string(nil), inv.Models...)
	}
	if !args.Seen["--thinking"] && inv.Thinking != "" {
		args.Thinking = agent.ThinkingLevel(inv.Thinking)
	}
	if !args.Seen["--system-prompt"] && inv.SystemPrompt != "" {
		args.SystemPrompt = inv.SystemPrompt
	}
	if !args.Seen["--append-system-prompt"] && inv.AppendSystemPrompt != "" {
		args.AppendSystemPrompt = inv.AppendSystemPrompt
	}
	if !args.Seen["--tools"] && len(inv.Tools) > 0 {
		args.Tools = append([]string(nil), inv.Tools...)
	}
	if !args.Seen["--no-tools"] && inv.NoTools {
		args.NoTools = true
	}

	// MCP. --no-mcp wins (in either direction). If user didn't pass
	// --mcp-config and the persisted path is set, re-apply it; warn on
	// drift / missing.
	if !args.Seen["--no-mcp"] && inv.NoMCP {
		args.NoMCP = true
	}
	if !args.Seen["--mcp-config"] && inv.MCPConfig != "" && !args.NoMCP {
		args.MCPConfig = inv.MCPConfig
		if inv.MCPConfigSHA256 != "" {
			cur := store.HashFile(inv.MCPConfig)
			switch {
			case cur == "":
				warn(fmt.Sprintf("--mcp-config %s referenced by the resumed session is missing; continuing without it", inv.MCPConfig))
				args.MCPConfig = ""
			case cur != inv.MCPConfigSHA256:
				warn(fmt.Sprintf("--mcp-config %s has changed since the session was started; using current contents (pass --no-restore-config to ignore)", inv.MCPConfig))
			}
		}
	}

	// Extensions.
	if !args.Seen["--no-extensions"] && inv.NoExtensions {
		args.NoExtensions = true
	}
	if !args.Seen["--extension"] && len(inv.Extensions) > 0 {
		args.Extensions = append([]string(nil), inv.Extensions...)
	}
	if !args.Seen["--disable-extension"] && len(inv.DisabledExtensions) > 0 {
		args.DisabledExtensions = append([]string(nil), inv.DisabledExtensions...)
	}

	// Skills / themes.
	if !args.Seen["--no-skills"] && inv.NoSkills {
		args.NoSkills = true
	}
	if !args.Seen["--skill"] && len(inv.Skills) > 0 {
		args.Skills = append([]string(nil), inv.Skills...)
	}
	if !args.Seen["--no-themes"] && inv.NoThemes {
		args.NoThemes = true
	}
	if !args.Seen["--theme"] && len(inv.Themes) > 0 {
		args.Themes = append([]string(nil), inv.Themes...)
	}
}

// maybeRestoreInvocation is the startup-path entry point: if the session
// store reopened an existing session (sm.WasResumed()) that carries a
// stamped invocation, merge it onto args. Otherwise — fresh session —
// stamp the args's invocation onto the new session header so future
// resumes can restore it. Per-field drift/missing warnings for
// --mcp-config are written to stderr; no other notice is emitted.
//
// stderr may be nil (warnings are silently dropped).
func maybeRestoreInvocation(args *Args, sm *store.SessionStore, isResumed bool, stderr io.Writer) {
	if sm == nil {
		return
	}
	if isResumed {
		inv := sm.GetInvocation()
		if inv.IsEmpty() {
			return
		}
		if args.NoRestoreConfig {
			return
		}
		warn := func(msg string) {
			if stderr != nil {
				fmt.Fprintln(stderr, "fir:", msg)
			}
		}
		ApplyInvocation(args, inv, warn)
		return
	}
	// Brand-new session: stamp the invocation for later resumes.
	if inv := BuildInvocation(args); inv != nil {
		sm.StampInvocation(inv)
	}
}
