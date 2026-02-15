// Ported from: packages/coding-agent/src/modes/interactive/components/bash-execution.ts
// Upstream hash: 1caadb2e
package components

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/kfet/tau/pkg/core/tools"
	"github.com/kfet/tau/pkg/modes/interactive/theme"
	"github.com/kfet/tau/pkg/tui"
	tuicomp "github.com/kfet/tau/pkg/tui/components"
)

// PreviewLines is the line limit when not expanded (matches tool execution behavior).
const PreviewLines = 20

// bashAnsiRe strips ANSI escape codes.
var bashAnsiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]`)

func bashStripAnsi(s string) string {
	return bashAnsiRe.ReplaceAllString(s, "")
}

// BashStatus represents the execution status of a bash command.
type BashStatus string

const (
	BashRunning   BashStatus = "running"
	BashComplete  BashStatus = "complete"
	BashCancelled BashStatus = "cancelled"
	BashError     BashStatus = "error"
)

// BashExecutionComponent displays bash command execution with streaming output.
type BashExecutionComponent struct {
	tui.Container
	mu               sync.Mutex // protects all mutable fields below
	command          string
	outputLines      []string
	status           BashStatus
	exitCode         *int
	loader           *tuicomp.Loader
	truncationResult *tools.TruncationResult
	fullOutputPath   string
	expanded         bool
	contentContainer *tui.Container
	ui               *tui.TUI
	borderColorKey   string
}

var _ tui.Component = (*BashExecutionComponent)(nil)

// NewBashExecutionComponent creates a new BashExecutionComponent.
func NewBashExecutionComponent(command string, ui *tui.TUI, excludeFromContext bool) *BashExecutionComponent {
	colorKey := "bashMode"
	if excludeFromContext {
		colorKey = "dim"
	}

	t := theme.GetTheme()
	borderColor := func(s string) string { return t.Fg(colorKey, s) }

	bc := &BashExecutionComponent{
		command:        command,
		status:         BashRunning,
		ui:             ui,
		borderColorKey: colorKey,
		contentContainer: &tui.Container{},
	}

	// Spacer
	bc.AddChild(tuicomp.NewSpacer(1))

	// Top border
	bc.AddChild(NewDynamicBorder(borderColor))

	// Content container
	bc.AddChild(bc.contentContainer)

	// Command header
	header := tuicomp.NewText(t.Fg(colorKey, t.Bold("$ "+command)), 1, 0, nil)
	bc.contentContainer.AddChild(header)

	// Loader — wrap TUI as RenderRequester (TUI.RequestRender takes bool, interface doesn't)
	var rr tuicomp.RenderRequester
	if ui != nil {
		rr = &tuiRenderRequester{ui: ui}
	}
	bc.loader = tuicomp.NewLoader(
		rr,
		func(s string) string { return t.Fg(colorKey, s) },
		func(s string) string { return t.Fg("muted", s) },
		fmt.Sprintf("Running... (%s to cancel)", EditorKey("selectCancel")),
	)
	bc.contentContainer.AddChild(bc.loader)

	// Bottom border
	bc.AddChild(NewDynamicBorder(borderColor))

	return bc
}

// SetExpanded sets whether the output is expanded or collapsed.
func (bc *BashExecutionComponent) SetExpanded(expanded bool) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.expanded = expanded
	bc.updateDisplay()
}

// Invalidate invalidates and rebuilds display.
func (bc *BashExecutionComponent) Invalidate() {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.Container.Invalidate()
	bc.updateDisplay()
}

// AppendOutput adds streaming output to the display.
// Safe to call from any goroutine.
func (bc *BashExecutionComponent) AppendOutput(chunk string) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	// Strip ANSI codes and normalize line endings
	clean := bashStripAnsi(chunk)
	clean = strings.ReplaceAll(clean, "\r\n", "\n")
	clean = strings.ReplaceAll(clean, "\r", "\n")

	newLines := strings.Split(clean, "\n")
	if len(bc.outputLines) > 0 && len(newLines) > 0 {
		// Append first chunk to last line (incomplete line continuation)
		bc.outputLines[len(bc.outputLines)-1] += newLines[0]
		bc.outputLines = append(bc.outputLines, newLines[1:]...)
	} else {
		bc.outputLines = append(bc.outputLines, newLines...)
	}

	bc.updateDisplay()
}

// SetComplete marks the command as finished.
// Safe to call from any goroutine.
func (bc *BashExecutionComponent) SetComplete(exitCode *int, cancelled bool, truncResult *tools.TruncationResult, fullOutputPath string) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	bc.exitCode = exitCode
	if cancelled {
		bc.status = BashCancelled
	} else if exitCode != nil && *exitCode != 0 {
		bc.status = BashError
	} else {
		bc.status = BashComplete
	}
	bc.truncationResult = truncResult
	bc.fullOutputPath = fullOutputPath

	bc.loader.Stop()
	bc.updateDisplay()
}

func (bc *BashExecutionComponent) updateDisplay() {
	t := theme.GetTheme()

	// Apply truncation for LLM context limits
	fullOutput := strings.Join(bc.outputLines, "\n")
	contextTruncation := tools.TruncateTail(fullOutput, tools.TruncationOptions{
		MaxLines: tools.DefaultMaxLines,
		MaxBytes: tools.DefaultMaxBytes,
	})

	// Get lines to potentially display
	var availableLines []string
	if contextTruncation.Content != "" {
		availableLines = strings.Split(contextTruncation.Content, "\n")
	}

	// Apply preview truncation based on expanded state
	previewStart := 0
	if len(availableLines) > PreviewLines {
		previewStart = len(availableLines) - PreviewLines
	}
	previewLogicalLines := availableLines[previewStart:]
	hiddenLineCount := previewStart

	// Rebuild content container
	bc.contentContainer.Clear()

	// Command header
	header := tuicomp.NewText(t.Fg(bc.borderColorKey, t.Bold("$ "+bc.command)), 1, 0, nil)
	bc.contentContainer.AddChild(header)

	// Output
	if len(availableLines) > 0 {
		if bc.expanded {
			styledLines := make([]string, len(availableLines))
			for i, line := range availableLines {
				styledLines[i] = t.Fg("muted", line)
			}
			displayText := strings.Join(styledLines, "\n")
			bc.contentContainer.AddChild(tuicomp.NewText("\n"+displayText, 1, 0, nil))
		} else {
			styledLines := make([]string, len(previewLogicalLines))
			for i, line := range previewLogicalLines {
				styledLines[i] = t.Fg("muted", line)
			}
			styledOutput := strings.Join(styledLines, "\n")

			width := 80
			if bc.ui != nil {
				width = bc.ui.Terminal.Columns()
			}

			result := TruncateToVisualLines("\n"+styledOutput, PreviewLines, width, 1)
			bc.contentContainer.AddChild(&staticComponent{lines: result.VisualLines})
		}
	}

	// Loader or status
	if bc.status == BashRunning {
		bc.contentContainer.AddChild(bc.loader)
	} else {
		var statusParts []string

		if hiddenLineCount > 0 {
			if bc.expanded {
				statusParts = append(statusParts, "("+KeyHint("expandTools", "to collapse")+")")
			} else {
				statusParts = append(statusParts,
					t.Fg("muted", fmt.Sprintf("... %d more lines", hiddenLineCount))+
						" ("+KeyHint("expandTools", "to expand")+")")
			}
		}

		if bc.status == BashCancelled {
			statusParts = append(statusParts, t.Fg("warning", "(cancelled)"))
		} else if bc.status == BashError && bc.exitCode != nil {
			statusParts = append(statusParts, t.Fg("error", fmt.Sprintf("(exit %d)", *bc.exitCode)))
		}

		wasTruncated := contextTruncation.Truncated
		if bc.truncationResult != nil && bc.truncationResult.Truncated {
			wasTruncated = true
		}
		if wasTruncated && bc.fullOutputPath != "" {
			statusParts = append(statusParts, t.Fg("warning", "Output truncated. Full output: "+bc.fullOutputPath))
		}

		if len(statusParts) > 0 {
			bc.contentContainer.AddChild(tuicomp.NewText("\n"+strings.Join(statusParts, "\n"), 1, 0, nil))
		}
	}
}

// GetOutput returns the raw output text.
func (bc *BashExecutionComponent) GetOutput() string {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	return strings.Join(bc.outputLines, "\n")
}

// GetCommand returns the command that was executed.
func (bc *BashExecutionComponent) GetCommand() string {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	return bc.command
}

// Render renders the component. Safe to call from the TUI thread.
func (bc *BashExecutionComponent) Render(width int) []string {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	return bc.Container.Render(width)
}

// tuiRenderRequester adapts *tui.TUI to RenderRequester interface.
type tuiRenderRequester struct {
	ui *tui.TUI
}

func (r *tuiRenderRequester) RequestRender() {
	r.ui.RequestRender(false)
}

// staticComponent renders fixed lines.
type staticComponent struct {
	lines []string
}

func (s *staticComponent) Render(width int) []string { return s.lines }
func (s *staticComponent) Invalidate()               {}
