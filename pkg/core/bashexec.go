// Ported from: packages/coding-agent/src/core/bash-executor.ts
// Upstream hash: 1caadb2e
package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/kfet/tau/pkg/core/tools"
)

// BashExecutorOptions configures bash execution.
type BashExecutorOptions struct {
	// OnChunk is called with sanitized output chunks during execution.
	OnChunk func(chunk string)
}

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

// ansiRegexp matches ANSI escape sequences.
var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]|\x1b\][^\x07]*\x07|\x1b[^[(\x1b]`)

// stripAnsi removes ANSI escape codes from text.
func stripAnsi(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

// sanitizeBinaryOutput replaces non-printable characters (except common whitespace).
func sanitizeBinaryOutput(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' || r == '\r' || (r >= 32 && r < 127) || r >= 128 {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
	}
	return b.String()
}

// ExecuteBash executes a bash command with optional streaming and cancellation.
//
// Features:
//   - Streams sanitized output via OnChunk callback
//   - Writes large output to temp file
//   - Supports cancellation via context
//   - Sanitizes output (strips ANSI, removes binary, normalizes newlines)
//   - Truncates output if exceeds default max bytes
func ExecuteBash(ctx context.Context, command string, opts *BashExecutorOptions) (BashResult, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.CommandContext(ctx, shell, "-c", command)
	cmd.Env = os.Environ()

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

		// Sanitize: strip ANSI, replace binary, normalize newlines
		text := sanitizeBinaryOutput(stripAnsi(string(data)))
		text = strings.ReplaceAll(text, "\r", "")

		// Start temp file if exceeds threshold
		if totalBytes > tools.DefaultMaxBytes && tempFile == nil {
			id := make([]byte, 8)
			rand.Read(id)
			tempFilePath = filepath.Join(os.TempDir(), "tau-bash-"+hex.EncodeToString(id)+".log")
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

		// Stream to callback
		if opts != nil && opts.OnChunk != nil {
			opts.OnChunk(text)
		}
	}

	// Read stdout and stderr concurrently
	var wg sync.WaitGroup
	wg.Add(2)

	readStream := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				handleData(buf[:n])
			}
			if err != nil {
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

// ExecuteBashSimple runs a bash command and returns the output as a string.
// This is a convenience wrapper for simple use cases.
func ExecuteBashSimple(ctx context.Context, command string) (string, int, error) {
	result, err := ExecuteBash(ctx, command, nil)
	if err != nil {
		return "", -1, err
	}
	return result.Output, result.ExitCode, nil
}

// ExecuteBashCapture runs a bash command and captures output.
// Returns the combined stdout+stderr.
func ExecuteBashCapture(command string) (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell, "-c", command)
	cmd.Env = os.Environ()

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	err := cmd.Run()
	return combined.String(), err
}
