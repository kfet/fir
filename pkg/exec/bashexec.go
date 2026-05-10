// Ported from: packages/coding-agent/src/core/bash-executor.ts
// Upstream hash: 1caadb2e
package exec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kfet/fir/pkg/agent/tools"
)

// BashResult holds the result of a bash command execution.
type BashResult struct {
	// Output is the combined stdout+stderr output (sanitized, possibly truncated).
	Output string
	// ExitCode is the process exit code. -1 if killed/cancelled.
	ExitCode int
	// Cancelled indicates the command was cancelled via context.
	Cancelled bool
	// Truncated indicates the output was truncated.
	Truncated bool
	// FullOutputPath is the path to temp file with full output (if output exceeded threshold).
	FullOutputPath string
}

// sanitizeBinaryOutput replaces non-printable characters (except common whitespace
// and ESC which is used in ANSI escape sequences).
func sanitizeBinaryOutput(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' || r == '\r' || r == '\x1b' || (r >= 32 && r < 127) || r >= 128 {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
	}
	return b.String()
}

// ExecuteBash executes a bash command with optional streaming and cancellation.
//
// When onChunk is non-nil, environment variables (CLICOLOR_FORCE, FORCE_COLOR)
// are injected so tools that check for TTY color support still emit ANSI
// codes even though stdout is a pipe. The raw output (with colors) is
// streamed via onChunk for display, while BashResult.Output is ANSI-stripped
// for LLM context.
//
// Features:
//   - Streams raw output (ANSI preserved) via onChunk callback for display
//   - Injects color-forcing env vars when streaming for display
//   - Writes large output to temp file
//   - Supports cancellation via context — kills entire process group
//   - Sanitizes stored output (strips ANSI, removes binary, normalizes newlines)
//   - Truncates output if exceeds default max bytes
func ExecuteBash(ctx context.Context, command string, onChunk func(chunk string)) (BashResult, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.CommandContext(ctx, shell, "-c", command)
	cmd.Env = os.Environ()

	// Run in its own process group so cancellation kills all children.
	setProcGroup(cmd)

	// When streaming for display, inject env vars that tell CLI tools to
	// emit ANSI colors even when stdout is not a TTY.
	if onChunk != nil {
		cmd.Env = tools.AppendColorEnv(cmd.Env)
	}

	// Create pipes for stdout and stderr
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return BashResult{}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return BashResult{}, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return BashResult{}, fmt.Errorf("start command: %w", err)
	}

	maxOutputBytes := tools.DefaultMaxBytes * 2

	var mu sync.Mutex
	var outputChunks []string
	var outputBytes int
	var totalBytes int
	var tempFilePath string
	var tempFile *os.File

	handleData := func(data []byte) {
		mu.Lock()
		defer mu.Unlock()

		totalBytes += len(data)

		// Sanitize binary but preserve ANSI for display
		rawText := sanitizeBinaryOutput(string(data))
		rawText = strings.ReplaceAll(rawText, "\r", "")

		// Stream raw (ANSI-preserved) output to display callback
		if onChunk != nil {
			onChunk(rawText)
		}

		// Strip ANSI for LLM context storage
		text := tools.StripAnsi(rawText)

		// Start temp file if exceeds threshold
		if totalBytes > tools.DefaultMaxBytes && tempFile == nil {
			id := make([]byte, 8)
			rand.Read(id)
			tempFilePath = filepath.Join(os.TempDir(), "fir-bash-"+hex.EncodeToString(id)+".log")
			f, err := os.Create(tempFilePath)
			if err == nil {
				tempFile = f
				// Write buffered chunks to temp file
				for _, chunk := range outputChunks {
					tempFile.WriteString(chunk)
				}
			}
		}

		if tempFile != nil {
			tempFile.WriteString(text)
		}

		// Keep rolling buffer
		outputChunks = append(outputChunks, text)
		outputBytes += len(text)
		for outputBytes > maxOutputBytes && len(outputChunks) > 1 {
			removed := outputChunks[0]
			outputChunks = outputChunks[1:]
			outputBytes -= len(removed)
		}
	}

	// Read stdout and stderr concurrently
	var wg sync.WaitGroup
	wg.Add(2)

	readStream := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				handleData(buf[:n])
			}
			if readErr != nil {
				break
			}
		}
	}

	go readStream(stdoutPipe)
	go readStream(stderrPipe)

	// Wait for all output to be read
	wg.Wait()

	// Wait for process to finish
	waitErr := cmd.Wait()

	if tempFile != nil {
		tempFile.Close()
	}

	// Build result
	mu.Lock()
	fullOutput := strings.Join(outputChunks, "")
	mu.Unlock()

	truncationResult := tools.TruncateTail(fullOutput, tools.TruncationOptions{})
	cancelled := ctx.Err() != nil

	exitCode := -1
	if !cancelled && waitErr == nil {
		exitCode = 0
	} else if !cancelled && waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	output := fullOutput
	if truncationResult.Truncated {
		output = truncationResult.Content
	}

	return BashResult{
		Output:         output,
		ExitCode:       exitCode,
		Cancelled:      cancelled,
		Truncated:      truncationResult.Truncated,
		FullOutputPath: tempFilePath,
	}, nil
}
