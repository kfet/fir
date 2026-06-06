#!/usr/bin/env bash
# Development wrapper: `go run` the Go demo extension on each launch.
#
# fir discovers a sub-directory extension by finding an executable `main`
# (or main.sh). Symlink or copy this directory into an extensions dir, e.g.:
#
#   ln -s "$(pwd)" ~/.config/fir/extensions/go-demo
#
# and fir will run this script as the entry point. For production, prefer a
# compiled binary (no per-launch compile latency):
#
#   go build -o ~/.config/fir/extensions/go-demo/main ./examples/demo
#
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
exec go run "$DIR"
