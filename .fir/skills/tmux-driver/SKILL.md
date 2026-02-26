---
name: tmux-driver
description: Drive interactive CLIs (python, gdb, etc.) via tmux with named project sessions and multi-window support
---

Drive interactive programs through tmux using a private socket. Works on Linux and macOS with stock tmux.

## Setup

```bash
# All commands use a wrapper that handles socket/session boilerplate:
source ./scripts/tmux-helpers.sh

# Start a named session (one per project/task):
tm-new myproject        # creates session "myproject" with window "shell"
tm-new myproject py     # creates session "myproject" with window "py"
```

**Optional: Custom socket directory**

By default, sessions use `${TMPDIR:-/tmp}/claude-tmux-sockets/claude.sock`. To use a different socket directory:

```bash
export CLAUDE_TMUX_SOCKET_DIR=/path/to/sockets
source ./scripts/tmux-helpers.sh
tm-new myproject
```

This is useful if you need to isolate sessions per project, workspace, or system.

After starting a session, **always** tell the user:
```
Monitor: tm-attach myproject
Capture: tm-capture myproject
```

## Core Commands

| Command | What it does |
|---|---|
| `tm-new NAME [WINDOW]` | New session, optional first window name |
| `tm-send NAME TEXT` | Send literal text + Enter to active window |
| `tm-sendraw NAME KEYS...` | Send raw keys (C-c, Escape, etc.) |
| `tm-capture NAME [LINES]` | Capture last N lines (default 200) |
| `tm-wait NAME PATTERN [TIMEOUT]` | Poll for regex, exit 0 on match |
| `tm-win NAME WINNAME [CMD]` | Create new window, optionally run CMD |
| `tm-select NAME WINNAME` | Switch active window |
| `tm-list [NAME]` | List sessions, or windows in a session |
| `tm-killwin NAME WINNAME` | Kill a specific window |
| `tm-renamewin NAME OLDNAME NEWNAME` | Rename a window |
| `tm-kill NAME` | Kill session |
| `tm-attach NAME` | Print attach command for user |

## Multi-Window Sessions

Each session can have multiple named windows. Create, rename, and manage them:

```bash
tm-new myproject                          # session with default window
tm-win myproject server 'make run'        # new window "server" running a command
tm-win myproject repl                     # new window "repl"
tm-renamewin myproject repl python        # rename "repl" to "python"
tm-select myproject python                # switch active window
tm-send myproject 'print("hello")'       # sends to active window (python)
tm-capture myproject                      # captures active window
tm-killwin myproject server               # delete the "server" window
```

To target a specific window regardless of which is active:

```bash
tm-send myproject:python 'x = 1'
tm-capture myproject:python
```

**What happens if the user attaches and changes the active window?** The agent's `tm-send`/`tm-capture` without a `:window` suffix go to whichever window is currently active — so if the user switches windows while attached, the agent's next unqualified command hits the wrong window. **Always use `NAME:WINDOW` targeting** when running multiple windows to avoid races. The helper commands accept either `NAME` (active window) or `NAME:WINDOW` (explicit).

## Multiple Named Sessions

Use one session per project or task:

```bash
tm-new frontend        # React dev server
tm-new backend         # Go API server  
tm-new debug           # gdb/lldb session
tm-list                # shows all sessions
```

Sessions are isolated — different sessions can't interfere with each other.

## Recipes

**Python REPL** (always set PYTHON_BASIC_REPL=1):
```bash
tm-new proj py
tm-send proj 'PYTHON_BASIC_REPL=1 python3 -q'
tm-wait proj '>>>'
tm-send proj 'print("hello")'
```

**Debugger** (use lldb by default):
```bash
tm-new proj dbg
tm-send proj 'lldb ./myapp'
tm-wait proj '(lldb)'
tm-send proj 'b main'
```

**Long-running server**:
```bash
tm-win proj server 'make run'
tm-wait proj:server 'Listening on'
tm-send proj:repl 'curl localhost:8080'
```

## Cleanup

```bash
tm-kill myproject       # kill one session
tm-killall              # kill all sessions on the socket
```
