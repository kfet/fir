package extension

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ErrTooManyFailures is returned when a process exceeds the max restart attempts.
var ErrTooManyFailures = errors.New("extension: too many consecutive failures")

const maxRestarts = 5

// Process wraps an exec.Cmd and provides a JSON-RPC Codec over its stdio.
type Process struct {
	cfg    ExtProcConfig
	env    []string
	logger *slog.Logger

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	codec    *Codec
	failures int
	backoff  time.Duration
	stopped  bool

	// Fork-template fields. When forkSrv is non-nil the process is a child of
	// the fork template (not a direct exec.Cmd child): cmd is nil, transport is
	// the unix-socket conn, and teardown/reaping is delegated to forkSrv.
	forkSrv *ForkServer
	forkPid int
	conn    io.Closer

	waitErr  error
	waitDone chan struct{}
}

// NewProcess creates a Process for the given config. Call Start() to spawn it.
func NewProcess(cfg ExtProcConfig, env []string, logger *slog.Logger) *Process {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Process{
		cfg:     cfg,
		env:     env,
		logger:  logger,
		backoff: 0,
	}
}

// GetCodec returns the JSON-RPC codec connected to the running process.
func (p *Process) GetCodec() *Codec {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.codec
}

// CloseStdin closes the process's stdin pipe, which also unblocks any
// pending reads on the codec's stdout side when the process exits.
func (p *Process) CloseStdin() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
}

// Start spawns the child process and wires up the Codec and stderr logger.
func (p *Process) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.startLocked()
}

// StartForked spawns the extension as a fork of the warm template managed by
// fs, instead of a fresh exec. The forked child inherits the template's
// imported heap copy-on-write and reconnects its stdio to a per-extension unix
// socket, which becomes this Process's Codec transport. On any error the caller
// should fall back to Start (plain exec).
func (p *Process) StartForked(fs *ForkServer) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	pid, conn, err := fs.Spawn(p.cfg.Name, p.cfg.Path, envSliceToMap(p.env))
	if err != nil {
		return err
	}
	p.forkSrv = fs
	p.forkPid = pid
	p.conn = conn
	p.stdin = conn.(io.WriteCloser)
	p.codec = NewCodec(conn, conn)
	p.cmd = nil
	p.stopped = false
	p.waitDone = make(chan struct{})
	return nil
}

func (p *Process) startLocked() error {
	cmd := exec.Command(p.cfg.Path)
	cmd.Env = append(os.Environ(), p.env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("extension: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("extension: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("extension: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("extension: start %s: %w", p.cfg.Name, err)
	}

	p.cmd = cmd
	p.stdin = stdin
	p.codec = NewCodec(stdout, stdin)
	p.stopped = false
	p.waitDone = make(chan struct{})

	// Forward stderr to logger.
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			p.logger.Info("extension stderr", "ext", p.cfg.Name, "line", scanner.Text())
		}
	}()

	// Wait goroutine.
	go func() {
		p.waitErr = cmd.Wait()
		close(p.waitDone)
	}()

	return nil
}

// Wait blocks until the process exits and returns its error.
func (p *Process) Wait() error {
	p.mu.Lock()
	done := p.waitDone
	p.mu.Unlock()
	if done == nil {
		return errors.New("extension: not started")
	}
	<-done
	return p.waitErr
}

// Pid returns the OS process ID of the running child, or 0 when the process
// has not been started or has already exited.
func (p *Process) Pid() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.forkSrv != nil {
		return p.forkPid
	}
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Stop sends SIGTERM, waits up to 2s, then SIGKILL.
func (p *Process) Stop(ctx context.Context) error {
	p.mu.Lock()
	if p.forkSrv != nil {
		fs := p.forkSrv
		pid := p.forkPid
		conn := p.conn
		done := p.waitDone
		alreadyStopped := p.stopped
		p.stopped = true
		p.mu.Unlock()
		if alreadyStopped {
			return nil
		}
		// Close the socket so the child's read loop sees EOF and exits, then
		// ask the template (its parent) to terminate and reap it.
		if conn != nil {
			_ = conn.Close()
		}
		_ = fs.StopChild(pid)
		if done != nil {
			close(done)
		}
		return nil
	}
	cmd := p.cmd
	done := p.waitDone
	p.stopped = true
	p.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	// SIGTERM
	_ = cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
	}

	// SIGKILL
	_ = cmd.Process.Kill()
	<-done
	return nil
}

// Restart stops the process (if running) and restarts it with exponential backoff.
// Returns ErrTooManyFailures after 5 consecutive failures.
func (p *Process) Restart() error {
	p.mu.Lock()
	if p.failures >= maxRestarts {
		p.mu.Unlock()
		return ErrTooManyFailures
	}
	backoff := p.backoff
	p.mu.Unlock()

	// Stop existing process.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = p.Stop(ctx)

	if backoff > 0 {
		time.Sleep(backoff)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.startLocked(); err != nil {
		p.failures++
		if p.backoff == 0 {
			p.backoff = 1 * time.Second
		} else if p.backoff < 30*time.Second {
			p.backoff *= 2
			if p.backoff > 30*time.Second {
				p.backoff = 30 * time.Second
			}
		}
		return err
	}

	// Successful start still counts as an attempt until reset externally;
	// but per the spec, consecutive *failures* are what count.
	// A successful start resets the counter.
	p.failures = 0
	p.backoff = 0
	return nil
}
