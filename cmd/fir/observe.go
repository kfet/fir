package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// runObserve implements the "fir observe" subcommand.
//
// Usage:
//
//	fir observe                    list live sessions
//	fir observe <id-prefix>        tail-and-format the session transcript
//	fir observe <id> --json        raw JSONL output (no formatting)
//	fir observe --cwd <path>       resolve by cwd (ambiguous if 0/many)
//	fir observe --cwd .            session in $PWD
//
// The mechanism is described in docs/design/observe.md: each fir session
// runs a per-session `observe.py` extension that writes a sidecar to
// $XDG_STATE_HOME/fir/agents/<session-id>.json with the on-disk transcript
// path. We read the sidecar, then `tail -n +1 -F` the transcript.
func runObserve() error {
	args := os.Args[2:] // skip "fir observe"

	// Parse flags + positional arg.
	var (
		idPrefix string
		cwdFlag  string
		jsonOut  bool
		interact bool
		fullText bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			jsonOut = true
		case a == "--full":
			fullText = true
		case a == "--interact":
			interact = true
		case a == "--cwd":
			if i+1 >= len(args) {
				return errors.New("--cwd requires an argument (path or '.')")
			}
			cwdFlag = args[i+1]
			i++
		case strings.HasPrefix(a, "--cwd="):
			cwdFlag = strings.TrimPrefix(a, "--cwd=")
		case a == "--help" || a == "-h":
			fmt.Fprint(os.Stderr, observeUsage)
			return nil
		case strings.HasPrefix(a, "--"):
			return fmt.Errorf("unknown flag: %s\n%s", a, observeUsage)
		default:
			if idPrefix != "" {
				return fmt.Errorf("unexpected extra argument: %s\n%s", a, observeUsage)
			}
			idPrefix = a
		}
	}

	// Mode selection.
	if idPrefix == "" && cwdFlag == "" {
		return runObserveList()
	}
	return runObserveTail(idPrefix, cwdFlag, jsonOut, interact, fullText)
}

const observeUsage = `usage: fir observe [<id-prefix>] [--cwd <path>] [--json] [--full] [--interact]

  fir observe                  list live sessions across all running fir processes
  fir observe <id-prefix>      tail-and-format the matching session's transcript
  fir observe --cwd <path>     resolve session by working directory
  fir observe --cwd .          session in current directory (error if 0/many)
  fir observe <id> --json      raw JSONL transcript — no formatting, no truncation (best for agents)
  fir observe <id> --full      formatted transcript with no truncation (agent-readable prose)
  fir observe <id> --interact  also pipe stdin to session as input (Enter to send)
`

// ---------------------------------------------------------------------------
// Sidecar — discovery
// ---------------------------------------------------------------------------

// sidecar is the JSON written by observe.py at $XDG_STATE_HOME/fir/agents/.
type sidecar struct {
	Schema      int    `json:"schema"`
	SessionID   string `json:"session_id"`
	PID         int    `json:"pid"`
	SocketPath  string `json:"socket_path"`
	StorePath   string `json:"store_path"`
	Cwd         string `json:"cwd"`
	StartedAt   string `json:"started_at"`
	Status      string `json:"status"`
	SessionName string `json:"session_name"`

	// Computed (not on disk).
	sidecarPath string // source path on disk
}

// loadSidecar reads and parses a single sidecar file.
func loadSidecar(path string) (*sidecar, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s sidecar
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	s.sidecarPath = path
	return &s, nil
}

// stateDir returns $XDG_STATE_HOME/fir/agents/ (default ~/.local/state/fir/agents/).
func stateDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "fir", "agents")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "fir", "agents")
}

// readSidecars reads all sidecars from the state dir. Stale (process dead)
// running entries are reclassified as `crashed`. Returns sidecars sorted by
// started_at descending (newest first).
func readSidecars() ([]sidecar, error) {
	dir := stateDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read state dir %s: %w", dir, err)
	}

	var out []sidecar
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		s, err := loadSidecar(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // skip unreadable / malformed entries
		}
		// Liveness: if status claims running/idle but the pid is dead, mark as crashed.
		if s.Status == "running" || s.Status == "idle" {
			if !pidAlive(s.PID) {
				s.Status = "crashed"
			}
		}
		out = append(out, *s)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt > out[j].StartedAt
	})
	return out, nil
}

