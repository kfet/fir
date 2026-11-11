package mcp

import (
	"context"
	"net/url"
	"os"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// SetFileApprovalFn installs the prompt that approves sending local files to
// MCP servers with the files extension. nil removes it, so files are refused
// unless a server's config sets files.autoApprove.
func (m *Manager) SetFileApprovalFn(fn FileApprovalFn) {
	if m == nil {
		return
	}
	if fn == nil {
		m.fileApproval.Store(nil)
		return
	}
	m.fileApproval.Store(&fn)
}

func (m *Manager) loadFileApproval() FileApprovalFn {
	if p := m.fileApproval.Load(); p != nil {
		return *p
	}
	return nil
}

// filesDirPath returns the private download directory, creating it once.
func (m *Manager) filesDirPath() (string, error) {
	m.filesDirOnce.Do(func() {
		m.filesDir, m.filesDirErr = os.MkdirTemp("", "fir-mcp-files-*")
	})
	return m.filesDir, m.filesDirErr
}

func filesEnabled(cfg ServerConfig) bool { return cfg.Files != nil && cfg.Files.Enabled }

// filesClientCapabilities is fir's default client capabilities (what the SDK
// uses when none are given) plus the files extension. The SDK sends them in
// the clientCapabilities _meta of every request on 2026-07-28 sessions.
func filesClientCapabilities() *sdk.ClientCapabilities {
	caps := &sdk.ClientCapabilities{RootsV2: &sdk.RootCapabilities{ListChanged: true}}
	caps.AddExtension(FilesExtensionID, filesClientCaps())
	return caps
}

// filesEnvFor returns the files context for a server session, or nil when
// the extension is off for it: not enabled in config, not advertised by the
// server, or a protocol revision before 2026-07-28.
func (m *Manager) filesEnvFor(name string, cfg ServerConfig, session *sdk.ClientSession) *filesEnv {
	if !filesEnabled(cfg) || session == nil {
		return nil
	}
	sf, ok := parseServerFiles(session.InitializeResult())
	if !ok {
		return nil
	}
	fx := &filesEnv{
		server:   name,
		cfg:      *cfg.Files,
		sf:       sf,
		approve:  m.loadFileApproval,
		dir:      m.filesDirPath,
		newHTTP:  defaultTransferClient,
		readFile: os.Open,
	}
	if cfg.Transport == "sse" || cfg.Transport == "streamable" {
		if u, err := url.Parse(cfg.URL); err == nil {
			fx.mcpURL = u
		}
		fx.token = func(ctx context.Context) (string, error) {
			sa, err := m.serverAuthFor(name, cfg)
			if err != nil || sa == nil {
				return "", err
			}
			tok, _, err := sa.accessToken(ctx)
			return tok, err
		}
	}
	return fx
}
