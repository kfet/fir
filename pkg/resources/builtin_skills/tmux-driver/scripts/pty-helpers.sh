#!/usr/bin/env bash
# pty-helpers.sh — drop-in replacement for tmux-helpers.sh using fir's
# built-in PTY driver. No tmux required.
#
# Source this file to get tm-* commands that work identically to the
# tmux-based versions but use "fir pty" under the hood.

_FIR_BIN="${FIR_BIN:-fir}"
_PTY_SERVER_PID=""

# Ensure the PTY server is running. Idempotent.
_pty_ensure_server() {
  local sock
  sock=$("$_FIR_BIN" pty list 2>/dev/null && return 0)

  # Server not running — start it in background.
  "$_FIR_BIN" pty serve &
  _PTY_SERVER_PID=$!
  sleep 0.3 # give it time to bind
}

tm-new() {
  local name="${1:?usage: tm-new NAME [WINDOW]}"
  local win="${2:-shell}"
  _pty_ensure_server
  "$_FIR_BIN" pty new "$name" "$win"
  echo "Session '$name' started (window: $win)"
  echo "  Capture: tm-capture $name"
}

tm-send() {
  local target="${1:?usage: tm-send TARGET TEXT}"
  shift
  "$_FIR_BIN" pty send "$target" "$*"
}

tm-sendraw() {
  local target="${1:?usage: tm-sendraw TARGET KEYS...}"
  shift
  "$_FIR_BIN" pty sendraw "$target" "$*"
}

tm-capture() {
  local target="${1:?usage: tm-capture TARGET [LINES]}"
  local lines="${2:-200}"
  "$_FIR_BIN" pty capture "$target" "$lines"
}

tm-wait() {
  local target="${1:?usage: tm-wait TARGET PATTERN [TIMEOUT]}"
  local pattern="${2:?pattern required}"
  local timeout="${3:-15}"
  "$_FIR_BIN" pty wait "$target" "$pattern" "$timeout"
}

tm-win() {
  local name="${1:?usage: tm-win NAME WINNAME [CMD]}"
  local winname="${2:?window name required}"
  local cmd="${3:-}"
  if [[ -n "$cmd" ]]; then
    "$_FIR_BIN" pty win "$name" "$winname" "$cmd"
  else
    "$_FIR_BIN" pty win "$name" "$winname"
  fi
}

tm-list() {
  if [[ -n "${1:-}" ]]; then
    "$_FIR_BIN" pty list "$1"
  else
    "$_FIR_BIN" pty list
  fi
}

tm-kill() {
  local name="${1:?usage: tm-kill NAME}"
  "$_FIR_BIN" pty kill "$name"
}

tm-killall() {
  "$_FIR_BIN" pty shutdown 2>/dev/null || true
}

tm-killwin() {
  local name="${1:?usage: tm-killwin NAME WINNAME}"
  local winname="${2:?window name required}"
  "$_FIR_BIN" pty killwin "$name" "$winname"
}

tm-attach() {
  local name="${1:?usage: tm-attach NAME}"
  # Check if tmux is available for real attach.
  if command -v tmux &>/dev/null; then
    echo "tmux attach not available with built-in PTY driver."
    echo "Use: tm-capture $name"
  else
    echo "Use: tm-capture $name"
  fi
}

# Bulk helpers — simplified versions for the PTY driver.

tm-status() {
  local name="${1:?usage: tm-status SESSION}"
  local windows
  windows=$("$_FIR_BIN" pty list "$name" 2>/dev/null) || return 1
  while IFS= read -r win; do
    local last alive_status
    last=$("$_FIR_BIN" pty capture "$name:$win" 5 2>/dev/null | grep -v '^$' | tail -1)
    last="${last:-idle}"
    if "$_FIR_BIN" pty alive "$name:$win" &>/dev/null; then
      alive_status="alive"
    else
      alive_status="dead"
    fi
    printf '%-20s  %-8s  %s\n' "$win" "$alive_status" "${last:0:60}"
  done <<< "$windows"
}

tm-bulk-rename() {
  # No-op: PTY driver doesn't have visual window names to rename.
  :
}

tm-loop-tick() {
  local session="${1:?usage: tm-loop-tick SESSION WORKTREE}"
  local worktree="${2:?worktree path required}"
  echo "=== $(date +%H:%M:%S) ==="
  echo "--- git ---"
  git -C "$worktree" log --oneline -5 2>/dev/null || echo "(no commits)"
  echo "--- workers ---"
  tm-status "$session"
}
