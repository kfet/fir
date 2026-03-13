package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/kfet/fir/pkg/ptydriver"
)

// runPTY implements the "fir pty" subcommand.
// Usage:
//
//	fir pty serve              — start the PTY server (foreground)
//	fir pty new NAME [WINDOW]  — create session
//	fir pty win NAME WINDOW [CMD] — create window
//	fir pty send TARGET TEXT   — send text + Enter
//	fir pty sendraw TARGET DATA — send raw bytes
//	fir pty capture TARGET [LINES] — capture output
//	fir pty wait TARGET PATTERN [TIMEOUT] — wait for pattern
//	fir pty list [SESSION]     — list sessions/windows
//	fir pty kill NAME          — kill session
//	fir pty killwin NAME WIN   — kill window
//	fir pty alive TARGET       — check if alive
//	fir pty shutdown           — stop server
func runPTY() {
	args := os.Args[2:] // skip "fir pty"
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: fir pty <command> [args...]")
		fmt.Fprintln(os.Stderr, "commands: serve, new, win, send, sendraw, capture, wait, list, kill, killwin, alive, shutdown")
		os.Exit(1)
	}

	cmd := args[0]
	args = args[1:]

	if cmd == "serve" {
		sock := ptydriver.DefaultSocketPath()
		srv, err := ptydriver.NewServer(sock)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "PTY server listening on %s\n", sock)
		fmt.Println(sock) // stdout for scripts to capture
		if err := srv.Serve(); err != nil {
			// Normal shutdown.
		}
		return
	}

	// All other commands are client calls.
	client := &ptydriver.Client{SocketPath: ptydriver.DefaultSocketPath()}

	switch cmd {
	case "new":
		need(args, 1, "fir pty new NAME [WINDOW]")
		session := args[0]
		window := "shell"
		if len(args) > 1 {
			window = args[1]
		}
		call(client, "new", map[string]string{"session": session, "window": window})

	case "win":
		need(args, 2, "fir pty win NAME WINDOW [CMD]")
		p := map[string]string{"session": args[0], "window": args[1]}
		if len(args) > 2 {
			p["command"] = args[2]
		}
		call(client, "new_window", p)

	case "send":
		need(args, 2, "fir pty send TARGET TEXT")
		call(client, "send", map[string]string{"target": args[0], "text": args[1]})

	case "sendraw":
		need(args, 2, "fir pty sendraw TARGET DATA")
		call(client, "send_raw", map[string]string{"target": args[0], "data": args[1]})

	case "capture":
		need(args, 1, "fir pty capture TARGET [LINES]")
		lines := 200
		if len(args) > 1 {
			if n, err := strconv.Atoi(args[1]); err == nil {
				lines = n
			}
		}
		resp, err := client.Call("capture", map[string]any{"target": args[0], "lines": lines})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if resp.Error != "" {
			fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
			os.Exit(1)
		}
		var result struct {
			Output string `json:"output"`
		}
		json.Unmarshal(resp.Result, &result)
		fmt.Print(result.Output)

	case "wait":
		need(args, 2, "fir pty wait TARGET PATTERN [TIMEOUT]")
		timeout := 15
		if len(args) > 2 {
			if n, err := strconv.Atoi(args[2]); err == nil {
				timeout = n
			}
		}
		call(client, "wait", map[string]any{"target": args[0], "pattern": args[1], "timeout": timeout})

	case "list":
		session := ""
		if len(args) > 0 {
			session = args[0]
		}
		resp, err := client.Call("list", map[string]string{"session": session})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if resp.Error != "" {
			fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
			os.Exit(1)
		}
		var items []string
		json.Unmarshal(resp.Result, &items)
		for _, item := range items {
			fmt.Println(item)
		}

	case "kill":
		need(args, 1, "fir pty kill NAME")
		call(client, "kill", map[string]string{"session": args[0]})

	case "killwin":
		need(args, 2, "fir pty killwin NAME WINDOW")
		call(client, "kill_window", map[string]string{"session": args[0], "window": args[1]})

	case "alive":
		need(args, 1, "fir pty alive TARGET")
		resp, err := client.Call("alive", map[string]string{"target": args[0]})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if resp.Error != "" {
			fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
			os.Exit(1)
		}
		var result struct {
			Alive bool `json:"alive"`
		}
		json.Unmarshal(resp.Result, &result)
		if result.Alive {
			fmt.Println("alive")
		} else {
			fmt.Println("dead")
			os.Exit(1)
		}

	case "shutdown":
		call(client, "shutdown", nil)

	default:
		fmt.Fprintf(os.Stderr, "unknown pty command: %s\n", cmd)
		os.Exit(1)
	}
}

func need(args []string, min int, usage string) {
	if len(args) < min {
		fmt.Fprintf(os.Stderr, "usage: %s\n", usage)
		os.Exit(1)
	}
}

func call(client *ptydriver.Client, method string, params any) {
	resp, err := client.Call(method, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}
}
