package extension

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// ForkServer manages a single warm "template" python3 process that imports the
// fir_ext SDK once and forks a child per extension on demand. Forked children
// inherit the template's interpreter heap copy-on-write, so the per-sidecar
// private memory and import-time startup latency both collapse toward a single
// shared baseline. See pkg/extension/sdk/python/forkserver.py for the template
// and /tmp/acp-fork-design.md for the architecture.
//
// Only builtin *.py extensions are forked; everything else keeps the plain
// exec.Command stdio path (see Process.Start). The template speaks a tiny
// newline-JSON control protocol over its own stdin/stdout; each forked child
// gets its own JSON-RPC channel back to Go over a per-extension unix socket.
type ForkServer struct {
	logger *slog.Logger

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	sockDir string

	mu      sync.Mutex // serialises control writes + reply reads
	nextID  int
	sockSeq int
	closed  bool
}

// forkAcceptTimeout bounds how long Spawn waits for the forked child to connect
// back on the unix socket before treating the spawn as failed (caller falls
// back to exec). A package var so tests can shrink it.
var forkAcceptTimeout = 10 * time.Second

// StartForkServer launches the template python3 process. env should include the
// SDK PYTHONPATH (so the template can import fir_ext); it is appended to the
// host environment. python3Path is the interpreter; pass "" to resolve
// "python3" from PATH. sdkDir is the extracted SDK root (contains
// python/forkserver.py).
func StartForkServer(sdkDir string, env []string, logger *slog.Logger) (*ForkServer, error) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	script := filepath.Join(sdkDir, "python", "forkserver.py")
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("forkserver: template script missing: %w", err)
	}

	python, err := exec.LookPath("python3")
	if err != nil {
		return nil, fmt.Errorf("forkserver: python3 not found: %w", err)
	}

	sockDir, err := os.MkdirTemp("/tmp", "firfork")
	if err != nil {
		// Fall back to the default temp dir; sun_path length permitting.
		sockDir, err = os.MkdirTemp("", "firfork")
		if err != nil {
			return nil, fmt.Errorf("forkserver: sock dir: %w", err)
		}
	}

	cmd := exec.Command(python, script)
	cmd.Env = append(os.Environ(), env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(sockDir)
		return nil, fmt.Errorf("forkserver: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(sockDir)
		return nil, fmt.Errorf("forkserver: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		os.RemoveAll(sockDir)
		return nil, fmt.Errorf("forkserver: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		os.RemoveAll(sockDir)
		return nil, fmt.Errorf("forkserver: start: %w", err)
	}

	fs := &ForkServer{
		logger:  logger,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		sockDir: sockDir,
	}

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			fs.logger.Info("forkserver stderr", "line", scanner.Text())
		}
	}()

	// Wait for the ready line so we know fir_ext imported cleanly before any
	// spawn is attempted.
	if err := fs.awaitReady(); err != nil {
		_ = fs.Close()
		return nil, err
	}
	return fs, nil
}

// awaitReady reads the initial {"ready":true} line from the template.
func (fs *ForkServer) awaitReady() error {
	type readyMsg struct {
		Ready bool   `json:"ready"`
		Error string `json:"error"`
	}
	ch := make(chan error, 1)
	go func() {
		line, err := fs.stdout.ReadBytes('\n')
		if err != nil {
			ch <- fmt.Errorf("forkserver: read ready: %w", err)
			return
		}
		var m readyMsg
		if err := json.Unmarshal(line, &m); err != nil || !m.Ready {
			ch <- fmt.Errorf("forkserver: bad ready line: %s", line)
			return
		}
		ch <- nil
	}()
	select {
	case err := <-ch:
		return err
	case <-time.After(15 * time.Second):
		return fmt.Errorf("forkserver: ready timeout")
	}
}

// Pid returns the template process pid, or 0 if not running.
func (fs *ForkServer) Pid() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.cmd == nil || fs.cmd.Process == nil {
		return 0
	}
	return fs.cmd.Process.Pid
}

// control sends one request and reads one reply under the control mutex.
func (fs *ForkServer) control(req map[string]any) (map[string]any, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.closed {
		return nil, fmt.Errorf("forkserver: closed")
	}
	fs.nextID++
	req["id"] = fs.nextID
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := fs.stdin.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("forkserver: write control: %w", err)
	}
	line, err := fs.stdout.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("forkserver: read reply: %w", err)
	}
	var reply map[string]any
	if err := json.Unmarshal(line, &reply); err != nil {
		return nil, fmt.Errorf("forkserver: bad reply: %w", err)
	}
	return reply, nil
}

