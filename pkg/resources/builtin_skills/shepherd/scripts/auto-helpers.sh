#!/usr/bin/env bash
# auto-helpers.sh — automatically selects the best available backend.
# Source this instead of tmux-helpers.sh or pty-helpers.sh.
#
# Priority:
#   1. tmux (if installed) — supports human attach
#   2. fir pty (built-in)  — no external deps

_SKILL_SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if command -v tmux &>/dev/null; then
  source "$_SKILL_SCRIPTS_DIR/tmux-helpers.sh"
  export _TM_BACKEND="tmux"
else
  source "$_SKILL_SCRIPTS_DIR/pty-helpers.sh"
  export _TM_BACKEND="fir-pty"
fi