// pidAlive returns true if a process with the given PID exists and is owned by us.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 == liveness probe on Unix.
	return p.Signal(syscall.Signal(0)) == nil
}

// ---------------------------------------------------------------------------
// Resolver
// ---------------------------------------------------------------------------

// resolveSidecar finds the sidecar matching idPrefix or cwd. Prefix-match runs
// against (session_id, session_name, basename(cwd)). Returns an error if zero
// or multiple sessions match (Git-style).
func resolveSidecar(idPrefix, cwdFlag string) (*sidecar, error) {
	all, err := readSidecars()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errors.New("no fir sessions found (no sidecars in " + stateDir() + ")")
	}

	if cwdFlag != "" {
		want := cwdFlag
		if want == "." {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("get cwd: %w", err)
			}
			want = cwd
		}
		want, _ = filepath.Abs(want)
		var matches []sidecar
		for _, s := range all {
			if s.Cwd == want {
				matches = append(matches, s)
			}
		}
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("no session in cwd %s", want)
		case 1:
			return &matches[0], nil
		default:
			return nil, ambiguityError(matches)
		}
	}

	// Prefix-match across (session_id, session_name, basename(cwd)).
	var matches []sidecar
	for _, s := range all {
		if strings.HasPrefix(s.SessionID, idPrefix) ||
			(s.SessionName != "" && strings.HasPrefix(s.SessionName, idPrefix)) ||
			strings.HasPrefix(filepath.Base(s.Cwd), idPrefix) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no session matching %q", idPrefix)
	case 1:
		return &matches[0], nil
	default:
		return nil, ambiguityError(matches)
	}
}

func ambiguityError(matches []sidecar) error {
	var b strings.Builder
	fmt.Fprintln(&b, "ambiguous match — candidates:")
	for _, s := range matches {
		fmt.Fprintf(&b, "  %s  %s  cwd=%s\n",
			s.SessionID[:min(8, len(s.SessionID))], s.SessionName, s.Cwd)
	}
	return errors.New(strings.TrimRight(b.String(), "\n"))
}

// ---------------------------------------------------------------------------
// `fir observe` (no args) — list
// ---------------------------------------------------------------------------

func runObserveList() error {
	all, err := readSidecars()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Fprintln(os.Stderr, "no fir sessions found")
		return nil
	}

	// Compute widths.
	idW, nameW, cwdW := 8, 4, 3
	for _, s := range all {
		idLen := min(8, len(s.SessionID))
		if idLen > idW {
			idW = idLen
		}
		if len(s.SessionName) > nameW {
			nameW = len(s.SessionName)
		}
		base := filepath.Base(s.Cwd)
		if len(base) > cwdW {
			cwdW = len(base)
		}
	}
	if nameW > 30 {
		nameW = 30
	}
	if cwdW > 30 {
		cwdW = 30
	}

	fmt.Printf("%-*s  %-*s  %-*s  %-9s  %s\n",
		idW, "ID", nameW, "NAME", cwdW, "CWD", "STATUS", "AGE")
	now := time.Now()
	for _, s := range all {
		id := s.SessionID
		if len(id) > 8 {
			id = id[:8]
		}
		name := s.SessionName
		if name == "" {
			name = "-"
		}
		name = truncRunes(name, nameW)
		cwd := truncRunes(filepath.Base(s.Cwd), cwdW)
		fmt.Printf("%-*s  %-*s  %-*s  %-9s  %s\n",
			idW, id,
			nameW, name,
			cwdW, cwd,
			s.Status,
			ageString(s.StartedAt, now),
		)
	}
	return nil
}

func ageString(startedAt string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return "?"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ---------------------------------------------------------------------------
// `fir observe <id>` — tail the transcript
// ---------------------------------------------------------------------------

// interactSendLoop reads lines from r and writes each non-empty line as a
// single message to w. Line-oriented: each Enter sends immediately (matches
// `fir send` and `tmux send-keys ... Enter`). First-line sigils !/+/\ are
// parsed by sendMsg. Closes w when r reaches EOF.
func interactSendLoop(r io.Reader, w io.WriteCloser) {
	defer w.Close()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := sendMsg(w, []string{line}, ""); err != nil {
			fmt.Fprintf(os.Stderr, "warning: send: %v\n", err)
			return
		}
	}
}

