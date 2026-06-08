// Verbatim "Facts" extraction for compaction summaries.
// Phase 2 #12: surface command lines, errors, and exit codes byte-for-byte
// so technical detail isn't paraphrased away during summarization.
package compaction

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/kfet/agent"
)

// Facts holds the verbatim signals extracted from a conversation slice.
type Facts struct {
	// Commands lists `bash` tool-call command strings, in order, deduped.
	Commands []string
	// Errors lists lines that match common error/exit-code shapes from
	// tool results. Verbatim, in order, deduped.
	Errors []string
}

// extractFacts walks an entry slice and pulls verbatim command lines and
// error/exit-code lines from assistant tool calls and tool results.
//
// `limit` bounds each list — older items are dropped first.
func extractFacts(messages []agent.AgentMessage, limit int) Facts {
	if limit <= 0 {
		limit = 20
	}
	var f Facts
	seenCmd := make(map[string]struct{})
	seenErr := make(map[string]struct{})

	for _, m := range messages {
		switch m.Role() {
		case "assistant":
			a := m.Message.AsAssistant()
			if a == nil {
				continue
			}
			for _, b := range a.Content {
				if b.ToolCall == nil || b.ToolCall.Name != "bash" {
					continue
				}
				cmd, _ := b.ToolCall.Arguments["command"].(string)
				cmd = strings.TrimSpace(cmd)
				if cmd == "" {
					continue
				}
				if _, ok := seenCmd[cmd]; ok {
					continue
				}
				seenCmd[cmd] = struct{}{}
				f.Commands = append(f.Commands, cmd)
			}
		case "toolResult":
			tr := m.Message.AsToolResult()
			if tr == nil {
				continue
			}
			for _, c := range tr.Content {
				if !c.IsText() {
					continue
				}
				for _, line := range factErrorLines(c.Text) {
					if _, ok := seenErr[line]; ok {
						continue
					}
					seenErr[line] = struct{}{}
					f.Errors = append(f.Errors, line)
				}
			}
		}
	}

	if len(f.Commands) > limit {
		f.Commands = f.Commands[len(f.Commands)-limit:]
	}
	if len(f.Errors) > limit {
		f.Errors = f.Errors[len(f.Errors)-limit:]
	}
	return f
}

// factErrorRe matches lines that look like errors, exit-code traces, or
// build/test failures. Conservative — false negatives are fine; false
// positives bloat the summary.
var factErrorRe = regexp.MustCompile(
	`(?i)\b(error|fatal|panic|fail(ed|ure)?|undefined|cannot|exit (code|status))\b|\b[Ee]xit\s*[: ]\s*\d+\b|^[a-zA-Z0-9_./]+:\d+:\d+:\s|: \*\*\* `,
)

func factErrorLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" || len(line) > 240 {
			continue
		}
		if factErrorRe.MatchString(line) {
			out = append(out, line)
		}
	}
	return out
}

// FormatFacts renders a verbatim "Facts" block to be appended to a
// summary. Returns "" if there is nothing to report.
func FormatFacts(f Facts) string {
	if len(f.Commands) == 0 && len(f.Errors) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Facts (verbatim)\n")
	if len(f.Commands) > 0 {
		b.WriteString("\n### Commands\n```\n")
		for _, c := range f.Commands {
			fmt.Fprintln(&b, c)
		}
		b.WriteString("```\n")
	}
	if len(f.Errors) > 0 {
		b.WriteString("\n### Errors / exit codes\n```\n")
		for _, e := range f.Errors {
			fmt.Fprintln(&b, e)
		}
		b.WriteString("```\n")
	}
	return b.String()
}
