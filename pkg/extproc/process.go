package extproc

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
	"time"
)

// ErrTooManyFailures is returned when a process exceeds the max restart attempts.
var ErrTooManyFailures = errors.New("extproc: too many consecutive failures")

const maxRestarts = 5

// Process wraps an exec.Cmd and provides a JSON-RPC Codec over its stdio.
type Process struct {
	cfg    ExtProcConfig
	env    []string
	logger *slog.Logger

	mu       sync.Mutex
	cmd      *exec.Cmd
	codec    *Codec
	failures int
	backoff  time.Duration
	stopped  bool

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
		backoff: 1 * time.Second,
	}
}

// GetCodec returns the JSON-RPC codec connected to the running process.
func (p *Process) GetCodec() *Codec {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.codec
}

// Start spawns the child process and wires up the Codec and stderr logger.
func (p *Process) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.startLocked()
}

func (p *Process) startLocked() error {
	cmd := exec.Command(p.cfg.Path)
	cmd.Env = append(os.Environ(), p.env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("extproc: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("extproc: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("extproc: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("extproc: start %s: %w", p.cfg.Name, err)
	}

	p.cmd = cmd
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
		return errors.New("extproc: not started")
	}
	<-done
	return p.waitErr
}

// Stop sends SIGTERM, waits up to 2s, then SIGKILL.
func (p *Process) Stop(ctx context.Context) error {
	p.mu.Lock()
	cmd := p.cmd
	done := p.waitDone
	p.stopped = true
	p.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	// SIGTERM
	_ = cmd.Process.Signal(os.Interrupt)

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

	time.Sleep(backoff)

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.startLocked(); err != nil {
		p.failures++
		if p.backoff < 30*time.Second {
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
	p.backoff = 1 * time.Second
	return nil
}
