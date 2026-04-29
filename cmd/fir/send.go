package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// runSend implements the "fir send" subcommand.
//
// Usage:
//
//	fir send <id-prefix>             interactive: read lines, send to session
//	fir send <id-prefix> --steer     lines sent as deliver_as=steer (interrupt)
//	fir send <id-prefix> --follow    lines sent as deliver_as=followUp (queue)
//	fir send <id-prefix> --cwd .     resolve session by cwd
//	echo "msg" | fir send <id>       pipe single message (non-interactive)
//
// Wire format to observe.py socket: NDJSON, one object per line:
//
//	{"deliver_as": "", "content": "message text"}
//
// Interactive mode: each non-empty line is sent on Enter (line-oriented, like
// ssh / nc). Blank lines are silently skipped. First-line sigils on each
// message:
//
//	!message    → deliver_as=steer (interrupt current turn)
//	+message    → deliver_as=followUp (queue for after current turn)
//	\!...       → escaped literal '!' prefix
//	\+...       → escaped literal '+' prefix
func runSend() error {
	args := os.Args[2:] // skip "fir send"

	var (
		idPrefix   string
		cwdFlag    string
		steerMode  bool
		followMode bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--steer":
			steerMode = true
		case a == "--follow":
			followMode = true
		case a == "--cwd":
			if i+1 >= len(args) {
				return errors.New("--cwd requires an argument")
			}
			cwdFlag = args[i+1]
			i++
		case strings.HasPrefix(a, "--cwd="):
			cwdFlag = strings.TrimPrefix(a, "--cwd=")
		case a == "--help" || a == "-h":
			fmt.Fprint(os.Stderr, sendUsage)
			return nil
		case strings.HasPrefix(a, "--"):
			return fmt.Errorf("unknown flag: %s\n%s", a, sendUsage)
		default:
			if idPrefix != "" {
				return fmt.Errorf("unexpected extra argument: %s\n%s", a, sendUsage)
			}
			idPrefix = a
		}
	}

	if idPrefix == "" && cwdFlag == "" {
		return fmt.Errorf("session id or --cwd required\n%s", sendUsage)
	}
	if steerMode && followMode {
		return errors.New("--steer and --follow are mutually exclusive")
	}

	s, err := resolveSidecar(idPrefix, cwdFlag)
	if err != nil {
		return err
	}
	if s.SocketPath == "" {
		return fmt.Errorf("session %s has no input socket (ended or not started)",
			s.SessionID[:min(8, len(s.SessionID))])
	}

	conn, err := net.Dial("unix", s.SocketPath)
	if err != nil {
		return fmt.Errorf("connect to session %s: %w\n(is the session still running?)",
			s.SessionID[:min(8, len(s.SessionID))], err)
	}
	defer conn.Close()

	// Greet the user when stdin is interactive.
	if isTerminal(os.Stdin) {
		name := s.SessionName
		if name == "" {
			name = s.SessionID[:min(8, len(s.SessionID))]
		}
		fmt.Fprintf(os.Stderr, "Connected to session %s", name)
		if s.SessionName != "" {
			fmt.Fprintf(os.Stderr, " (%s)", s.SessionID[:min(8, len(s.SessionID))])
		}
		fmt.Fprintln(os.Stderr, ". Enter to send. Ctrl-\\ to disconnect.")
		fmt.Fprintln(os.Stderr, "  ! prefix → steer (interrupt)   + prefix → followUp (queue)")
	}

	// Trap Ctrl-\ (SIGQUIT) for clean detach.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGQUIT)
	go func() {
		<-quit
		conn.Close()
		os.Exit(0)
	}()

	// Default deliver_as from flags.
	defaultDeliverAs := ""
	if steerMode {
		defaultDeliverAs = "steer"
	} else if followMode {
		defaultDeliverAs = "followUp"
	}

	scanner := bufio.NewScanner(os.Stdin)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue // skip blank lines silently
		}
		if err := sendMsg(conn, []string{line}, defaultDeliverAs); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	return nil
}

// sendMsg encodes one message and writes it to w.
// First-line sigils (! / +) override defaultDeliverAs unless escaped with \.
func sendMsg(w io.Writer, lines []string, defaultDeliverAs string) error {
	if len(lines) == 0 {
		return nil
	}

	first := lines[0]
	deliverAs := defaultDeliverAs
	switch {
	case strings.HasPrefix(first, "\\!") || strings.HasPrefix(first, "\\+"):
		// Escape: strip the leading backslash.
		lines[0] = first[1:]
	case strings.HasPrefix(first, "!"):
		deliverAs = "steer"
		lines[0] = first[1:]
	case strings.HasPrefix(first, "+"):
		deliverAs = "followUp"
		lines[0] = first[1:]
	}

	content := strings.Join(lines, "\n")
	if strings.TrimSpace(content) == "" {
		return nil
	}

	data, err := json.Marshal(map[string]string{
		"deliver_as": deliverAs,
		"content":    content,
	})
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

const sendUsage = `usage: fir send <id-prefix> [--steer | --follow] [--cwd <path>]

  fir send <id-prefix>            interactive: Enter to send each line, Ctrl-\ to disconnect
  fir send <id-prefix> --steer    all messages sent as steer (interrupt)
  fir send <id-prefix> --follow   all messages sent as followUp (queue)
  fir send --cwd .                resolve session by current directory
  echo "fix the bug" | fir send <id>   pipe a single message

First-line sigils (override per-message):
  !message     → steer (interrupt current turn)
  +message     → followUp (queue after current turn)
  \!message    → literal '!' (escaped)
`
