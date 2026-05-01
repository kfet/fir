package extension

// CLI verb dispatch — top-level `fir <verb>` names registered by extensions
// in their comment frontmatter. The mechanism is described in
// docs/design/extension-cli-verbs.md.
//
// At fir startup (before normal subcommand dispatch) we build a verb-table
// keyed by the verb name. When `fir <verb> <args...>` matches an entry, we
// spawn the extension as a subprocess, perform the standard init handshake,
// then send a single `cli_invoke` request carrying argv. The extension drives
// stdio via the existing JSON-RPC bridge using three notifications:
//
//   ext → fir : cli_stdout {data: "..."}        write to fir's stdout
//   ext → fir : cli_stderr {data: "..."}        write to fir's stderr
//   fir → ext : cli_stdin  {data: "..." | eof: true}   forward fir's stdin lines
//   fir → ext : cli_signal {name: "SIGINT"|...}        forward signals
//
// The `cli_invoke` response is `{"exit_code": N}`, which fir uses as its
// own exit code. Extensions may also call any standard bridge method that
// does not require a session (e.g. exec, side_query in future) — but verb
// dispatch runs cold; no Manager, no AgentSession.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/kfet/fir/pkg/extension/sdk"
)

// CLIVerbBinding pairs a verb name with the extension that claims it.
type CLIVerbBinding struct {
	Verb string
	Ext  ExtProcConfig
}

// DiscoverCLIVerbs scans all available extension sources (builtin, global,
// project, package) and returns the verb→extension binding table. Returns
// an error when two extensions declare the same verb.
//
// Reserved (fir-builtin) verbs supplied via reserved are excluded from the
// table; if any extension claims one, an error is returned.
func DiscoverCLIVerbs(projectDir string, extraDirs []string, extraFiles []string, reserved []string) ([]CLIVerbBinding, error) {
	configs, err := Discover(projectDir)
	if err != nil {
		return nil, err
	}
	if len(extraDirs) > 0 {
		extras, _ := DiscoverExtra(extraDirs)
		// Merge with project/global precedence already applied by Discover;
		// drop duplicates by name.
		seen := make(map[string]bool, len(configs))
		for _, c := range configs {
			seen[c.Name] = true
		}
		for _, c := range extras {
			if !seen[c.Name] {
				configs = append(configs, c)
				seen[c.Name] = true
			}
		}
	}
	if len(extraFiles) > 0 {
		seen := make(map[string]bool, len(configs))
		for _, c := range configs {
			seen[c.Name] = true
		}
		for _, c := range ConfigsFromFiles(extraFiles) {
			if !seen[c.Name] {
				configs = append(configs, c)
				seen[c.Name] = true
			}
		}
	}

	return buildVerbTable(configs, reserved)
}

// buildVerbTable is the pure collision/reservation logic split out from
// DiscoverCLIVerbs so it can be unit-tested without touching the filesystem.
func buildVerbTable(configs []ExtProcConfig, reserved []string) ([]CLIVerbBinding, error) {
	reservedSet := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		reservedSet[r] = true
	}

	byVerb := make(map[string]CLIVerbBinding)
	for _, cfg := range configs {
		for _, verb := range cfg.CLIVerbs {
			verb = strings.TrimSpace(verb)
			if verb == "" {
				continue
			}
			if reservedSet[verb] {
				return nil, fmt.Errorf("extension %q claims reserved verb %q (built-in fir subcommand)", cfg.Name, verb)
			}
			if other, ok := byVerb[verb]; ok {
				return nil, fmt.Errorf("verb %q claimed by both extensions %q and %q — disable one to resolve",
					verb, other.Ext.Name, cfg.Name)
			}
			byVerb[verb] = CLIVerbBinding{Verb: verb, Ext: cfg}
		}
	}

	out := make([]CLIVerbBinding, 0, len(byVerb))
	for _, b := range byVerb {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Verb < out[j].Verb })
	return out, nil
}

// LookupCLIVerb returns the binding for verb, or nil if not registered.
// projectDir is the project root (typically the cwd). reserved is the set of
// fir-builtin subcommand names that must not be shadowed.
func LookupCLIVerb(verb string, projectDir string, reserved []string) (*CLIVerbBinding, error) {
	bindings, err := DiscoverCLIVerbs(projectDir, nil, nil, reserved)
	if err != nil {
		return nil, err
	}
	for i := range bindings {
		if bindings[i].Verb == verb {
			return &bindings[i], nil
		}
	}
	return nil, nil
}

// VerbInvokeParams is the params object for the cli_invoke request.
type VerbInvokeParams struct {
	Verb        string   `json:"verb"`
	Argv        []string `json:"argv"`
	Cwd         string   `json:"cwd"`
	StdinIsTTY  bool     `json:"stdin_is_tty"`
	StdoutIsTTY bool     `json:"stdout_is_tty"`
	StderrIsTTY bool     `json:"stderr_is_tty"`
}

// VerbInvokeResult is the response shape for cli_invoke.
type VerbInvokeResult struct {
	ExitCode int `json:"exit_code"`
}

