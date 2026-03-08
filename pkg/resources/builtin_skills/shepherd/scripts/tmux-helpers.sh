#!/usr/bin/env bash
# tmux helper functions — source this file to get tm-* commands.
# All functions use a shared private socket under CLAUDE_TMUX_SOCKET_DIR.

_TM_SOCKET_DIR="${CLAUDE_TMUX_SOCKET_DIR:-${TMPDIR:-/tmp}/claude-tmux-sockets}"
mkdir -p "$_TM_SOCKET_DIR"
_TM_SOCKET="$_TM_SOCKET_DIR/claude.sock"

_tm() { tmux -S "$_TM_SOCKET" "$@"; }

# Parse "name" or "name:window" into session and target
_tm_target() {
  local spec="$1" lines="${2:-}"
  if [[ "$spec" == *:* ]]; then
    echo "$spec"
  else
    echo "$spec"
  fi
}

tm-new() {
  local name="${1:?usage: tm-new NAME [WINDOW]}"
  local win="${2:-shell}"
  _tm new -d -s "$name" -n "$win"
  echo "Session '$name' started (window: $win)"
  echo "  Monitor: tmux -S $_TM_SOCKET attach -t $name"
  echo "  Capture: tm-capture $name"
}

tm-send() {
  local target="${1:?usage: tm-send TARGET TEXT}"
  shift
  _tm send-keys -t "$target" -l -- "$*"
  _tm send-keys -t "$target" Enter
}

tm-sendraw() {
  local target="${1:?usage: tm-sendraw TARGET KEYS...}"
  shift
  _tm send-keys -t "$target" "$@"
}

tm-capture() {
  local target="${1:?usage: tm-capture TARGET [LINES]}"
  local lines="${2:-200}"
  _tm capture-pane -p -J -t "$target" -S "-$lines"
}

tm-wait() {
  local target="${1:?usage: tm-wait TARGET PATTERN [TIMEOUT]}"
  local pattern="${2:?pattern required}"
  local timeout="${3:-15}"
  local deadline=$(( $(date +%s) + timeout ))

  while true; do
    local text
    text="$(_tm capture-pane -p -J -t "$target" -S -1000 2>/dev/null || true)"
    if printf '%s\n' "$text" | grep -qE -- "$pattern"; then
      return 0
    fi
    if (( $(date +%s) >= deadline )); then
      echo "Timeout after ${timeout}s waiting for: $pattern" >&2
      printf '%s\n' "$text" >&2
      return 1
    fi
    sleep 0.5
  done
}

tm-win() {
  local name="${1:?usage: tm-win NAME WINNAME [CMD]}"
  local winname="${2:?window name required}"
  local cmd="${3:-}"
  if [[ -n "$cmd" ]]; then
    _tm new-window -t "$name" -n "$winname" "$cmd"
  else
    _tm new-window -t "$name" -n "$winname"
  fi
}

tm-select() {
  local name="${1:?usage: tm-select NAME WINNAME}"
  local winname="${2:?window name required}"
  _tm select-window -t "$name:$winname"
}

tm-list() {
  if [[ -n "${1:-}" ]]; then
    _tm list-windows -t "$1" -F '  #{window_index}: #{window_name} #{?window_active,(active),}'
  else
    _tm list-sessions -F '#{session_name} (#{session_windows} windows, #{?session_attached,attached,detached})'
  fi
}

tm-kill() {
  local name="${1:?usage: tm-kill NAME}"
  _tm kill-session -t "$name"
}

tm-killall() {
  _tm kill-server 2>/dev/null || true
}

tm-attach() {
  local name="${1:?usage: tm-attach NAME}"
  echo "tmux -S $_TM_SOCKET attach -t $name"
}

tm-killwin() {
  local name="${1:?usage: tm-killwin NAME WINNAME}"
  local winname="${2:?window name required}"
  _tm kill-window -t "$name:$winname"
}

tm-renamewin() {
  local name="${1:?usage: tm-renamewin NAME OLDNAME NEWNAME}"
  local oldname="${2:?old window name required}"
  local newname="${3:?new window name required}"
  _tm rename-window -t "$name:$oldname" "$newname"
}