func runObserveTail(idPrefix, cwdFlag string, jsonOut, interact, fullText bool) error {
	s, err := resolveSidecar(idPrefix, cwdFlag)
	if err != nil {
		return err
	}
	if s.StorePath == "" {
		return fmt.Errorf("session %s has no transcript on disk (in-memory session)", s.SessionID[:min(8, len(s.SessionID))])
	}

	// --interact: start the send loop concurrently so input and tail share
	// one terminal. tmux users should prefer separate panes + `fir send`.
	if interact {
		if s.SocketPath == "" {
			fmt.Fprintln(os.Stderr, "warning: --interact requested but session has no input socket (read-only)")
		} else {
			conn, cerr := net.Dial("unix", s.SocketPath)
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "warning: --interact: connect socket: %v (continuing read-only)\n", cerr)
			} else {
				go interactSendLoop(os.Stdin, conn)
			}
		}
	}

	f, err := os.Open(s.StorePath)
	if err != nil {
		return fmt.Errorf("open transcript %s: %w", s.StorePath, err)
	}
	defer f.Close()

	// Live-tail loop: read until EOF, then poll os.Stat for size growth.
	// Sleep 100ms between polls — trivially correct, no per-uid watch limit,
	// ~10 syscalls/sec when idle.
	br := bufio.NewReader(f)
	var (
		buf       []byte
		lastSize  int64
		formatter = newTranscriptFormatter(os.Stdout, jsonOut, fullText)
	)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			// Could be a partial line if we hit a write mid-flush — accumulate.
			buf = append(buf, line...)
			if buf[len(buf)-1] == '\n' {
				formatter.write(buf[:len(buf)-1])
				buf = buf[:0]
			}
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, io.EOF) {
			return fmt.Errorf("read transcript: %w", err)
		}
		// EOF — poll for growth.
		for {
			time.Sleep(100 * time.Millisecond)
			st, serr := os.Stat(s.StorePath)
			if serr != nil {
				// File vanished (rotated?). Exit cleanly rather than spin.
				return nil
			}
			curSize := st.Size()
			if curSize > lastSize || curSize > offset(f) {
				lastSize = curSize
				break
			}
			// No new data. If the session has ended, stop tailing.
			if s.sidecarPath != "" {
				if fresh, ferr := loadSidecar(s.sidecarPath); ferr == nil && fresh.Status == "ended" {
					return nil
				}
			}
		}
	}
}

// offset returns the current read offset of f, or 0 on error.
func offset(f *os.File) int64 {
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0
	}
	return pos
}

// ---------------------------------------------------------------------------
// Formatter
// ---------------------------------------------------------------------------

// transcriptFormatter renders SessionEntry JSONL lines as human-readable text
// (or, with --json, dumps them verbatim).
type transcriptFormatter struct {
	w        io.Writer
	rawJSON  bool
	fullText bool // disable all truncation (for agent consumers)
	color    bool
}