// RunCLIVerb spawns the extension associated with binding, performs the init
// handshake, sends `cli_invoke` with argv, and pumps stdio notifications
// between the extension and fir's real TTY/stdio. Returns the exit code the
// extension reported (or a non-zero code on protocol failure).
//
// argv is the list of arguments after the verb itself (i.e. os.Args[2:]).
func RunCLIVerb(binding *CLIVerbBinding, argv []string, cwd string, projectDir string) (int, error) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Build SDK env (Python interpreter needs PYTHONPATH for fir_ext).
	// Failure here is fatal: without the SDK, builtin Python extensions
	// can't import fir_ext and the handshake will time out with a
	// confusing message. Surface the real cause instead.
	sdkPath, err := sdk.EnsureExtracted()
	if err != nil {
		return 1, fmt.Errorf("extract extension SDK: %w", err)
	}
	env := sdk.SDKEnv(sdkPath)

	proc := NewProcess(binding.Ext, env, logger)
	if err := proc.Start(); err != nil {
		return 1, fmt.Errorf("spawn extension %q: %w", binding.Ext.Name, err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = proc.Stop(ctx)
	}()

	// Standard handshake. Verb mode does not advertise config_dirs (no
	// session, no project trust dance — extensions that need filesystem
	// state should compute paths from $XDG_*).
	if _, err := Handshake(proc, cwd, []string{filepath.Join(projectDir, ".fir"), userConfigFirDir()}, 0); err != nil {
		return 1, fmt.Errorf("init handshake: %w", err)
	}

	codec := proc.GetCodec()

	// Send cli_invoke as a request (id 2; init used id 1).
	const invokeID = 2
	params := VerbInvokeParams{
		Verb:        binding.Verb,
		Argv:        argv,
		Cwd:         cwd,
		StdinIsTTY:  isCharDevice(os.Stdin),
		StdoutIsTTY: isCharDevice(os.Stdout),
		StderrIsTTY: isCharDevice(os.Stderr),
	}
	if err := codec.WriteRequest(invokeID, "cli_invoke", params); err != nil {
		return 1, fmt.Errorf("send cli_invoke: %w", err)
	}

	// stdin pump: read fir's stdin line-by-line and forward as cli_stdin
	// notifications. EOF closes the channel by sending {eof: true}. Runs
	// in a goroutine so we don't block waiting for stdin if the extension
	// doesn't consume it. The goroutine outlives this call when the
	// extension exits without consuming all stdin; that's fine — fir is
	// about to exit and the OS reaps it.
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		// Allow long lines (e.g. JSON pasted into `fir send`). 1 MiB cap
		// is far above any realistic terminal input.
		buf := make([]byte, 0, 64*1024)
		sc.Buffer(buf, 1024*1024)
		for sc.Scan() {
			_ = codec.WriteNotification("cli_stdin", map[string]any{"data": sc.Text() + "\n"})
		}
		_ = codec.WriteNotification("cli_stdin", map[string]any{"eof": true})
	}()

	// signal pump: forward common signals as cli_signal notifications.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP, syscall.SIGWINCH)
	defer func() {
		signal.Stop(sigCh)
		close(sigCh) // unblocks the goroutine's range loop
	}()
	go func() {
		for s := range sigCh {
			_ = codec.WriteNotification("cli_signal", map[string]string{"name": s.String()})
		}
	}()

	// Read loop: dispatch cli_stdout / cli_stderr notifications and watch
	// for the cli_invoke response.
	for {
		msg, err := codec.ReadMessage()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 1, fmt.Errorf("extension exited before returning a result")
			}
			return 1, fmt.Errorf("read from extension: %w", err)
		}
		switch m := msg.(type) {
		case *Notification:
			switch m.Method {
			case "cli_stdout":
				writeStreamPayload(os.Stdout, m.Params)
			case "cli_stderr":
				writeStreamPayload(os.Stderr, m.Params)
			default:
				// Unknown notifications are ignored — extensions speaking a
				// future protocol revision shouldn't crash this dispatcher.
			}
		case *Response:
			if !idEqualsInt(m.ID, invokeID) {
				continue // late response to some other request
			}
			if m.Error != nil {
				return 1, fmt.Errorf("extension returned error: %s", m.Error.Message)
			}
			var res VerbInvokeResult
			if m.Result != nil {
				_ = json.Unmarshal(*m.Result, &res)
			}
			return res.ExitCode, nil
		case *Request:
			// Verb-mode does not run a Manager — ignore unsolicited bridge
			// calls (notify, exec, etc.) gracefully with method-not-found.
			_ = codec.WriteResponse(m.ID, nil, &Error{
				Code:    -32601,
				Message: fmt.Sprintf("method %q not available in cli verb mode", m.Method),
			})
		}
	}
}

func writeStreamPayload(w io.Writer, raw *json.RawMessage) {
	if raw == nil {
		return
	}
	var p struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(*raw, &p); err != nil {
		return
	}
	_, _ = io.WriteString(w, p.Data)
}

func idEqualsInt(id any, want int) bool {
	switch v := id.(type) {
	case float64:
		return int(v) == want
	case int:
		return v == want
	case int64:
		return int(v) == want
	case json.Number:
		i, err := v.Int64()
		return err == nil && int(i) == want
	}
	return false
}

func isCharDevice(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func userConfigFirDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "fir")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "fir")
}