// Spawn forks a child running the extension at path, wiring it to a fresh unix
// socket. It returns the child pid and the accepted connection (which the
// caller wraps in a Codec). env is applied in the child via os.environ.update.
func (fs *ForkServer) Spawn(name, path string, env map[string]string) (int, net.Conn, error) {
	fs.mu.Lock()
	if fs.closed {
		fs.mu.Unlock()
		return 0, nil, fmt.Errorf("forkserver: closed")
	}
	fs.sockSeq++
	seq := fs.sockSeq
	sockDir := fs.sockDir
	fs.mu.Unlock()

	sockPath := filepath.Join(sockDir, strconv.Itoa(seq)+".sock")
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return 0, nil, fmt.Errorf("forkserver: listen %s: %w", sockPath, err)
	}
	defer ln.Close()
	defer os.Remove(sockPath)

	reply, err := fs.control(map[string]any{
		"cmd":  "spawn",
		"path": path,
		"sock": sockPath,
		"env":  env,
	})
	if err != nil {
		return 0, nil, err
	}
	if errStr, ok := reply["error"].(string); ok && errStr != "" {
		return 0, nil, fmt.Errorf("forkserver: spawn %s: %s", name, errStr)
	}
	pidF, ok := reply["pid"].(float64)
	if !ok {
		return 0, nil, fmt.Errorf("forkserver: spawn %s: no pid in reply", name)
	}
	pid := int(pidF)

	if ul, ok := ln.(*net.UnixListener); ok {
		_ = ul.SetDeadline(time.Now().Add(forkAcceptTimeout))
	}
	conn, err := ln.Accept()
	if err != nil {
		// Child never connected — reap it and report failure so the caller
		// can fall back to a plain exec spawn.
		_, _ = fs.control(map[string]any{"cmd": "stop", "pid": pid})
		return 0, nil, fmt.Errorf("forkserver: accept %s: %w", name, err)
	}
	return pid, conn, nil
}

// StopChild asks the template to terminate and reap a forked child (it is the
// child's parent; Go cannot waitpid a non-child). Best-effort.
func (fs *ForkServer) StopChild(pid int) error {
	if pid <= 0 {
		return nil
	}
	_, err := fs.control(map[string]any{"cmd": "stop", "pid": pid})
	return err
}

// Close shuts the template down and removes the socket directory.
func (fs *ForkServer) Close() error {
	fs.mu.Lock()
	if fs.closed {
		fs.mu.Unlock()
		return nil
	}
	fs.closed = true
	stdin := fs.stdin
	cmd := fs.cmd
	sockDir := fs.sockDir
	fs.mu.Unlock()

	// Ask politely, then close stdin (EOF ends the control loop).
	if stdin != nil {
		_, _ = stdin.Write([]byte(`{"cmd":"shutdown"}` + "\n"))
		_ = stdin.Close()
	}

	if cmd != nil && cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Signal(syscall.SIGKILL)
			<-done
		}
	}
	if sockDir != "" {
		_ = os.RemoveAll(sockDir)
	}
	return nil
}

// forkEligible reports whether an extension should be spawned via the fork
// template rather than a plain exec. Only builtin Python scripts qualify: they
// are trusted (no arbitrary shebang/venv) and import the SDK we already warmed.
func forkEligible(cfg ExtProcConfig) bool {
	return cfg.Scope == "builtin" && filepath.Ext(cfg.Path) == ".py"
}

// envSliceToMap converts "K=V" env entries into a map for the fork control
// protocol. Entries without '=' are skipped.
func envSliceToMap(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	m := make(map[string]string, len(env))
	for _, e := range env {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				m[e[:i]] = e[i+1:]
				break
			}
		}
	}
	return m
}

// forkServerDisabled reports whether the fork template is explicitly disabled
// via FIR_NO_FORKSERVER (a safety valve / debugging escape hatch).
func forkServerDisabled() bool {
	v := os.Getenv("FIR_NO_FORKSERVER")
	return v != "" && v != "0" && v != "false"
}