func newTranscriptFormatter(w io.Writer, rawJSON, fullText bool) *transcriptFormatter {
	color := false
	if f, ok := w.(*os.File); ok {
		// Honour NO_COLOR (https://no-color.org).
		color = isTerminal(f) && os.Getenv("NO_COLOR") == ""
	}
	return &transcriptFormatter{w: w, rawJSON: rawJSON, fullText: fullText, color: color}
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// write renders one JSONL record.
func (tf *transcriptFormatter) write(line []byte) {
	if tf.rawJSON {
		tf.w.Write(line)
		tf.w.Write([]byte{'\n'})
		return
	}
	rendered := tf.render(line)
	if rendered == "" {
		return
	}
	fmt.Fprintln(tf.w, rendered)
}

// render decodes one JSONL line into a human-readable string. Returns ""
// for entry types we deliberately don't show.
func (tf *transcriptFormatter) render(line []byte) string {
	var probe struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message,omitempty"`
		Provider  string          `json:"provider,omitempty"`
		ModelID   string          `json:"modelId,omitempty"`
		Summary   string          `json:"summary,omitempty"`
		Name      string          `json:"name,omitempty"`
		Command   string          `json:"command,omitempty"`
		Args      string          `json:"args,omitempty"`
		PlanTitle string          `json:"planTitle,omitempty"`
		// Header fields (first line only).
		Version int    `json:"version,omitempty"`
		ID      string `json:"id,omitempty"`
		Cwd     string `json:"cwd,omitempty"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return tf.dim("?? " + string(line))
	}
	ts := tf.shortTime(probe.Timestamp)
	prefix := ""
	if ts != "" {
		prefix = tf.dim("["+ts+"] ") + " "
	}

	switch probe.Type {
	case "session":
		// Header line (first record). Render as a session-banner.
		return tf.bold("◆ session "+probe.ID[:min(8, len(probe.ID))]) +
			"  " + tf.dim("v"+fmt.Sprint(probe.Version)) +
			"  cwd=" + probe.Cwd
	case "message":
		return prefix + tf.renderMessage(probe.Message)
	case "model_change":
		return prefix + tf.dim(fmt.Sprintf("✎ model → %s/%s", probe.Provider, probe.ModelID))
	case "thinking_level_change":
		return prefix + tf.dim("✎ thinking level changed")
	case "compaction":
		return prefix + tf.color2("⟳", "yellow") + " compaction: " + probe.Summary
	case "session_info":
		return prefix + tf.dim("✎ session named: ") + probe.Name
	case "command":
		args := probe.Args
		if !tf.fullText {
			args = truncRunes(args, 60)
		}
		return prefix + tf.dim("$ ") + probe.Command + " " + args
	case "plan_update":
		return prefix + "📋 plan: " + probe.PlanTitle
	case "label", "branch_summary", "custom", "custom_message":
		return "" // not interesting for live observation
	default:
		return prefix + tf.dim(probe.Type)
	}
}

// renderMessage extracts a brief representation from an `ai.Message` blob.
// We only need to show role + a short content summary; full content is
// available via --json.
func (tf *transcriptFormatter) renderMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return tf.dim("(empty message)")
	}
	var m struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return tf.dim("(unparseable message)")
	}
	body := summariseContent(m.Content, tf.fullText)
	switch m.Role {
	case "user":
		return tf.color2("▸ user", "cyan") + "  " + body
	case "assistant":
		return tf.color2("◆ assistant", "green") + "  " + body
	case "tool":
		return tf.color2("✓ tool", "magenta") + "  " + body
	case "system":
		return tf.dim("· system  ") + body
	default:
		return tf.dim("· "+m.Role+" ") + body
	}
}

// summariseContent reduces a Message.Content (string or array of blocks) to a
// single human-readable form. When full is true, no truncation is applied.
func summariseContent(c any, full bool) string {
	limit := 200
	if full {
		limit = 0 // 0 = no truncation
	}
	switch v := c.(type) {
	case string:
		return truncOneLine(v, limit)
	case []any:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			t, _ := m["type"].(string)
			switch t {
			case "text":
				if s, _ := m["text"].(string); s != "" {
					parts = append(parts, truncOneLine(s, limit))
				}
			case "tool_use":
				name, _ := m["name"].(string)
				parts = append(parts, "→ "+name)
			case "tool_result":
				content, _ := m["content"].(string)
				rLimit := limit
				if rLimit > 0 {
					rLimit = 100
				}
				parts = append(parts, "← "+truncOneLine(content, rLimit))
			case "image":
				parts = append(parts, "[image]")
			case "thinking":
				parts = append(parts, "(thinking)")
			}
		}
		return strings.Join(parts, "  ")
	default:
		return "(unrenderable)"
	}
}

func truncOneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if max <= 0 {
		return s // 0 = no truncation
	}
	return truncRunes(s, max)
}

// truncRunes truncates s to max runes, appending "…" if any runes were
// dropped. Always cuts at a rune boundary.
func truncRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

func (tf *transcriptFormatter) shortTime(rfc3339 string) string {
	if rfc3339 == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, rfc3339)
	if err != nil {
		t, err = time.Parse(time.RFC3339, rfc3339)
		if err != nil {
			return ""
		}
	}
	return t.Local().Format("15:04:05")
}

// ANSI colour helpers — no-ops when color is off.
func (tf *transcriptFormatter) dim(s string) string {
	if !tf.color {
		return s
	}
	return "\x1b[2m" + s + "\x1b[0m"
}

func (tf *transcriptFormatter) bold(s string) string {
	if !tf.color {
		return s
	}
	return "\x1b[1m" + s + "\x1b[0m"
}

func (tf *transcriptFormatter) color2(s, name string) string {
	if !tf.color {
		return s
	}
	code := map[string]string{
		"cyan":    "36",
		"green":   "32",
		"yellow":  "33",
		"magenta": "35",
		"red":     "31",
	}[name]
	if code == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
